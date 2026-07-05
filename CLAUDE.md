# tofu-postgres — Agent Operating Guide

> **⛔ NO DIRECT APPLIES TO ANY DEVICE — EVER.**
>
> Direct changes to **any** device — router, firewall, switch, access point, hypervisor, mail gateway, or any other appliance — are **NEVER** permitted, by anyone, for any reason. This bans hand-run `tofu apply`, hand-run `ansible-playbook`, SSH/serial/CLI config writes, REST/API mutations, and web-GUI/console edits.
>
> **Every change MUST flow through the sanctioned pipeline:** declare intent in **prod-netbox** (the single source of truth), then realize it **only** through **prod-semaphore** (the sanctioned runner). A change that did not go **prod-netbox → prod-semaphore** must never reach a device.
>
> **Sole exception:** a specific direct action is permitted *only* when the operator authorizes that exact action in advance by answering an explicit, **alarm-flavored `AskUserQuestion`** — one that names the device, the precise action, and the risk — **in the affirmative**. No standing grants, no inferred permission, no carrying one approval to another action or device. Absent that in-the-moment "yes," the answer is no.
>
> **Never offload the work onto the operator.** When you are blocked, ask for the break-glass authorization that lets *you* do the job — never ask the operator to run a command, SSH in, or make the change on your behalf. The operator grants permission; they do not perform your labour.

Native OpenTofu/Terraform provider for **PostgreSQL host management** over an
SSH/CLI transport. Sibling of `../tofu-opnsense`, `../tofu-proxmox`,
`../tofu-openwrt-ubus` — same toolchain, same house standards. The workspace-root
`../CLAUDE.md` applies; this adds specifics.

## What this is / isn't

- **Is:** a provider for a PostgreSQL host's **installed state** (apt package),
  **config files** (postgresql.conf via a conf.d drop-in + pg_hba.conf),
  **service** (per-cluster systemd unit), and **HA topology** (streaming,
  repmgr, and Patroni — mode-selectable). PostgreSQL exposes no management REST
  API, so everything is driven over SSH/CLI, key/cert auth only.
- **Isn't:** a logical-object provider. Databases, roles, grants, extensions,
  and schema live at the **consumer layer**, composed from
  **`cyrilgdn/postgresql`** over the wire this provider's config opens. Do not
  add DB/role/grant CRUD here.

## Resources (all typed, SSH-driven, import-to-0-diff)

- `postgres_package` — `apt install postgresql-<major>` + `apt-mark hold`.
- `postgres_config` — postgresql.conf keys (conf.d drop-in) + the tofu-owned
  pg_hba.conf block. Manage-declared-only; update → reload, or restart when a
  postmaster-context key (shared_buffers/max_connections/listen_addresses) is
  declared.
- `postgres_service` — the `postgresql@<major>-<cluster>` systemd unit
  (enabled/state + `restart_triggers`).
- `postgres_cluster` — declarative HA topology record (name, ha_mode,
  dcs_reference, synchronous); validated, consumed by nodes.
- `postgres_cluster_node` — per-node HA bring-up, dispatched on `ha_mode`.

## Design tenets

- **Transport/framework split.** `internal/postgres/` is framework-free: the SSH
  client plus **pure** helpers (conf/pg_hba rendering, conninfo, patroni.yml /
  repmgr.conf rendering, dpkg/pg_lsclusters parsing, mode dispatch), each
  table-tested. `internal/provider/` wires those to the plugin framework.
- **Injected exec.** Device-apply logic is a list of `postgres.Command`s built by
  pure functions and executed through a `postgres.RunFunc` seam. Tests inject a
  recording fake, so CRUD dispatch is verified hermetically — **the provider
  never applies to a real host in the test suite.**
- **No-op deletes.** Uninstalling PG / stopping the service / tearing a node out
  of a live cluster on `destroy` would cause an outage — deletes stop managing,
  they don't destroy (mirrors `opnsense_system_config` / `proxmox_host_config`).
- **Secrets never in state.** Passwords/keys come from the provider block / env
  (OpenBao → `TF_VAR_*` via Semaphore), injected at apply.

## Toolchain

- Go 1.26.4 (`/home/jameson/.local/go`), `terraform-plugin-framework` v1.19.0.
  Do **not** add or bump deps — they mirror `../tofu-opnsense`.
- Provider address: `registry.terraform.io/jamesonrgrieve/postgres`; TypeName
  `postgres` (resources `postgres_*`).
- General Go / Terraform-provider standards are canonical at
  `/home/jameson/source/ai-prompts/go.md` and `.../tofu.md`. Read them first;
  this file only holds repo-specific facts.
- `make check` (tidy + fmt + vet + test + build) is the gate; `.githooks/pre-commit`
  re-runs it. Enable with `git config core.hooksPath .githooks`. Never `--no-verify`.

## Hard rules

- **HA operations are interruption-unsafe.** A base-backup clone, a promotion, a
  failover, or a config restart can corrupt or split-brain a cluster. Never run
  them synchronously against prod; validate on a lab twin in byte-for-byte
  identical form and drive live changes via Semaphore. See `DESIGN.md`.
- **No secrets in the repo, ever.** No credentials in code, examples, or state.
- **Fix spurious diffs in the subset/read logic**, never by widening what gets
  stored — manage-declared-only is the contract.
