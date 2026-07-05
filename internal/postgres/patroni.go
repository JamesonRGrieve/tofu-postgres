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
	b.WriteString("  authentication:\n")
	b.WriteString("    replication:\n")
	fmt.Fprintf(&b, "      username: %s\n", p.ReplUser)
	fmt.Fprintf(&b, "      password: %s\n", p.ReplPassword)
	b.WriteString("    superuser:\n")
	fmt.Fprintf(&b, "      username: %s\n", p.SuperUser)
	fmt.Fprintf(&b, "      password: %s\n", p.SuperPassword)
	return b.String()
}

// PatroniCommands renders: write patroni.yml, then enable+start the patroni
// service. Patroni owns the postgres process once running — do NOT also manage
// the same cluster via postgres_service (see DESIGN.md).
func PatroniCommands(clusterName, yaml string) []Command {
	confPath := PatroniConfPath(clusterName)
	return []Command{
		{Label: "patroni conf", Cmd: fmt.Sprintf("mkdir -p /etc/patroni && cat > %s", shQuote(confPath)), Stdin: []byte(yaml)},
		{Label: "patroni service", Cmd: "systemctl enable --now patroni"},
	}
}
