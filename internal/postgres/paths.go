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

// ServiceUnit is the per-cluster systemd unit on Debian
// (postgresql@<major>-<cluster>).
func ServiceUnit(version, cluster string) string {
	return fmt.Sprintf("postgresql@%s-%s", version, cluster)
}
