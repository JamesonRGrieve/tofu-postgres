// SPDX-License-Identifier: AGPL-3.0-or-later

package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/JamesonRGrieve/tofu-postgres/internal/postgres"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestConfigSettingsOrderAndRestart(t *testing.T) {
	m := configModel{
		SharedBuffers:      types.StringValue("256MB"),
		WorkMem:            types.StringValue("16MB"),
		MaxConnections:     types.Int64Value(100),
		PasswordEncryption: types.StringValue("scram-sha-256"),
		WalInitZero:        types.BoolValue(false),
		// effective_cache_size, maintenance_work_mem, listen_addresses, wal_recycle left null
	}
	settings, keys := m.settings()
	if len(settings) != 5 {
		t.Fatalf("want 5 declared settings, got %d: %v", len(settings), keys)
	}
	// shared_buffers is postmaster-context → restart.
	if !postgres.NeedsRestart(keys) {
		t.Fatal("shared_buffers should force restart")
	}
	// wal_init_zero renders unquoted as off.
	rendered := postgres.RenderConfD(settings)
	if !strings.Contains(rendered, "wal_init_zero = off") {
		t.Fatalf("wal_init_zero not rendered off:\n%s", rendered)
	}
	if !strings.Contains(rendered, "shared_buffers = '256MB'") {
		t.Fatalf("shared_buffers not quoted:\n%s", rendered)
	}
}

func TestBuildConfigCommandsReloadPath(t *testing.T) {
	settings := []postgres.ConfSetting{{Key: "work_mem", Value: "16MB", Quote: true}}
	hba := []postgres.HBAEntry{{Type: "host", Database: "all", User: "all", Address: "10.0.0.0/24"}}
	cmds := buildConfigCommands("16", "main", settings, hba, false)
	if len(cmds) != 3 {
		t.Fatalf("want write+hba+reload = 3 commands, got %d", len(cmds))
	}
	last := cmds[len(cmds)-1].Cmd
	if !strings.Contains(last, "pg_ctlcluster 16 main reload") {
		t.Fatalf("last command should reload: %q", last)
	}
	// The conf.d write carries the rendered file on stdin.
	if !strings.Contains(string(cmds[0].Stdin), "work_mem") {
		t.Fatalf("conf.d stdin missing setting: %q", cmds[0].Stdin)
	}
	// The pg_hba step carries the rendered block on stdin.
	if !strings.Contains(string(cmds[1].Stdin), postgres.HBABeginMarker) {
		t.Fatalf("hba stdin missing marker: %q", cmds[1].Stdin)
	}
}

func TestBuildConfigCommandsRestartPath(t *testing.T) {
	settings := []postgres.ConfSetting{{Key: "shared_buffers", Value: "256MB", Quote: true}}
	cmds := buildConfigCommands("16", "main", settings, nil, true)
	last := cmds[len(cmds)-1].Cmd
	if !strings.Contains(last, "pg_ctlcluster 16 main restart") {
		t.Fatalf("last command should restart: %q", last)
	}
}

func TestConfigApplyInjectedExec(t *testing.T) {
	rec := &recorder{}
	r := &configResource{}
	m := configModel{
		Version:       types.StringValue("16"),
		Cluster:       types.StringValue("main"),
		SharedBuffers: types.StringValue("256MB"),
		PgHba:         types.ListNull(hbaObjectType()),
	}
	if err := r.apply(context.Background(), m, rec.fn()); err != nil {
		t.Fatalf("apply error: %v", err)
	}
	// write drop-in + restart (no hba declared) = 2 commands.
	if len(rec.cmds) != 2 {
		t.Fatalf("want 2 commands, got %d: %v", len(rec.cmds), rec.cmds)
	}
	if !strings.Contains(rec.joined(), "restart") {
		t.Fatalf("shared_buffers should have restarted: %s", rec.joined())
	}
}
