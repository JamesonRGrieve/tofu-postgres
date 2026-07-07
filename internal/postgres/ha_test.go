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
	for _, want := range []string{"wal_level = replica", "max_wal_senders = 5", "hot_standby = on"} {
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
	conf := RenderRepmgrConf(RepmgrConfParams{
		NodeID: 1, NodeName: "node1",
		Conninfo: "host=10.0.0.1 port=5432 user=repmgr dbname=repmgr",
		DataDir:  "/var/lib/postgresql/16/main",
	})
	for _, want := range []string{
		"node_id=1",
		"node_name='node1'",
		"conninfo='host=10.0.0.1 port=5432 user=repmgr dbname=repmgr'",
		"data_directory='/var/lib/postgresql/16/main'",
		"replication_user='repmgr'",
		"failover='automatic'",
	} {
		if !strings.Contains(conf, want) {
			t.Fatalf("repmgr.conf missing %q:\n%s", want, conf)
		}
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
