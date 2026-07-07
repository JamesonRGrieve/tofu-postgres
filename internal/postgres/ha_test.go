// SPDX-License-Identifier: AGPL-3.0-or-later

package postgres

import (
	"strings"
	"testing"
)

func labels(cmds []Command) []string {
	out := make([]string, len(cmds))
	for i, c := range cmds {
		out[i] = c.Label
	}
	return out
}

func TestStreamingPrimaryCommands(t *testing.T) {
	cmds := StreamingPrimaryCommands(StreamingPrimaryParams{
		Version: "16", Cluster: "main", MaxWalSenders: 5, Slots: []string{"standby1"},
	})
	// config write carries the rendered HA drop-in on stdin.
	conf := string(cmds[0].Stdin)
	// wal_keep_size must always be emitted (default floor) so pg_basebackup can't
	// hit "requested WAL segment … already removed".
	for _, want := range []string{"wal_level = replica", "max_wal_senders = 5", "hot_standby = on", "wal_keep_size = '512MB'"} {
		if !strings.Contains(conf, want) {
			t.Fatalf("HA drop-in missing %q:\n%s", want, conf)
		}
	}
	joined := strings.Join(labels(cmds), ",")
	if !strings.Contains(joined, "reload") || !strings.Contains(joined, "create slot standby1") {
		t.Fatalf("streaming primary labels = %s", joined)
	}
	// With no ReplicationUser, no role command is emitted.
	if strings.Contains(joined, "replication role") {
		t.Fatalf("unexpected replication role command without a user: %s", joined)
	}
}

func TestStreamingPrimaryCreatesReplicationRole(t *testing.T) {
	cmds := StreamingPrimaryCommands(StreamingPrimaryParams{
		Version: "16", Cluster: "main", ReplicationUser: "replicator", ReplicationPassword: "p'w",
	})
	var roleCmd Command
	for _, c := range cmds {
		if strings.Contains(c.Label, "replication role") {
			roleCmd = c
		}
	}
	if roleCmd.Label == "" {
		t.Fatal("no replication role command emitted for a primary with a ReplicationUser")
	}
	sql := string(roleCmd.Stdin)
	for _, want := range []string{"CREATE ROLE \"replicator\"", "LOGIN REPLICATION", "ALTER ROLE", "PASSWORD 'p''w'"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("replication role SQL missing %q:\n%s", want, sql)
		}
	}
	// The password must travel on stdin, never in the command's argv.
	if strings.Contains(roleCmd.Cmd, "p'w") || strings.Contains(roleCmd.Cmd, "PASSWORD") {
		t.Fatalf("password leaked into argv: %s", roleCmd.Cmd)
	}
}

func TestStreamingStandbyCommands(t *testing.T) {
	conninfo := BuildPrimaryConninfo(Conninfo{Host: "10.0.0.1", User: "replicator", Password: "pw", ApplicationName: "n2"})
	cmds := StreamingStandbyCommands(StreamingStandbyParams{
		Version: "16", Cluster: "main", Conninfo: conninfo, Slot: "standby1",
		PrimaryHost: "10.0.0.1", PrimaryPort: 5432, ReplicationUser: "replicator", ReplicationPassword: "pw",
	})
	var clone, pgpass Command
	for _, c := range cmds {
		if strings.Contains(c.Label, "clone") {
			clone = c
		}
		if strings.Contains(c.Label, "pgpass") {
			pgpass = c
		}
	}
	if clone.Cmd == "" {
		t.Fatal("no clone command emitted")
	}
	// The clone must guard on primary_conninfo referencing the primary host
	// (idempotent + self-healing), empty the datadir (a fresh install populated
	// it), and run pg_basebackup -R --slot; it must NOT use the old PG_VERSION
	// guard that skipped the clone after a fresh install.
	for _, want := range []string{"host=10.0.0.1", "postgresql.auto.conf", "-mindepth 1 -delete", "pg_basebackup", "-R", "--slot=", "standby1"} {
		if !strings.Contains(clone.Cmd, want) {
			t.Fatalf("clone command missing %q:\n%s", want, clone.Cmd)
		}
	}
	if strings.Contains(clone.Cmd, "PG_VERSION") {
		t.Fatalf("clone still uses the broken PG_VERSION guard:\n%s", clone.Cmd)
	}
	// The .pgpass carries the password on stdin, never argv.
	if pgpass.Label == "" || !strings.Contains(string(pgpass.Stdin), "10.0.0.1:5432:replication:replicator:pw") {
		t.Fatalf("pgpass not written with the replication credential on stdin: %q / %q", pgpass.Cmd, string(pgpass.Stdin))
	}
	if strings.Contains(pgpass.Cmd, "pw") {
		t.Fatalf("password leaked into pgpass argv: %s", pgpass.Cmd)
	}
}

func TestRenderRepmgrConf(t *testing.T) {
	confPath := "/etc/postgresql/17/main/repmgr.conf"
	conf := RenderRepmgrConf(RepmgrConfParams{
		NodeID: 1, NodeName: "node1",
		Conninfo: "host=10.0.0.1 port=5432 user=repmgr dbname=repmgr",
		DataDir:  "/var/lib/postgresql/17/main",
		ConfPath: confPath, PGBinDir: "/usr/lib/postgresql/17/bin",
	})
	for _, want := range []string{
		"node_id=1",
		"node_name='node1'",
		"conninfo='host=10.0.0.1 port=5432 user=repmgr dbname=repmgr'",
		"data_directory='/var/lib/postgresql/17/main'",
		"pg_bindir='/usr/lib/postgresql/17/bin'",
		"replication_user='repmgr'",
		"failover='automatic'",
		// promote/follow must reference the REAL conf path (not a hardcoded
		// /etc/repmgr.conf) so repmgrd re-reads the same file.
		"promote_command='repmgr standby promote -f " + confPath + " --log-to-file'",
		"follow_command='repmgr standby follow -f " + confPath + " --log-to-file --upstream-node-id=%n'",
	} {
		if !strings.Contains(conf, want) {
			t.Fatalf("repmgr.conf missing %q:\n%s", want, conf)
		}
	}
	if strings.Contains(conf, "-f /etc/repmgr.conf") {
		t.Fatalf("repmgr.conf still references the hardcoded /etc/repmgr.conf:\n%s", conf)
	}
}

// find returns the first command whose label contains sub, or a zero Command.
func find(cmds []Command, sub string) Command {
	for _, c := range cmds {
		if strings.Contains(c.Label, sub) {
			return c
		}
	}
	return Command{}
}

func TestRepmgrInstallCommand(t *testing.T) {
	c := RepmgrInstallCommand("17")
	for _, want := range []string{"postgresql-17-repmgr", "DPkg::Lock::Timeout=300", "install -y"} {
		if !strings.Contains(c.Cmd, want) {
			t.Fatalf("repmgr install missing %q: %s", want, c.Cmd)
		}
	}
}

func TestRepmgrPrimaryCommands(t *testing.T) {
	cmds := RepmgrPrimaryCommands(RepmgrPrimaryParams{
		Version: "17", Cluster: "main", ConfPath: "/etc/postgresql/17/main/repmgr.conf",
		Conf: "conf-body", SelfHost: "10.0.0.21", SelfPort: 5432, ReplUser: "repmgr", ReplPassword: "s'ecret",
	})
	joined := strings.Join(labels(cmds), ",")
	for _, want := range []string{"repmgr primary conf", "repmgr primary role", "repmgr primary database", "repmgr primary pgpass", "repmgr primary register", "repmgrd defaults", "repmgr daemon"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("primary labels missing %q: %s", want, joined)
		}
	}
	// `repmgr primary register` self-connects over TCP, so the primary needs a
	// .pgpass with its own credential (on stdin, never argv).
	pgpass := find(cmds, "primary pgpass")
	if !strings.Contains(string(pgpass.Stdin), "*:*:*:repmgr:s'ecret") {
		t.Fatalf("primary pgpass missing wildcard repmgr entry:\n%s", string(pgpass.Stdin))
	}
	if strings.Contains(pgpass.Cmd, "ecret") {
		t.Fatalf("password leaked into pgpass argv: %s", pgpass.Cmd)
	}
	// The repmgr role is created SUPERUSER, with the password on stdin (never argv).
	role := find(cmds, "primary role")
	sql := string(role.Stdin)
	for _, want := range []string{"CREATE ROLE \"repmgr\"", "LOGIN SUPERUSER REPLICATION", "PASSWORD 's''ecret'"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("role SQL missing %q:\n%s", want, sql)
		}
	}
	if strings.Contains(role.Cmd, "ecret") || strings.Contains(role.Cmd, "PASSWORD") {
		t.Fatalf("password leaked into role argv: %s", role.Cmd)
	}
	// The database is created idempotently via \gexec guarded on pg_database.
	db := find(cmds, "primary database")
	dbSQL := string(db.Stdin)
	for _, want := range []string{"CREATE DATABASE \"repmgr\" OWNER \"repmgr\"", "NOT EXISTS (SELECT FROM pg_database WHERE datname = 'repmgr')", "\\gexec"} {
		if !strings.Contains(dbSQL, want) {
			t.Fatalf("database SQL missing %q:\n%s", want, dbSQL)
		}
	}
	// repmgrd is pointed at the real conf via /etc/default/repmgrd.
	defaults := find(cmds, "repmgrd defaults")
	if !strings.Contains(defaults.Cmd, "/etc/default/repmgrd") {
		t.Fatalf("repmgrd defaults not written to /etc/default/repmgrd: %s", defaults.Cmd)
	}
	for _, want := range []string{"REPMGRD_ENABLED=yes", "REPMGRD_CONF=\"/etc/postgresql/17/main/repmgr.conf\""} {
		if !strings.Contains(string(defaults.Stdin), want) {
			t.Fatalf("repmgrd defaults missing %q:\n%s", want, string(defaults.Stdin))
		}
	}
}

func TestRepmgrStandbyCommands(t *testing.T) {
	cmds := RepmgrStandbyCommands(RepmgrStandbyParams{
		Version: "17", Cluster: "main", ConfPath: "/etc/postgresql/17/main/repmgr.conf",
		Conf: "conf-body", PrimaryHost: "10.0.0.1", PrimaryPort: 5432,
		ReplUser: "repmgr", ReplPassword: "pw",
	})
	clone := find(cmds, "standby clone")
	// Fixed guard: key on primary_conninfo in postgresql.auto.conf + empty the
	// datadir; never the broken PG_VERSION guard.
	for _, want := range []string{"host=10.0.0.1", "postgresql.auto.conf", "find", "-mindepth 1 -delete", "standby clone --force"} {
		if !strings.Contains(clone.Cmd, want) {
			t.Fatalf("clone missing %q:\n%s", want, clone.Cmd)
		}
	}
	if strings.Contains(clone.Cmd, "PG_VERSION") {
		t.Fatalf("clone still uses the broken PG_VERSION guard:\n%s", clone.Cmd)
	}
	// pgpass carries the repmgr credential as a wildcard entry on stdin, never argv.
	pgpass := find(cmds, "standby pgpass")
	if !strings.Contains(string(pgpass.Stdin), "*:*:*:repmgr:pw") {
		t.Fatalf("standby pgpass missing wildcard repmgr entry:\n%s", string(pgpass.Stdin))
	}
	if strings.Contains(pgpass.Cmd, "pw") {
		t.Fatalf("password leaked into pgpass argv: %s", pgpass.Cmd)
	}
	if find(cmds, "repmgrd defaults").Cmd == "" {
		t.Fatal("standby did not configure /etc/default/repmgrd")
	}
}

func TestRepmgrWitnessCommands(t *testing.T) {
	cmds := RepmgrWitnessCommands(RepmgrStandbyParams{
		Version: "17", Cluster: "main", ConfPath: "/etc/postgresql/17/main/repmgr.conf",
		Conf: "conf-body", PrimaryHost: "10.0.0.1", PrimaryPort: 5432,
		ReplUser: "repmgr", ReplPassword: "pw",
	})
	joined := strings.Join(labels(cmds), ",")
	for _, want := range []string{"witness conf", "witness role", "witness database", "witness register", "repmgrd defaults"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("witness labels missing %q: %s", want, joined)
		}
	}
	if !strings.Contains(find(cmds, "witness register").Cmd, "witness register --force") {
		t.Fatalf("witness register command wrong: %s", find(cmds, "witness register").Cmd)
	}
}

func TestPatroniCommands(t *testing.T) {
	cmds := PatroniCommands(PatroniNodeParams{
		Version: "17", Cluster: "main", ClusterName: "pgcluster",
		DataDir: "/var/lib/postgresql/17/main", YAML: "scope: pgcluster\n",
	})
	joined := strings.Join(labels(cmds), ",")
	for _, want := range []string{"patroni install", "patroni conf", "patroni dropin", "patroni daemon-reload", "patroni takeover", "patroni service"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("patroni labels missing %q: %s", want, joined)
		}
	}
	// Install goes through the lock-wait apt prefix and pulls the etcd DCS client
	// (Debian ships etcd support in the separate patroni-etcd package, not as a
	// Recommends — without it patronictl can't find an etcd DCS implementation).
	install := find(cmds, "patroni install").Cmd
	if !strings.Contains(install, "DPkg::Lock::Timeout=300") {
		t.Fatalf("patroni install not lock-wait: %s", install)
	}
	if !strings.Contains(install, "python3-etcd3") {
		t.Fatalf("patroni install missing etcd DCS client package: %s", install)
	}
	// The drop-in clears the packaged unit's ConditionPathExists gate (else systemd
	// skips the service) and resets ExecStart to run our per-cluster config path.
	dropin := find(cmds, "patroni dropin")
	for _, want := range []string{"[Unit]\nConditionPathExists=\n", "ExecStart=\n", "/usr/bin/patroni /etc/patroni/pgcluster.yml"} {
		if !strings.Contains(string(dropin.Stdin), want) {
			t.Fatalf("patroni drop-in missing %q:\n%s", want, string(dropin.Stdin))
		}
	}
	// Takeover: stop + disable the packaged unit and empty the data_dir, guarded
	// on patroni.dynamic.json so a live Patroni node is never wiped.
	takeover := find(cmds, "patroni takeover")
	for _, want := range []string{"patroni.dynamic.json", "pg_ctlcluster 17 main stop", "systemctl disable postgresql@17-main", "-mindepth 1 -delete"} {
		if !strings.Contains(takeover.Cmd, want) {
			t.Fatalf("patroni takeover missing %q:\n%s", want, takeover.Cmd)
		}
	}
}

func TestPatroniNodeDefaultsBinDir(t *testing.T) {
	// bin_dir must default to the versioned Debian path so Patroni initdb's with
	// the right server binaries.
	cmds, err := NodeCommands(NodeSpec{
		Mode: ModePatroni, Role: RolePrimary, Version: "17", Cluster: "main",
		ClusterName: "pgcluster", NodeName: "n1", DCSReference: "10.0.0.30:2379",
	})
	if err != nil {
		t.Fatalf("NodeCommands: %v", err)
	}
	conf := find(cmds, "patroni conf")
	if !strings.Contains(string(conf.Stdin), "bin_dir: /usr/lib/postgresql/17/bin") {
		t.Fatalf("patroni.yml missing defaulted bin_dir:\n%s", string(conf.Stdin))
	}
}

func TestRenderPatroniYAML(t *testing.T) {
	y := RenderPatroniYAML(PatroniParams{
		Scope: "pgcluster", NodeName: "node1", DCSHosts: "10.0.0.10:2379",
		RestAPIListen: "0.0.0.0:8008", RestAPIConnect: "10.0.0.20:8008",
		PGListen: "0.0.0.0:5432", PGConnect: "10.0.0.20:5432",
		DataDir: "/var/lib/postgresql/16/main", Synchronous: true,
		ReplUser: "replicator", ReplPassword: "rp", SuperUser: "postgres", SuperPassword: "sp",
	})
	for _, want := range []string{
		"scope: pgcluster",
		"name: node1",
		"etcd3:", // default DCS backend
		"hosts: 10.0.0.10:2379",
		"synchronous_mode: true",
		"data_dir: /var/lib/postgresql/16/main",
		"connect_address: 10.0.0.20:5432",
		"username: replicator",
	} {
		if !strings.Contains(y, want) {
			t.Fatalf("patroni.yml missing %q:\n%s", want, y)
		}
	}
}
