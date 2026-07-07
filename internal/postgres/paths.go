// SPDX-License-Identifier: AGPL-3.0-or-later

package postgres

import "fmt"

// Debian/Ubuntu lay clusters out per (major version, cluster name). These
// helpers centralize the layout so resources never hardcode the path shape.

// ConfigDir is the per-cluster config directory (holds postgresql.conf,
// pg_hba.conf, and the conf.d/ include dir).
func ConfigDir(version, cluster string) string {
	return fmt.Sprintf("/etc/postgresql/%s/%s", version, cluster)
}

// ConfDropInDir is the conf.d directory postgresql.conf includes by default on
// Debian. Drop-in files here layer over the base config without editing it.
func ConfDropInDir(version, cluster string) string {
	return ConfigDir(version, cluster) + "/conf.d"
}

// ConfDropInPath is the single tofu-owned drop-in file. The high numeric prefix
// makes it win over the packaged defaults (later files override earlier ones).
func ConfDropInPath(version, cluster string) string {
	return ConfDropInDir(version, cluster) + "/99-tofu.conf"
}

// HBAPath is the per-cluster pg_hba.conf.
func HBAPath(version, cluster string) string {
	return ConfigDir(version, cluster) + "/pg_hba.conf"
}

// DataDir is the per-cluster data directory.
func DataDir(version, cluster string) string {
	return fmt.Sprintf("/var/lib/postgresql/%s/%s", version, cluster)
}

// PackageName is the apt package for a given PostgreSQL major version.
func PackageName(version string) string {
	return "postgresql-" + version
}

// PGBinDir is the PostgreSQL binary directory for a major version on Debian —
// the value repmgr.conf's pg_bindir and Patroni's bin_dir must point at so both
// invoke the versioned server/utility binaries (pg_ctl, pg_basebackup, initdb)
// rather than a wrong-version copy on PATH.
func PGBinDir(version string) string {
	return fmt.Sprintf("/usr/lib/postgresql/%s/bin", version)
}

// AptGet is the apt-get invocation prefix every mutating apt call shares: the
// non-interactive frontend plus a bounded wait (DPkg::Lock::Timeout) for the
// dpkg lock, so a converge racing a boot-time apt-daily timer blocks (up to
// 300s) rather than exiting 100 instantly. Shared by postgres_package and the
// HA package installs (repmgr, Patroni).
const AptGet = "DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=300"

// ServiceUnit is the per-cluster systemd unit on Debian
// (postgresql@<major>-<cluster>).
func ServiceUnit(version, cluster string) string {
	return fmt.Sprintf("postgresql@%s-%s", version, cluster)
}
