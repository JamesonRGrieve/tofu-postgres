// SPDX-License-Identifier: AGPL-3.0-or-later

package postgres

import (
	"strings"
	"testing"
)

func TestHAModeValid(t *testing.T) {
	for _, m := range []HAMode{ModeStreaming, ModeRepmgr, ModePatroni} {
		if !m.Valid() {
			t.Fatalf("%q should be valid", m)
		}
	}
	if HAMode("galera").Valid() {
		t.Fatal("galera should be invalid")
	}
}

func TestNodeCommandsDispatch(t *testing.T) {
	base := NodeSpec{
		Version: "16", Cluster: "main", ClusterName: "pgcluster", NodeName: "node1", NodeID: 1,
		Host: "10.0.0.20", PrimaryHost: "10.0.0.10", ReplicationUser: "replicator", ReplicationSlot: "node1",
		DCSReference: "10.0.0.30:2379", RestAPIConnect: "10.0.0.20:8008", PGConnect: "10.0.0.20:5432",
		SuperUser: "postgres", SuperPassword: "sp", ReplicationPassword: "rp",
	}

	cases := []struct {
		name       string
		mode       HAMode
		role       NodeRole
		wantLabel  string // a label the first command should contain
		wantSubstr string // a substring somewhere in the command set
	}{
		{"streaming primary", ModeStreaming, RolePrimary, "streaming primary", "wal_level = replica"},
		{"streaming replica", ModeStreaming, RoleReplica, "streaming standby", "pg_basebackup"},
		{"repmgr primary", ModeRepmgr, RolePrimary, "repmgr primary", "primary register"},
		{"repmgr replica", ModeRepmgr, RoleReplica, "repmgr standby", "standby clone"},
		{"repmgr witness", ModeRepmgr, RoleWitness, "repmgr witness", "witness register"},
		{"patroni primary", ModePatroni, RolePrimary, "patroni", "scope: pgcluster"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := base
			s.Mode, s.Role = tc.mode, tc.role
			cmds, err := NodeCommands(s)
			if err != nil {
				t.Fatalf("NodeCommands error: %v", err)
			}
			if len(cmds) == 0 {
				t.Fatal("no commands emitted")
			}
			if !strings.Contains(cmds[0].Label, tc.wantLabel) {
				t.Fatalf("first label = %q, want contains %q", cmds[0].Label, tc.wantLabel)
			}
			var all strings.Builder
			for _, c := range cmds {
				all.WriteString(c.Cmd)
				all.Write(c.Stdin)
			}
			if !strings.Contains(all.String(), tc.wantSubstr) {
				t.Fatalf("command set missing %q for %s", tc.wantSubstr, tc.name)
			}
		})
	}
}

func TestNodeCommandsErrors(t *testing.T) {
	if _, err := NodeCommands(NodeSpec{Mode: "nope", Role: RolePrimary}); err == nil {
		t.Fatal("unknown mode should error")
	}
	if _, err := NodeCommands(NodeSpec{Mode: ModeStreaming, Role: "nope"}); err == nil {
		t.Fatal("unknown role should error")
	}
	if _, err := NodeCommands(NodeSpec{Mode: ModeStreaming, Role: RoleWitness}); err == nil {
		t.Fatal("streaming+witness is unsupported and should error")
	}
}
