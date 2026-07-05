// SPDX-License-Identifier: AGPL-3.0-or-later

package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/JamesonRGrieve/tofu-postgres/internal/postgres"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestGrantCommandsRevokeThenGrant(t *testing.T) {
	rec := &recorder{}
	spec := postgres.GrantSpec{Role: "app", Database: "appdb", ObjectType: postgres.GrantDatabase, Privileges: []string{"CONNECT"}}
	if err := postgres.RunCommands(grantCommands(spec), rec.fn()); err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if len(rec.cmds) != 2 {
		t.Fatalf("want revoke+grant = 2 commands, got %d", len(rec.cmds))
	}
	if !strings.Contains(string(rec.stdin[0]), "REVOKE ALL PRIVILEGES ON DATABASE") {
		t.Fatalf("first stdin should revoke all: %q", rec.stdin[0])
	}
	if !strings.Contains(string(rec.stdin[1]), `GRANT CONNECT ON DATABASE "appdb" TO "app"`) {
		t.Fatalf("second stdin should grant declared: %q", rec.stdin[1])
	}
	// A database grant connects to the default maintenance database (no -d).
	if strings.Contains(rec.cmds[0], " -d ") {
		t.Fatalf("database grant should not select a db: %q", rec.cmds[0])
	}
}

func TestGrantSchemaSelectsDatabase(t *testing.T) {
	rec := &recorder{}
	spec := postgres.GrantSpec{Role: "app", Database: "appdb", ObjectType: postgres.GrantSchema, Schema: "public", Privileges: []string{"USAGE"}}
	if err := postgres.RunCommands(grantCommands(spec), rec.fn()); err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if !strings.Contains(rec.cmds[1], `-d "appdb"`) {
		t.Fatalf("schema grant should connect to target db: %q", rec.cmds[1])
	}
}

func TestGrantModelSpec(t *testing.T) {
	ctx := context.Background()
	privs, _ := types.ListValueFrom(ctx, types.StringType, []string{"SELECT", "INSERT"})
	objs, _ := types.ListValueFrom(ctx, types.StringType, []string{"t1"})
	m := grantModel{
		Role:       types.StringValue("app"),
		Database:   types.StringValue("appdb"),
		ObjectType: types.StringValue(postgres.GrantTable),
		Schema:     types.StringValue("public"),
		Objects:    objs,
		Privileges: privs,
	}
	spec := m.spec(ctx)
	if spec.ObjectType != postgres.GrantTable || len(spec.Objects) != 1 || len(spec.Privileges) != 2 {
		t.Fatalf("spec = %+v", spec)
	}
	if got := grantID(m); got != "app:appdb:table" {
		t.Fatalf("grantID = %q", got)
	}
}
