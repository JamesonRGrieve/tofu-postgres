// SPDX-License-Identifier: AGPL-3.0-or-later

package postgres

import (
	"fmt"
	"strings"
)

// PatroniConfPath is the tofu-owned Patroni config for a cluster.
func PatroniConfPath(clusterName string) string {
	return "/etc/patroni/" + clusterName + ".yml"
}

// DCSType is the distributed-configuration-store backend Patroni bootstraps
// against.
type DCSType string

const (
	DCSEtcd3  DCSType = "etcd3"
	DCSConsul DCSType = "consul"
)

// PatroniParams renders a Patroni config. The DCS (etcd/consul) is where Patroni
// stores cluster leadership; DCSHosts is the dcs_reference endpoint list.
type PatroniParams struct {
	Scope          string // cluster name (Patroni "scope")
	NodeName       string // this node's Patroni name
	DCS            DCSType
	DCSHosts       string // e.g. "10.0.0.10:2379,10.0.0.11:2379"
	RestAPIListen  string // e.g. "0.0.0.0:8008"
	RestAPIConnect string // e.g. "10.0.0.20:8008"
	PGListen       string // e.g. "0.0.0.0:5432"
	PGConnect      string // e.g. "10.0.0.20:5432"
	DataDir        string
	BinDir         string // e.g. "/usr/lib/postgresql/16/bin"
	Synchronous    bool
	ReplUser       string
	ReplPassword   string
	SuperUser      string
	SuperPassword  string
	PgHbaCIDR      string // node subnet allowed for replication + access; empty → omit pg_hba
}

// RenderPatroniYAML hand-renders patroni.yml (no YAML dependency is added to
// go.mod). Indentation is two-space; the structure follows the canonical
// Patroni schema (scope/name/restapi/<dcs>/bootstrap/postgresql).
func RenderPatroniYAML(p PatroniParams) string {
	dcs := p.DCS
	if dcs == "" {
		dcs = DCSEtcd3
	}
	syncMode := "false"
	if p.Synchronous {
		syncMode = "true"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", strings.TrimPrefix(ManagedHeader, "# "))
	fmt.Fprintf(&b, "scope: %s\n", p.Scope)
	fmt.Fprintf(&b, "name: %s\n", p.NodeName)
	b.WriteString("restapi:\n")
	fmt.Fprintf(&b, "  listen: %s\n", p.RestAPIListen)
	fmt.Fprintf(&b, "  connect_address: %s\n", p.RestAPIConnect)
	// DCS backend block (etcd3 / consul).
	fmt.Fprintf(&b, "%s:\n", dcs)
	fmt.Fprintf(&b, "  hosts: %s\n", p.DCSHosts)
	b.WriteString("bootstrap:\n")
	b.WriteString("  dcs:\n")
	b.WriteString("    ttl: 30\n")
	b.WriteString("    loop_wait: 10\n")
	b.WriteString("    retry_timeout: 10\n")
	b.WriteString("    maximum_lag_on_failover: 1048576\n")
	fmt.Fprintf(&b, "    synchronous_mode: %s\n", syncMode)
	b.WriteString("postgresql:\n")
	fmt.Fprintf(&b, "  listen: %s\n", p.PGListen)
	fmt.Fprintf(&b, "  connect_address: %s\n", p.PGConnect)
	fmt.Fprintf(&b, "  data_dir: %s\n", p.DataDir)
	if strings.TrimSpace(p.BinDir) != "" {
		fmt.Fprintf(&b, "  bin_dir: %s\n", p.BinDir)
	}
	// Patroni OWNS pg_hba.conf — it re-initdb's the leader and rewrites the file
	// from postgresql.pg_hba, so a consumer's postgres_config pg_hba block is moot
	// in patroni mode. Without entries here, a replica's pg_basebackup from the
	// leader is rejected ("no pg_hba.conf entry for replication connection").
	// Render (over the node subnet): local peer for patroni's own superuser socket
	// access, a replication entry for the replication role, and general access.
	if strings.TrimSpace(p.PgHbaCIDR) != "" {
		b.WriteString("  pg_hba:\n")
		b.WriteString("  - local all all peer\n")
		fmt.Fprintf(&b, "  - host replication %s %s scram-sha-256\n", p.ReplUser, p.PgHbaCIDR)
		fmt.Fprintf(&b, "  - host all all %s scram-sha-256\n", p.PgHbaCIDR)
	}
	b.WriteString("  authentication:\n")
	b.WriteString("    replication:\n")
	fmt.Fprintf(&b, "      username: %s\n", p.ReplUser)
	fmt.Fprintf(&b, "      password: %s\n", p.ReplPassword)
	b.WriteString("    superuser:\n")
	fmt.Fprintf(&b, "      username: %s\n", p.SuperUser)
	fmt.Fprintf(&b, "      password: %s\n", p.SuperPassword)
	return b.String()
}

// patroniDCSPackage maps a DCS backend to the Debian python client package(s)
// Patroni needs to talk to it. Debian's `patroni` package Depends on an
// alternative (python3-etcd | python3-consul | python3-kazoo | …) that apt
// happily satisfies with WHICHEVER is already available (consul/kubernetes on a
// base image) — so an etcd DCS gets NO etcd client and Patroni reports "Can not
// find suitable configuration of distributed configuration store. Available
// implementations: consul, kubernetes" and refuses to start. Install the etcd
// clients explicitly: python3-etcd3 (v3 API — what the rendered `etcd3:` block
// uses) plus python3-etcd (v2, harmless fallback). There is no `patroni-etcd`
// package on Debian 13 — the backends are plain python modules.
func patroniDCSPackage(dcs DCSType) string {
	switch dcs {
	case DCSConsul:
		return "python3-consul"
	default: // etcd3 (default) / etcd
		return "python3-etcd3 python3-etcd"
	}
}

// PatroniInstallCommand installs Patroni + the client package for the chosen DCS
// backend via the lock-wait apt prefix.
func PatroniInstallCommand(dcs DCSType) Command {
	return Command{
		Label: "patroni install",
		Cmd:   AptGet + " update -qq && " + AptGet + " install -y -qq patroni " + patroniDCSPackage(dcs),
	}
}

// patroniDropInCommands makes the packaged patroni.service run OUR per-cluster
// config. The Debian `patroni` unit both hardcodes an ExecStart config path AND
// gates startup on `ConditionPathExists=/etc/patroni/config.yml` — so with our
// config at a different path systemd SILENTLY SKIPS the service ("was skipped
// because of an unmet condition check"), making `enable --now` report success
// while patroni never runs. The drop-in therefore (1) clears the condition list
// with an empty `ConditionPathExists=` in [Unit] and (2) resets ExecStart (an
// empty ExecStart= is required before a replacement) to point patroni at
// PatroniConfPath; then daemon-reload. Robust to whatever the packaged unit ships.
func patroniDropInCommands(confPath string) []Command {
	dropin := "[Unit]\nConditionPathExists=\n[Service]\nExecStart=\nExecStart=/usr/bin/patroni " + confPath + "\n"
	return []Command{
		{
			Label: "patroni dropin",
			Cmd:   "mkdir -p /etc/systemd/system/patroni.service.d && cat > /etc/systemd/system/patroni.service.d/10-tofu.conf",
			Stdin: []byte(dropin),
		},
		{Label: "patroni daemon-reload", Cmd: "systemctl daemon-reload"},
	}
}

// patroniTakeoverCommand hands the packaged cluster's data_dir to Patroni:
// Patroni — not systemd — must own the postgres process and it must initdb into
// an EMPTY data_dir, but the fresh install already started a populated `main`
// cluster there. So stop and disable the Debian postgresql@<major>-<cluster>
// unit and empty the data_dir. Guarded on the absence of patroni.dynamic.json
// (Patroni writes it once it has bootstrapped) so this runs only before Patroni
// has taken over — a re-apply against a live Patroni node is a no-op and never
// wipes a running cluster.
func patroniTakeoverCommand(version, cluster, dataDir string) Command {
	unit := ServiceUnit(version, cluster)
	body := fmt.Sprintf("pg_ctlcluster %s %s stop || true; systemctl disable %s 2>/dev/null || true; find %s -mindepth 1 -delete",
		version, cluster, unit, shQuote(dataDir))
	return Command{
		Label: "patroni takeover",
		Cmd:   fmt.Sprintf("if [ ! -f %s/patroni.dynamic.json ]; then %s; fi", shQuote(dataDir), body),
	}
}

// PatroniNodeParams configures a Patroni node's bring-up.
type PatroniNodeParams struct {
	Version     string
	Cluster     string // Debian cluster name (packaged unit + data_dir)
	ClusterName string // Patroni scope (config filename)
	DataDir     string
	YAML        string
	DCS         DCSType // selects the patroni-<dcs> client package to install
}

// PatroniCommands renders the full Patroni node bring-up: install Patroni, write
// patroni.yml, install the systemd drop-in pointing the unit at it, hand the
// data_dir to Patroni (stop/disable the packaged unit + empty the dir, guarded),
// then enable+start patroni. Patroni owns the postgres process once running — do
// NOT also manage the same cluster via postgres_service (see DESIGN.md).
func PatroniCommands(p PatroniNodeParams) []Command {
	confPath := PatroniConfPath(p.ClusterName)
	cmds := []Command{
		PatroniInstallCommand(p.DCS),
		{Label: "patroni conf", Cmd: fmt.Sprintf("mkdir -p /etc/patroni && cat > %s", shQuote(confPath)), Stdin: []byte(p.YAML)},
	}
	cmds = append(cmds, patroniDropInCommands(confPath)...)
	cmds = append(cmds,
		patroniTakeoverCommand(p.Version, p.Cluster, p.DataDir),
		Command{Label: "patroni service", Cmd: "systemctl enable --now patroni"},
		// Reload so an ALREADY-running node (re-apply / config change, e.g. an
		// updated pg_hba) re-reads patroni.yml without a restart. On first bootstrap
		// this is a harmless no-op — patroni just started with the same config.
		Command{Label: "patroni reload", Cmd: fmt.Sprintf(
			"patronictl -c %s reload %s --force 2>/dev/null || true", shQuote(confPath), shQuote(p.ClusterName))},
	)
	return cmds
}
