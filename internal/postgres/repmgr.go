// SPDX-License-Identifier: AGPL-3.0-or-later

package postgres

import (
	"fmt"
	"strings"
)

// RepmgrConfPath is the tofu-owned repmgr config for a cluster.
func RepmgrConfPath(version, cluster string) string {
	return ConfigDir(version, cluster) + "/repmgr.conf"
}

// RepmgrConfParams renders repmgr.conf. conninfo is how OTHER nodes reach THIS
// node (repmgr requires a routable, non-localhost conninfo).
type RepmgrConfParams struct {
	NodeID          int
	NodeName        string
	Conninfo        string // conninfo for this node (BuildPrimaryConninfo)
	DataDir         string
	ReplicationUser string // repmgr's replication/superuser role (default "repmgr")
	Location        string // optional failover location tag
}

// RenderRepmgrConf renders a repmgr.conf. Keys are emitted in a stable order so
// the file is byte-identical across applies.
func RenderRepmgrConf(p RepmgrConfParams) string {
	user := p.ReplicationUser
	if user == "" {
		user = "repmgr"
	}
	var b strings.Builder
	b.WriteString(ManagedHeader + "\n")
	fmt.Fprintf(&b, "node_id=%d\n", p.NodeID)
	fmt.Fprintf(&b, "node_name='%s'\n", p.NodeName)
	fmt.Fprintf(&b, "conninfo='%s'\n", p.Conninfo)
	fmt.Fprintf(&b, "data_directory='%s'\n", p.DataDir)
	fmt.Fprintf(&b, "replication_user='%s'\n", user)
	if strings.TrimSpace(p.Location) != "" {
		fmt.Fprintf(&b, "location='%s'\n", p.Location)
	}
	b.WriteString("failover='automatic'\n")
	b.WriteString("promote_command='repmgr standby promote -f /etc/repmgr.conf --log-to-file'\n")
	b.WriteString("follow_command='repmgr standby follow -f /etc/repmgr.conf --log-to-file --upstream-node-id=%n'\n")
	return b.String()
}

// RepmgrPrimaryCommands renders: write repmgr.conf, register this node as the
// repmgr primary, and enable repmgrd.
func RepmgrPrimaryCommands(confPath, conf string) []Command {
	return []Command{
		{Label: "repmgr primary conf", Cmd: fmt.Sprintf("cat > %s && chown postgres:postgres %s", shQuote(confPath), shQuote(confPath)), Stdin: []byte(conf)},
		{Label: "repmgr primary register", Cmd: fmt.Sprintf("su postgres -c %s", shQuote(fmt.Sprintf("repmgr -f %s primary register --force", confPath)))},
		{Label: "repmgr primary daemon", Cmd: "systemctl enable --now repmgrd 2>/dev/null || true"},
	}
}

// RepmgrStandbyParams configures cloning a standby from a repmgr primary.
type RepmgrStandbyParams struct {
	Version     string
	Cluster     string
	ConfPath    string
	Conf        string
	PrimaryHost string
	PrimaryPort int
	ReplUser    string // default "repmgr"
	ReplDB      string // default "repmgr"
}

// RepmgrStandbyCommands renders: write repmgr.conf, clone from the primary
// (guarded on an empty datadir), start, then register as a standby, and enable
// repmgrd. Cloning is interruption-unsafe — see DESIGN.md.
func RepmgrStandbyCommands(p RepmgrStandbyParams) []Command {
	user := p.ReplUser
	if user == "" {
		user = "repmgr"
	}
	db := p.ReplDB
	if db == "" {
		db = "repmgr"
	}
	port := p.PrimaryPort
	if port == 0 {
		port = DefaultPGPort
	}
	dataDir := DataDir(p.Version, p.Cluster)
	clone := fmt.Sprintf("repmgr -h %s -p %d -U %s -d %s -f %s standby clone --force",
		p.PrimaryHost, port, user, db, p.ConfPath)
	guarded := fmt.Sprintf("[ -f %s/PG_VERSION ] || su postgres -c %s", shQuote(dataDir), shQuote(clone))
	return []Command{
		{Label: "repmgr standby conf", Cmd: fmt.Sprintf("cat > %s && chown postgres:postgres %s", shQuote(p.ConfPath), shQuote(p.ConfPath)), Stdin: []byte(p.Conf)},
		{Label: "repmgr standby stop", Cmd: fmt.Sprintf("pg_ctlcluster %s %s stop || true", p.Version, p.Cluster)},
		{Label: "repmgr standby clone", Cmd: guarded},
		{Label: "repmgr standby start", Cmd: fmt.Sprintf("pg_ctlcluster %s %s start", p.Version, p.Cluster)},
		{Label: "repmgr standby register", Cmd: fmt.Sprintf("su postgres -c %s", shQuote(fmt.Sprintf("repmgr -f %s standby register --force", p.ConfPath)))},
		{Label: "repmgr standby daemon", Cmd: "systemctl enable --now repmgrd 2>/dev/null || true"},
	}
}
