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

// RepmgrPackage is the apt package that supplies the repmgr CLI + repmgrd for a
// PostgreSQL major version (Debian names it postgresql-<major>-repmgr).
func RepmgrPackage(version string) string {
	return "postgresql-" + version + "-repmgr"
}

// RepmgrInstallCommand installs the versioned repmgr package via the lock-wait
// apt prefix. Every repmgr node needs it before any repmgr/repmgrd invocation,
// so it is prepended to the primary/standby/witness command lists.
func RepmgrInstallCommand(version string) Command {
	pkg := RepmgrPackage(version)
	return Command{
		Label: "repmgr install " + pkg,
		Cmd:   AptGet + " update -qq && " + AptGet + " install -y -qq " + pkg,
	}
}

// RepmgrConfParams renders repmgr.conf. Conninfo is how OTHER nodes reach THIS
// node (repmgr requires a routable, non-localhost conninfo). ConfPath is the
// path the file lands at — the rendered promote/follow commands reference it so
// repmgrd and a manual `repmgr` invocation read the same config. PGBinDir points
// repmgr at the versioned server binaries.
type RepmgrConfParams struct {
	NodeID          int
	NodeName        string
	Conninfo        string // conninfo for this node (BuildPrimaryConninfo)
	DataDir         string
	ReplicationUser string // repmgr's replication/superuser role (default "repmgr")
	ConfPath        string // where this file is written (default /etc/repmgr.conf)
	PGBinDir        string // /usr/lib/postgresql/<major>/bin
	Location        string // optional failover location tag
}

// RenderRepmgrConf renders a repmgr.conf. Keys are emitted in a stable order so
// the file is byte-identical across applies.
func RenderRepmgrConf(p RepmgrConfParams) string {
	user := p.ReplicationUser
	if user == "" {
		user = "repmgr"
	}
	confPath := p.ConfPath
	if confPath == "" {
		confPath = "/etc/repmgr.conf"
	}
	var b strings.Builder
	b.WriteString(ManagedHeader + "\n")
	fmt.Fprintf(&b, "node_id=%d\n", p.NodeID)
	fmt.Fprintf(&b, "node_name='%s'\n", p.NodeName)
	fmt.Fprintf(&b, "conninfo='%s'\n", p.Conninfo)
	fmt.Fprintf(&b, "data_directory='%s'\n", p.DataDir)
	if strings.TrimSpace(p.PGBinDir) != "" {
		fmt.Fprintf(&b, "pg_bindir='%s'\n", p.PGBinDir)
	}
	fmt.Fprintf(&b, "replication_user='%s'\n", user)
	if strings.TrimSpace(p.Location) != "" {
		fmt.Fprintf(&b, "location='%s'\n", p.Location)
	}
	b.WriteString("failover='automatic'\n")
	// promote/follow must reference THIS conf (not a hardcoded /etc/repmgr.conf):
	// repmgrd, launched from /etc/default/repmgrd with the same REPMGRD_CONF, runs
	// these, and they in turn re-read the config to find their node. A path
	// mismatch made repmgrd promote/follow against a config that does not exist.
	fmt.Fprintf(&b, "promote_command='repmgr standby promote -f %s --log-to-file'\n", confPath)
	fmt.Fprintf(&b, "follow_command='repmgr standby follow -f %s --log-to-file --upstream-node-id=%%n'\n", confPath)
	return b.String()
}

// writeRepmgrConfCommand writes repmgr.conf owned by postgres.
func writeRepmgrConfCommand(role, confPath, conf string) Command {
	return Command{
		Label: "repmgr " + role + " conf",
		Cmd:   fmt.Sprintf("cat > %s && chown postgres:postgres %s", shQuote(confPath), shQuote(confPath)),
		Stdin: []byte(conf),
	}
}

// repmgrDatabaseSQL renders an idempotent CREATE DATABASE for the repmgr
// metadata database. CREATE DATABASE cannot run inside a DO/transaction block,
// so it is done via psql's \gexec: the SELECT yields the DDL string only when
// the database is absent, and \gexec executes each returned row as SQL. Fed to
// psql on stdin (-f -).
func repmgrDatabaseSQL(db, owner string) string {
	create := "CREATE DATABASE " + QuoteIdent(db) + " OWNER " + QuoteIdent(owner)
	return "SELECT " + QuoteLiteral(create) +
		" WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = " + QuoteLiteral(db) + ")\n\\gexec\n"
}

// repmgrRoleDBCommands creates the repmgr role (LOGIN SUPERUSER REPLICATION,
// create-or-sync) and its metadata database on a node's LOCAL postgres over the
// unix socket (peer auth, no superuser password; secrets on stdin). The primary
// and witness both need this before registering — the primary owns the cluster
// metadata, the witness stores its own copy. role/password default to repmgr.
func repmgrRoleDBCommands(role, version, cluster, user, password, db string) []Command {
	return []Command{
		{
			Label: "repmgr " + role + " role",
			Cmd:   fmt.Sprintf("su postgres -c %s", shQuote(primaryPsql(version, cluster, "-f -"))),
			Stdin: []byte(replicationRoleSQL(user, password, true)),
		},
		{
			Label: "repmgr " + role + " database",
			Cmd:   fmt.Sprintf("su postgres -c %s", shQuote(primaryPsql(version, cluster, "-f -"))),
			Stdin: []byte(repmgrDatabaseSQL(db, user)),
		},
	}
}

// repmgrdEnableCommands configures the Debian repmgrd unit to read this
// cluster's repmgr.conf and enables it. The packaged /lib/systemd/system/repmgrd
// unit sources /etc/default/repmgrd for REPMGRD_ENABLED (a guard the unit
// refuses to start without) and REPMGRD_CONF (which conf to load); without this
// file repmgrd either refuses to start or loads a non-existent /etc/repmgr.conf.
func repmgrdEnableCommands(confPath string) []Command {
	defaults := ManagedHeader + "\nREPMGRD_ENABLED=yes\nREPMGRD_CONF=\"" + confPath + "\"\n"
	return []Command{
		{Label: "repmgrd defaults", Cmd: "cat > /etc/default/repmgrd", Stdin: []byte(defaults)},
		{Label: "repmgr daemon", Cmd: "systemctl enable --now repmgrd"},
	}
}

// RepmgrPrimaryParams configures bring-up of the repmgr primary.
type RepmgrPrimaryParams struct {
	Version      string
	Cluster      string
	ConfPath     string
	Conf         string
	SelfHost     string // this node's own routable address (repmgr.conf conninfo host)
	SelfPort     int
	ReplUser     string // repmgr superuser role (default "repmgr")
	ReplPassword string
	ReplDB       string // repmgr metadata db (default "repmgr")
}

// RepmgrPrimaryCommands renders: write repmgr.conf, create the repmgr
// role+database on the local cluster, drop a ~postgres/.pgpass, register this
// node as the repmgr primary, then enable repmgrd (pointed at this conf).
func RepmgrPrimaryCommands(p RepmgrPrimaryParams) []Command {
	user := orDefault(p.ReplUser, "repmgr")
	db := orDefault(p.ReplDB, "repmgr")
	port := orInt(p.SelfPort, DefaultPGPort)
	cmds := []Command{writeRepmgrConfCommand("primary", p.ConfPath, p.Conf)}
	cmds = append(cmds, repmgrRoleDBCommands("primary", p.Version, p.Cluster, user, p.ReplPassword, db)...)
	// `repmgr primary register` connects to THIS node through the repmgr.conf
	// conninfo — which is a routable TCP host (repmgr requires a non-localhost
	// conninfo), so it hits scram auth and needs the password from ~postgres/.pgpass
	// (peer auth is only for the local socket, not the self-TCP connection).
	if p.SelfHost != "" && p.ReplPassword != "" {
		cmds = append(cmds, pgpassCommand("repmgr primary pgpass",
			fmt.Sprintf("%s:%d:replication:%s:%s", p.SelfHost, port, user, p.ReplPassword),
			fmt.Sprintf("%s:%d:%s:%s:%s", p.SelfHost, port, db, user, p.ReplPassword)))
	}
	cmds = append(cmds, Command{
		Label: "repmgr primary register",
		Cmd:   fmt.Sprintf("su postgres -c %s", shQuote(fmt.Sprintf("repmgr -f %s primary register --force", p.ConfPath))),
	})
	return append(cmds, repmgrdEnableCommands(p.ConfPath)...)
}

// RepmgrStandbyParams configures cloning/registering a standby (and, reused, a
// witness) from a repmgr primary.
type RepmgrStandbyParams struct {
	Version      string
	Cluster      string
	ConfPath     string
	Conf         string
	PrimaryHost  string
	PrimaryPort  int
	ReplUser     string // default "repmgr"
	ReplPassword string
	ReplDB       string // default "repmgr"
}

// RepmgrStandbyCommands renders: write repmgr.conf, stop the local cluster, drop
// a ~postgres/.pgpass with the repmgr credential, empty-then-clone from the
// primary (guarded), start, register as a standby, then enable repmgrd. Cloning
// is interruption-unsafe — see DESIGN.md.
func RepmgrStandbyCommands(p RepmgrStandbyParams) []Command {
	user := orDefault(p.ReplUser, "repmgr")
	db := orDefault(p.ReplDB, "repmgr")
	port := orInt(p.PrimaryPort, DefaultPGPort)
	dataDir := DataDir(p.Version, p.Cluster)
	clone := fmt.Sprintf("repmgr -h %s -p %d -U %s -d %s -f %s standby clone --force",
		p.PrimaryHost, port, user, db, p.ConfPath)
	// The fresh package install already populated the datadir, and repmgr's clone
	// (a pg_basebackup wrapper) requires an EMPTY target — so on a node that is not
	// already a standby OF THIS PRIMARY, wipe the local cluster and clone. The
	// guard keys on the primary_conninfo the clone writes into postgresql.auto.conf
	// (true only after a real clone). It deliberately does NOT key on PG_VERSION
	// (every initialized datadir has it — that is what made the old guard skip the
	// clone forever on a fresh node); so this converges a fresh node and self-heals
	// a botched standby, while a real re-apply is a no-op.
	guarded := fmt.Sprintf(
		"if ! grep -qs %s %s/postgresql.auto.conf; then find %s -mindepth 1 -delete && su postgres -c %s; fi",
		shQuote("host="+p.PrimaryHost), shQuote(dataDir), shQuote(dataDir), shQuote(clone))
	cmds := []Command{
		writeRepmgrConfCommand("standby", p.ConfPath, p.Conf),
		{Label: "repmgr standby stop", Cmd: fmt.Sprintf("pg_ctlcluster %s %s stop || true", p.Version, p.Cluster)},
	}
	if p.PrimaryHost != "" && p.ReplPassword != "" {
		cmds = append(cmds, pgpassCommand("repmgr standby pgpass",
			fmt.Sprintf("%s:%d:replication:%s:%s", p.PrimaryHost, port, user, p.ReplPassword),
			fmt.Sprintf("%s:%d:%s:%s:%s", p.PrimaryHost, port, db, user, p.ReplPassword)))
	}
	cmds = append(cmds,
		Command{Label: "repmgr standby clone", Cmd: guarded},
		Command{Label: "repmgr standby start", Cmd: fmt.Sprintf("pg_ctlcluster %s %s start", p.Version, p.Cluster)},
		Command{Label: "repmgr standby register", Cmd: fmt.Sprintf("su postgres -c %s", shQuote(fmt.Sprintf("repmgr -f %s standby register --force", p.ConfPath)))},
	)
	return append(cmds, repmgrdEnableCommands(p.ConfPath)...)
}

// RepmgrWitnessCommands renders witness bring-up: write repmgr.conf, create the
// repmgr role+database on the witness's own standalone postgres, drop a .pgpass
// to reach the primary, register as a witness, then enable repmgrd. A witness
// holds no data replica but participates in repmgrd's failover quorum.
func RepmgrWitnessCommands(p RepmgrStandbyParams) []Command {
	user := orDefault(p.ReplUser, "repmgr")
	db := orDefault(p.ReplDB, "repmgr")
	port := orInt(p.PrimaryPort, DefaultPGPort)
	register := fmt.Sprintf("repmgr -h %s -p %d -U %s -d %s -f %s witness register --force",
		p.PrimaryHost, port, user, db, p.ConfPath)
	cmds := []Command{writeRepmgrConfCommand("witness", p.ConfPath, p.Conf)}
	cmds = append(cmds, repmgrRoleDBCommands("witness", p.Version, p.Cluster, user, p.ReplPassword, db)...)
	if p.PrimaryHost != "" && p.ReplPassword != "" {
		cmds = append(cmds, pgpassCommand("repmgr witness pgpass",
			fmt.Sprintf("%s:%d:replication:%s:%s", p.PrimaryHost, port, user, p.ReplPassword),
			fmt.Sprintf("%s:%d:%s:%s:%s", p.PrimaryHost, port, db, user, p.ReplPassword)))
	}
	cmds = append(cmds, Command{Label: "repmgr witness register", Cmd: fmt.Sprintf("su postgres -c %s", shQuote(register))})
	return append(cmds, repmgrdEnableCommands(p.ConfPath)...)
}
