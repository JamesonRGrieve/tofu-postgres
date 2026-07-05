<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
# Design

`tofu-postgres` is a native OpenTofu/Terraform provider for a PostgreSQL host's
**installed state, config files, service, and HA topology**, driven over an
SSH/CLI transport. Logical objects (databases, roles, grants, schema) are
deliberately out of scope and composed from `cyrilgdn/postgresql` at the
consumer layer.

## Architecture

Two layers, mirroring the sibling providers (`tofu-opnsense`, `tofu-proxmox`):

```
main.go                      provider server entry (address jamesonrgrieve/postgres)
internal/postgres/           transport + pure logic (NO terraform imports)
  ssh.go                     os/exec ssh transport (key/cert only, temp-file key_pem)
  client.go                  Client{SSH}, RunFunc seam, Command, RunCommands
  paths.go                   Debian cluster layout (config/data dirs, unit names)
  config.go                  ConfSetting + RenderConfD + ParseConfD + NeedsRestart
  pghba.go                   HBAEntry + RenderPgHba/ParseHBABlock + reassemble command
  inventory.go               dpkg / pg_lsclusters parsing
  conninfo.go                libpq primary_conninfo builder
  streaming.go               streaming primary/standby command builders
  repmgr.go                  repmgr.conf render + primary/standby command builders
  patroni.go                 patroni.yml render + service command builders
  mode.go                    HAMode/NodeRole + NodeSpec + NodeCommands dispatch
internal/provider/           terraform-plugin-framework wiring
  provider.go                SSH-transport-only provider config
  common.go                  shared configure/run/list/import helpers
  package_resource.go        postgres_package
  config_resource.go         postgres_config
  service_resource.go        postgres_service
  cluster_resource.go        postgres_cluster (declarative record + validation)
  cluster_node_resource.go   postgres_cluster_node (mode-dispatched bring-up)
```

The **transport/pure layer is framework-free** so it stays unit-testable in
isolation; the provider layer is a thin adapter. Framework types never leak
downward.

## SSH transport

PostgreSQL has no management REST API — package install, config files, service
control, and HA bring-up are all host CLI. The provider invokes the system `ssh`
binary via `os/exec` (no `golang.org/x/crypto/ssh` dependency), reusing the
lab's SSH machinery (OpenBao-signed certs / agent / `ssh_config`). Auth is
**key/cert only — never a password, never sshpass**. `ssh_key_pem` (e.g. an
OpenBao-signed key) is materialized to a temp `0600` file per call and removed
after; `ssh_key_file` and `ssh_config`/agent paths never touch key material.
Transient connection resets (relay `nc -e` pipes) are retried with backoff.

A rendered config file (postgresql.conf drop-in, pg_hba block, repmgr.conf,
patroni.yml) is piped to the remote `cat`/`tee` via **stdin**, so the content
never appears in the process argument list or a shell-quoted command.

## Injected-exec testing

Device-apply logic is expressed as an ordered `[]postgres.Command` built by
**pure functions** (`packageCommands`, `buildConfigCommands`,
`buildServiceCommands`, `NodeCommands`, and the HA renderers) and executed
through a `postgres.RunFunc` seam. The test suite injects a recording fake for
that seam, so every resource's create/update dispatch — including the full HA
mode fan-out — is verified **without touching a live host**. `go test ./...` is
hermetic by default; there is no path by which the suite applies to a device.

## Resource semantics

- **Manage-declared-only (`postgres_config`).** State reflects only the config
  keys the configuration declares; an unset key is neither written to the
  conf.d drop-in nor reconciled on read. The `pg_hba` list fully owns a
  marker-delimited block (`# BEGIN tofu-managed … # END tofu-managed`) inside
  `pg_hba.conf`, leaving packaged/operator lines untouched. Fix a spurious diff
  in the read/subset logic, never by widening stored state.
- **Reload vs restart.** An update reloads the cluster (`pg_ctlcluster … reload`)
  unless a **postmaster-context** key is declared — `shared_buffers`,
  `max_connections`, `listen_addresses` only take effect on a full restart, so
  declaring any of them makes the update restart instead.
- **Import-to-0-diff.** Every stateful resource implements `ImportState`;
  importing then planning yields no diff. Config/service read-back parses the
  device files/units into the declared attributes.
- **No-op deletes.** Removing a resource stops managing it; it does **not**
  uninstall PostgreSQL, stop the service, or tear a node out of a live cluster —
  those are outage-class actions that must be driven deliberately, not on a
  `terraform destroy`.

## ZFS/COW WAL tuning

On a ZFS (copy-on-write) datadir, WAL pre-allocation and recycling are pure
overhead: zeroing a new segment (`wal_init_zero`) and renaming an old one
(`wal_recycle`) both force full-record rewrites the COW layer never benefits
from. Set both `false` on ZFS-backed clusters. `full_page_writes` is left ON —
a 16K WAL record still spans two 8K heap pages, so torn-page protection is still
required. `password_encryption` defaults to `scram-sha-256` (md5 is deprecated).

## HA modes

`postgres_cluster.ha_mode` selects the topology; `postgres_cluster_node`
dispatches its bring-up on the same mode via `postgres.NodeCommands`.

### streaming — plain physical replication

- **Primary:** an HA drop-in sets `wal_level=replica`, `max_wal_senders`,
  `max_replication_slots`, `hot_standby=on`; a physical replication slot is
  created idempotently per standby.
- **Standby:** the local cluster is stopped, `pg_basebackup -R -X stream
  --slot=<slot>` clones from the primary (writing `primary_conninfo` +
  `standby.signal`), then it is started. The clone is guarded on an empty
  datadir (`PG_VERSION` absent) so a re-apply is a no-op.

### repmgr — managed streaming + automatic failover

- `repmgr.conf` is rendered per node (node_id, node_name, conninfo, data dir,
  `failover='automatic'`, promote/follow commands).
- **Primary:** `repmgr primary register`; **standby:** `repmgr standby clone`
  (guarded), start, `repmgr standby register`; **witness:** `repmgr witness
  register`. `repmgrd` is enabled for automatic failover.

### patroni — DCS-coordinated HA

- `patroni.yml` is rendered (scope, name, restapi, the etcd/consul DCS block,
  bootstrap dcs with `synchronous_mode`, postgresql connect/data/auth). No YAML
  dependency is added; the file is hand-rendered.
- Each node runs the same config and coordinates leadership through the DCS;
  Patroni self-elects (role is advisory). `postgres_cluster.dcs_reference` is
  required in this mode. **Do not** also manage the same cluster with
  `postgres_service` — Patroni owns the postgres process once running.

## Risk & verification owed

Cluster bring-up beyond the deterministic command sequences here is
**verification-owed** and **interruption-unsafe**:

- **`pg_basebackup` clones, promotions, and failovers must not be run
  synchronously against production.** A killed base-backup leaves a partial
  datadir; a mishandled promotion split-brains the cluster. Run them detached
  and durable, never on a timeout (per the workspace "fragile long-running
  operations" rule).
- **A config restart takes the cluster offline briefly**, and a bad
  `listen_addresses`/`pg_hba` change can lock out the very connections a
  consumer depends on. Treat every HA change as a production-change-safety event:
  **prove it on a lab twin in byte-for-byte identical form first, arm an
  out-of-band rollback (a datadir/DB snapshot restorable independently of the
  path the change could sever), and block on a real post-change health check on
  the user-visible surface** (a role connecting over the wire and running a
  query — not a loopback `pg_isready`).
- **Drive all live changes through the sanctioned pipeline** (prod-netbox →
  prod-semaphore). The provider's apply logic is present and unit-tested via
  injected exec, but it has **not** been exercised end-to-end against a live
  cluster in this baseline — that bring-up validation is the next step and is
  owed before any production use.
