// SPDX-License-Identifier: AGPL-3.0-or-later

package provider

import (
	"strings"
	"testing"

	"github.com/JamesonRGrieve/tofu-postgres/internal/postgres"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestNodeSpecProjection(t *testing.T) {
	m := clusterNodeModel{
		Cluster:         types.StringValue("pgcluster"),
		HAMode:          types.StringValue("repmgr"),
		Version:         types.StringValue("16"),
		PGCluster:       types.StringValue("main"),
		NodeName:        types.StringValue("node1"),
		NodeID:          types.Int64Value(1),
		Host:            types.StringValue("10.0.0.20"),
		Role:            types.StringValue("primary"),
		ReplicationUser: types.StringValue("repmgr"),
	}
	s := m.nodeSpec()
	if s.Mode != postgres.ModeRepmgr || s.Role != postgres.RolePrimary {
		t.Fatalf("mode/role = %q/%q", s.Mode, s.Role)
	}
	if s.ClusterName != "pgcluster" || s.Cluster != "main" || s.NodeID != 1 {
		t.Fatalf("projection = %#v", s)
	}
}

func TestClusterNodeApplyDispatch(t *testing.T) {
	cases := []struct {
		name       string
		mode, role string
		wantSubstr string
	}{
		{"streaming primary", "streaming", "primary", "wal_level = replica"},
		{"streaming replica", "streaming", "replica", "pg_basebackup"},
		{"repmgr primary", "repmgr", "primary", "primary register"},
		{"patroni node", "patroni", "primary", "scope: pgcluster"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			r := &clusterNodeResource{}
			m := clusterNodeModel{
				Cluster: types.StringValue("pgcluster"), HAMode: types.StringValue(tc.mode),
				Version: types.StringValue("16"), PGCluster: types.StringValue("main"),
				NodeName: types.StringValue("node1"), NodeID: types.Int64Value(1),
				Host: types.StringValue("10.0.0.20"), Role: types.StringValue(tc.role),
				PrimaryHost: types.StringValue("10.0.0.10"), ReplicationUser: types.StringValue("repl"),
				ReplicationSlot: types.StringValue("node1"), DCSReference: types.StringValue("10.0.0.30:2379"),
				RestAPIConnect: types.StringValue("10.0.0.20:8008"), PGConnect: types.StringValue("10.0.0.20:5432"),
				SuperUser: types.StringValue("postgres"), SuperPassword: types.StringValue("sp"),
				ReplicationPassword: types.StringValue("rp"),
			}
			if err := r.apply(m, rec.fn()); err != nil {
				t.Fatalf("apply error: %v", err)
			}
			all := rec.joined()
			for i, s := range rec.stdin {
				_ = i
				all += "\n" + string(s)
			}
			if !strings.Contains(all, tc.wantSubstr) {
				t.Fatalf("%s: dispatched commands missing %q", tc.name, tc.wantSubstr)
			}
		})
	}
}

func TestClusterNodeApplyUnknownMode(t *testing.T) {
	rec := &recorder{}
	r := &clusterNodeResource{}
	m := clusterNodeModel{
		Cluster: types.StringValue("c"), HAMode: types.StringValue("galera"),
		Version: types.StringValue("16"), PGCluster: types.StringValue("main"),
		NodeName: types.StringValue("n1"), Host: types.StringValue("h"), Role: types.StringValue("primary"),
	}
	if err := r.apply(m, rec.fn()); err == nil {
		t.Fatal("unknown mode should error before running any command")
	}
	if len(rec.cmds) != 0 {
		t.Fatalf("no commands should run on an unknown mode, ran %d", len(rec.cmds))
	}
}
