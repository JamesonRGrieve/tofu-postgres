<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
# terraform-provider-postgres

A native OpenTofu/Terraform provider that manages a **PostgreSQL host** — its
installed package, config files, service, and HA topology — over an SSH/CLI
transport. PostgreSQL exposes no management REST API, so every resource drives
the host's CLI (apt, `pg_ctlcluster`, `systemctl`, `pg_basebackup`, `repmgr`,
`patroni`) over SSH with key/cert auth.

**Scope boundary — logical objects are composed, not owned here.** This provider
owns **install → config → service → HA**. Logical DB/role/grant/schema CRUD is
**out of scope**: compose those from
[`cyrilgdn/postgresql`](https://registry.terraform.io/providers/cyrilgdn/postgresql)
at the consumer layer (the `tofu/` repo), connecting over the wire the config
here opens. The two providers are complementary: this one makes PostgreSQL
*exist and listen*; `cyrilgdn/postgresql` fills it with databases and roles.

## Provider configuration

```hcl
provider "postgres" {
  ssh_host    = "10.0.0.20"      # host or host:port, no scheme
  ssh_user    = "root"           # default root
  ssh_key_pem = var.ssh_key_pem  # OpenBao-signed key from TF_VAR_*; or ssh_key_file / agent
  # ssh_port        = 22
  # timeout_seconds = 45         # raise for slow ops (a base-backup clone)
}
```

Credentials are injected at apply from the secret store (OpenBao → `TF_VAR_*` via
Semaphore) — never hard-coded, never persisted to state.

## Resources

| Resource | Manages | Import id |
|---|---|---|
| `postgres_package` | `apt install postgresql-<major>` + `apt-mark hold` | `<major>` (e.g. `16`) |
| `postgres_config` | postgresql.conf keys (conf.d drop-in) + tofu-owned pg_hba.conf block | `<version>` or `<version>/<cluster>` |
| `postgres_service` | the `postgresql@<major>-<cluster>` systemd unit | `<version>` or `<version>/<cluster>` |
| `postgres_cluster` | declarative HA topology (name, ha_mode, dcs_reference, synchronous) | `<name>` |
| `postgres_cluster_node` | per-node HA bring-up, dispatched on `ha_mode` | `<cluster>/<node_name>` |

Every stateful resource implements `ImportState`; importing an existing object
and immediately planning yields **zero diff**. Config resources are
**manage-declared-only**: an unset attribute is neither written nor reconciled,
so the provider never clobbers keys it does not manage.

See [`examples/`](examples/) for runnable HCL per resource, including all three
HA modes.

## HA modes

`postgres_cluster.ha_mode` selects the replication strategy; each
`postgres_cluster_node` dispatches its bring-up on the same mode:

- **`streaming`** — plain physical replication: primary sets `wal_level=replica`
  + senders/slots; standby `pg_basebackup`s and follows via `primary_conninfo`.
- **`repmgr`** — `repmgr.conf` + `repmgr primary register` / `standby clone` /
  `repmgrd` for automatic failover.
- **`patroni`** — `patroni.yml` + a DCS (etcd/consul) bootstrap; Patroni
  self-elects the leader.

**These operations are interruption-unsafe.** Base-backup clones, promotion, and
failover can corrupt or split-brain a cluster; validate on a lab twin and drive
live changes through the sanctioned pipeline. See [`DESIGN.md`](DESIGN.md).

## Development

Go 1.26.4, `terraform-plugin-framework` v1.19.0. The full local gate mirrors CI
and the pre-commit hook:

```sh
make check   # go mod tidy + gofmt + go vet + go test + go build
git config core.hooksPath .githooks   # wire the pre-commit gate
```

Install locally for a `dev_overrides` `.tfrc`:

```sh
make install   # builds terraform-provider-postgres into $DEV_BIN_DIR
```

## License

AGPL-3.0-or-later. Every source file carries an SPDX header.
