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
  created idempotently per standby; and — when a `replication_user` is set — the
  `LOGIN REPLICATION` role the standbys authenticate as is created-or-synced over
  the local socket (peer auth, no superuser password; password fed on stdin so it
  never hits argv). This role is HA plumbing the mode cannot work without (the
  standby's `pg_basebackup` connects as it), in the same category as the physical
  slots — not a user-facing logical role (those stay at the consumer/`cyrilgdn`
  layer).
- **Standby:** the local cluster is stopped; a `~postgres/.pgpass` is written with
  the replication credential (so the walreceiver authenticates even if
  `pg_basebackup -R` omits the password from `primary_conninfo`); the datadir —
  which the fresh package install already populated — is **emptied** and
  `pg_basebackup -R -X stream --slot=<slot>` clones from the primary (writing
  `primary_conninfo` + `standby.signal`); then it is started. The clone is guarded
  on `primary_conninfo` already referencing the primary host in
  `postgresql.auto.conf` — the only state that is true exclusively after a real
  clone — so a re-apply is a no-op, a fresh node converges, and a previously
  half-brought-up standby self-heals. (It must NOT guard on `PG_VERSION`: every
  initialized datadir has it, so that guard skipped the clone on first apply and
  left the node a disconnected standalone.)

### repmgr — managed streaming + automatic failover

- **Package.** Every repmgr node's command list is prefixed with an install of
  the versioned `postgresql-<major>-repmgr` package (repmgr CLI + repmgrd) via
  the lock-wait apt prefix — nothing repmgr does works without it.
- **`repmgr.conf`** is rendered per node (node_id, node_name, conninfo, data dir,
  `pg_bindir` → `/usr/lib/postgresql/<major>/bin`, `failover='automatic'`). The
  `promote_command`/`follow_command` reference the **real** conf path
  (`/etc/postgresql/<major>/<cluster>/repmgr.conf`), not a hardcoded
  `/etc/repmgr.conf`, so repmgrd re-reads the same file.
- **`repmgrd` service.** `/etc/default/repmgrd` is written with
  `REPMGRD_ENABLED=yes` and `REPMGRD_CONF="<confPath>"` (the Debian repmgrd unit
  refuses to start without the enable flag and otherwise loads a non-existent
  `/etc/repmgr.conf`), then `systemctl enable --now repmgrd`.
- **Primary:** write conf → create the repmgr role (`LOGIN SUPERUSER
  REPLICATION`, create-or-ALTER idempotently, password on stdin) + the `repmgr`
  metadata database (idempotent via psql `\gexec` guarded on `pg_database`) over
  the local socket (peer auth) → `repmgr primary register --force` → enable
  repmgrd. The primary keeps its populated datadir.
- **Standby:** write conf → stop the local cluster → write a `~postgres/.pgpass`
  with the repmgr credential (`…:replication:repmgr:pw` **and**
  `…:repmgr:repmgr:pw`, on stdin) → **empty the datadir** (the fresh install
  populated it) and `repmgr standby clone --force` → start → `repmgr standby
  register --force` → enable repmgrd. The clone is guarded on `primary_conninfo`
  already referencing the primary host in `postgresql.auto.conf` (true only
  after a real clone) — **not** on `PG_VERSION` (every initialized datadir has
  it, so the old guard skipped the clone forever on a fresh node) — so a fresh
  node converges, a botched standby self-heals, and a re-apply is a no-op.
- **Witness:** write conf → create the repmgr role+database on the witness's own
  standalone postgres → `.pgpass` → `repmgr witness register --force` → enable
  repmgrd (a witness holds no data replica but joins repmgrd's failover quorum).

### patroni — DCS-coordinated HA

- **Package.** Each node installs `patroni` via the lock-wait apt prefix; apt
  Recommends pulls the DCS client library (python3-etcd3 for an etcd DCS), so no
  separate client package is installed.
- **`patroni.yml`** is hand-rendered (scope, name, restapi, the etcd/consul DCS
  block, bootstrap dcs with `synchronous_mode`, postgresql connect/data/auth,
  and `bin_dir` → `/usr/lib/postgresql/<major>/bin`). No YAML dependency is
  added. It is written to `/etc/patroni/<scope>.yml`.
- **Service reconciliation.** The Debian `patroni` unit's `ExecStart` hardcodes a
  config path that has varied across releases, so rather than depend on it a
  systemd drop-in (`/etc/systemd/system/patroni.service.d/10-tofu.conf`) resets
  `ExecStart` and points it at our config path, then `systemctl daemon-reload`.
- **Empty-datadir bootstrap.** Patroni — not systemd — must own the postgres
  process and must `initdb` into an **empty** data_dir, but the fresh install
  already started a populated `main` cluster there. So before starting patroni
  the packaged `postgresql@<major>-<cluster>` unit is stopped and disabled and
  the data_dir is emptied — all guarded on the **absence** of
  `<data_dir>/patroni.dynamic.json` (Patroni writes it once bootstrapped), so a
  re-apply against a live Patroni node is a no-op and never wipes a running
  cluster. Patroni then initdb+bootstraps into the empty dir via the DCS.
- Each node runs the same config and coordinates leadership through the DCS;
  Patroni self-elects (role is advisory). `postgres_cluster.dcs_reference` is
  required in this mode. **Do not** also manage the same cluster with
  `postgres_service` — Patroni owns the postgres process once running (the
  consumer already count-guards `postgres_service` off in patroni mode).

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
