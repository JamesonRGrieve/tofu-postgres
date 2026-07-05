// SPDX-License-Identifier: AGPL-3.0-or-later

package provider

import (
	"strings"
	"testing"

	"github.com/JamesonRGrieve/tofu-postgres/internal/postgres"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestRoleCreateSQLFromModel(t *testing.T) {
	m := roleModel{
		Name:       types.StringValue("app"),
		Login:      types.BoolValue(true),
		Superuser:  types.BoolValue(false),
		CreateDB:   types.BoolValue(true),
		CreateRole: types.BoolValue(false),
	}
	sql := postgres.CreateRoleSQL(m.spec("s3cret"))
	for _, want := range []string{`CREATE ROLE "app" WITH`, "LOGIN", "NOSUPERUSER", "CREATEDB", "NOCREATEROLE", "PASSWORD 's3cret'"} {
		if !strings.Contains(sql, want) {
			t.Errorf("create sql missing %q: %q", want, sql)
		}
	}
	// An empty (unchanged) password never renders a PASSWORD clause.
	if strings.Contains(postgres.CreateRoleSQL(m.spec("")), "PASSWORD") {
		t.Error("empty password should omit PASSWORD clause")
	}
}

func TestRoleFinishInjectedExec(t *testing.T) {
	rec := &recorder{reply: map[string][]byte{"psql": []byte("t|f|t|f\n")}}
	r := &roleResource{}
	m := roleModel{Name: types.StringValue("app")}
	r.finish(&m, rec.fn(), nil)
	if m.ID.ValueString() != "app" {
		t.Fatalf("id = %q", m.ID.ValueString())
	}
	if !m.Login.ValueBool() || m.Superuser.ValueBool() || !m.CreateDB.ValueBool() || m.CreateRole.ValueBool() {
		t.Fatalf("reconciled flags = %+v", m)
	}
	// The read-back runs as the postgres superuser reading its SQL from stdin.
	if !strings.HasPrefix(rec.cmds[0], "su postgres -c ") {
		t.Fatalf("read-back should run as postgres: %q", rec.cmds[0])
	}
	if !strings.Contains(string(rec.stdin[0]), "pg_roles") {
		t.Fatalf("read-back SQL should hit pg_roles: %q", rec.stdin[0])
	}
}

func TestReconcileRoleNeverSetsPassword(t *testing.T) {
	m := roleModel{Name: types.StringValue("app")}
	reconcileRole(&m, postgres.RoleInfo{Login: true})
	if !m.Password.IsNull() {
		t.Fatal("password must never be populated from read-back")
	}
}
