// SPDX-License-Identifier: AGPL-3.0-or-later

package provider

import (
	"strings"
	"testing"

	"github.com/JamesonRGrieve/tofu-postgres/internal/postgres"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestDatabaseCreateCommandsInjectedExec(t *testing.T) {
	rec := &recorder{}
	spec := postgres.DatabaseSpec{Name: "app", Owner: "app_owner", Encoding: "UTF8"}
	if err := postgres.RunCommands(createCommands(spec), rec.fn()); err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if len(rec.cmds) != 1 {
		t.Fatalf("want 1 command, got %d", len(rec.cmds))
	}
	// The statement rides on stdin (never argv), quoting the identifiers.
	sql := string(rec.stdin[0])
	if !strings.Contains(sql, `CREATE DATABASE "app" OWNER "app_owner" ENCODING 'UTF8'`) {
		t.Fatalf("create stdin = %q", sql)
	}
	if !strings.HasPrefix(rec.cmds[0], "su postgres -c ") {
		t.Fatalf("command should run as postgres superuser: %q", rec.cmds[0])
	}
}

func TestDatabaseReconcile(t *testing.T) {
	m := databaseModel{
		Name:      types.StringValue("app"),
		LCCollate: types.StringNull(), // undeclared → not reconciled
	}
	r := &databaseResource{}
	r.reconcile(&m, postgres.DatabaseInfo{Name: "app", Owner: "app_owner", Encoding: "UTF8", LCCollate: "C"})
	if m.Owner.ValueString() != "app_owner" {
		t.Fatalf("owner = %q", m.Owner.ValueString())
	}
	if m.Encoding.ValueString() != "UTF8" {
		t.Fatalf("encoding = %q", m.Encoding.ValueString())
	}
	// lc_collate was null in config, so it must stay null (manage-declared-only).
	if !m.LCCollate.IsNull() {
		t.Fatalf("undeclared lc_collate should stay null, got %q", m.LCCollate.ValueString())
	}
}

func TestDatabaseReconcileDeclaredLocale(t *testing.T) {
	m := databaseModel{Name: types.StringValue("app"), LCCollate: types.StringValue("C"), LCCtype: types.StringValue("C")}
	r := &databaseResource{}
	r.reconcile(&m, postgres.DatabaseInfo{Name: "app", Owner: "o", Encoding: "UTF8", LCCollate: "en_US.UTF-8", LCCtype: "en_US.UTF-8"})
	if m.LCCollate.ValueString() != "en_US.UTF-8" {
		t.Fatalf("declared lc_collate should refresh, got %q", m.LCCollate.ValueString())
	}
}
