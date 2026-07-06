// SPDX-License-Identifier: AGPL-3.0-or-later

package postgres

import (
	"strings"
	"testing"
)

func TestQuoteIdent(t *testing.T) {
	cases := []struct{ in, want string }{
		{"app", `"app"`},
		{"weird name", `"weird name"`},
		{`ro"le`, `"ro""le"`},
		{`a"; DROP DATABASE x; --`, `"a""; DROP DATABASE x; --"`},
	}
	for _, tc := range cases {
		if got := QuoteIdent(tc.in); got != tc.want {
			t.Errorf("QuoteIdent(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestQuoteLiteral(t *testing.T) {
	cases := []struct{ in, want string }{
		{"UTF8", "'UTF8'"},
		{"en_US.UTF-8", "'en_US.UTF-8'"},
		{"O'Brien", "'O''Brien'"},
	}
	for _, tc := range cases {
		if got := QuoteLiteral(tc.in); got != tc.want {
			t.Errorf("QuoteLiteral(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPsqlExec(t *testing.T) {
	if got := PsqlExec(""); !strings.HasPrefix(got, "su postgres -c ") || strings.Contains(got, " -d ") {
		t.Fatalf("PsqlExec(\"\") = %q", got)
	}
	got := PsqlExec("my db")
	if !strings.Contains(got, `-d "my db"`) {
		t.Fatalf("PsqlExec db arg not double-quoted: %q", got)
	}
	// The whole inner command is single-quoted for `su -c`, so the injected
	// double-quoted db survives verbatim.
	if !strings.Contains(got, "psql -tAqX -v ON_ERROR_STOP=1") {
		t.Fatalf("PsqlExec missing psql flags: %q", got)
	}
}

func TestCreateDatabaseSQL(t *testing.T) {
	cases := []struct {
		name string
		spec DatabaseSpec
		want string
	}{
		{
			name: "name only",
			spec: DatabaseSpec{Name: "app"},
			want: `CREATE DATABASE "app"`,
		},
		{
			name: "encoding forces template0",
			spec: DatabaseSpec{Name: "app", Owner: "app_owner", Encoding: "UTF8"},
			want: `CREATE DATABASE "app" OWNER "app_owner" ENCODING 'UTF8' TEMPLATE "template0"`,
		},
		{
			name: "locale forces template0",
			spec: DatabaseSpec{Name: "app", Encoding: "UTF8", LCCollate: "C", LCCtype: "C"},
			want: `CREATE DATABASE "app" ENCODING 'UTF8' LC_COLLATE 'C' LC_CTYPE 'C' TEMPLATE "template0"`,
		},
		{
			name: "explicit template wins",
			spec: DatabaseSpec{Name: "app", Template: "template1"},
			want: `CREATE DATABASE "app" TEMPLATE "template1"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CreateDatabaseSQL(tc.spec); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAlterAndDropDatabaseSQL(t *testing.T) {
	if got := AlterDatabaseOwnerSQL("app", "new_owner"); got != `ALTER DATABASE "app" OWNER TO "new_owner"` {
		t.Errorf("alter = %q", got)
	}
	if got := DropDatabaseSQL("app"); got != `DROP DATABASE IF EXISTS "app"` {
		t.Errorf("drop = %q", got)
	}
}

func TestParseDatabaseRow(t *testing.T) {
	if _, ok := ParseDatabaseRow("   \n"); ok {
		t.Fatal("empty output should report absent")
	}
	info, ok := ParseDatabaseRow("app|app_owner|UTF8|en_US.UTF-8|en_US.UTF-8\n")
	if !ok {
		t.Fatal("row should parse")
	}
	if info.Name != "app" || info.Owner != "app_owner" || info.Encoding != "UTF8" || info.LCCollate != "en_US.UTF-8" {
		t.Fatalf("parsed = %+v", info)
	}
}

func TestRoleOptionClauseAndSQL(t *testing.T) {
	s := RoleSpec{Name: "app", Login: true, CreateDB: true}
	create := CreateRoleSQL(s)
	if !strings.HasPrefix(create, `CREATE ROLE "app" WITH `) {
		t.Fatalf("create prefix = %q", create)
	}
	for _, want := range []string{"LOGIN", "NOSUPERUSER", "CREATEDB", "NOCREATEROLE"} {
		if !strings.Contains(create, want) {
			t.Errorf("create missing %q: %q", want, create)
		}
	}
	// A password is rendered as a quoted literal only when present.
	if strings.Contains(create, "PASSWORD") {
		t.Errorf("no password expected: %q", create)
	}
	withPw := CreateRoleSQL(RoleSpec{Name: "app", Login: true, Password: "s3cr'et"})
	if !strings.Contains(withPw, "PASSWORD 's3cr''et'") {
		t.Errorf("password not quoted/escaped: %q", withPw)
	}
	if got := AlterRoleSQL(s); !strings.HasPrefix(got, `ALTER ROLE "app" WITH `) {
		t.Errorf("alter = %q", got)
	}
	if got := DropRoleSQL("app"); got != `DROP ROLE IF EXISTS "app"` {
		t.Errorf("drop = %q", got)
	}
}

func TestParseRoleRow(t *testing.T) {
	if _, ok := ParseRoleRow(""); ok {
		t.Fatal("empty output should report absent")
	}
	info, ok := ParseRoleRow("t|f|t|f\n")
	if !ok {
		t.Fatal("row should parse")
	}
	if !info.Login || info.Superuser || !info.CreateDB || info.CreateRole {
		t.Fatalf("parsed = %+v", info)
	}
}

func TestGrantSQL(t *testing.T) {
	cases := []struct {
		name string
		spec GrantSpec
		want string
	}{
		{
			name: "database all",
			spec: GrantSpec{Role: "app", Database: "appdb", ObjectType: GrantDatabase, Privileges: []string{"ALL"}},
			want: `GRANT ALL PRIVILEGES ON DATABASE "appdb" TO "app"`,
		},
		{
			name: "database subset",
			spec: GrantSpec{Role: "app", Database: "appdb", ObjectType: GrantDatabase, Privileges: []string{"connect", "temporary"}},
			want: `GRANT CONNECT, TEMPORARY ON DATABASE "appdb" TO "app"`,
		},
		{
			name: "schema usage",
			spec: GrantSpec{Role: "app", Database: "appdb", ObjectType: GrantSchema, Schema: "public", Privileges: []string{"USAGE"}},
			want: `GRANT USAGE ON SCHEMA "public" TO "app"`,
		},
		{
			name: "all tables",
			spec: GrantSpec{Role: "app", Database: "appdb", ObjectType: GrantAllTables, Schema: "public", Privileges: []string{"SELECT"}},
			want: `GRANT SELECT ON ALL TABLES IN SCHEMA "public" TO "app"`,
		},
		{
			name: "specific tables",
			spec: GrantSpec{Role: "app", Database: "appdb", ObjectType: GrantTable, Schema: "public", Objects: []string{"t1", "t2"}, Privileges: []string{"SELECT", "INSERT"}},
			want: `GRANT SELECT, INSERT ON TABLE "public"."t1", "public"."t2" TO "app"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := GrantSQL(tc.spec); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRevokeSQL(t *testing.T) {
	s := GrantSpec{Role: "app", Database: "appdb", ObjectType: GrantDatabase}
	if got := RevokeSQL(s, []string{"ALL"}); got != `REVOKE ALL PRIVILEGES ON DATABASE "appdb" FROM "app"` {
		t.Errorf("revoke = %q", got)
	}
}

func TestGrantDB(t *testing.T) {
	if got := (GrantSpec{ObjectType: GrantDatabase, Database: "appdb"}).GrantDB(); got != "" {
		t.Errorf("database grant should connect to default, got %q", got)
	}
	if got := (GrantSpec{ObjectType: GrantSchema, Database: "appdb"}).GrantDB(); got != "appdb" {
		t.Errorf("schema grant should connect to target db, got %q", got)
	}
}

func TestReadGrantSQL(t *testing.T) {
	cases := []struct {
		name     string
		spec     GrantSpec
		contains []string
	}{
		{"database", GrantSpec{Role: "app", Database: "appdb", ObjectType: GrantDatabase}, []string{"aclexplode(d.datacl)", "'appdb'", "'app'"}},
		{"schema", GrantSpec{Role: "app", ObjectType: GrantSchema, Schema: "public"}, []string{"aclexplode(n.nspacl)", "'public'"}},
		{"all tables", GrantSpec{Role: "app", ObjectType: GrantAllTables, Schema: "public"}, []string{"role_table_grants", "table_schema = 'public'"}},
		{"tables", GrantSpec{Role: "app", ObjectType: GrantTable, Schema: "public", Objects: []string{"t1"}}, []string{"role_table_grants", "table_name IN ('t1')"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := ReadGrantSQL(tc.spec)
			for _, want := range tc.contains {
				if !strings.Contains(q, want) {
					t.Errorf("query %q missing %q", q, want)
				}
			}
		})
	}
}

func TestParseGrantPrivileges(t *testing.T) {
	got := ParseGrantPrivileges("CONNECT\ntemporary\nCONNECT\n\nCREATE\n")
	want := []string{"CONNECT", "CREATE", "TEMPORARY"}
	if !stringSetEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestReconcileGrantPrivileges(t *testing.T) {
	cases := []struct {
		name       string
		objectType string
		prior      []string
		observed   []string
		want       []string
	}{
		{"all collapses when prior all", GrantDatabase, []string{"ALL"}, []string{"CONNECT", "CREATE", "TEMPORARY"}, []string{"ALL"}},
		{"all collapses on fresh import", GrantDatabase, nil, []string{"TEMPORARY", "CONNECT", "CREATE"}, []string{"ALL"}},
		{"explicit full list stays a list", GrantDatabase, []string{"CONNECT", "CREATE", "TEMPORARY"}, []string{"CONNECT", "CREATE", "TEMPORARY"}, []string{"CONNECT", "CREATE", "TEMPORARY"}},
		{"partial stays a list", GrantDatabase, []string{"CONNECT"}, []string{"CONNECT"}, []string{"CONNECT"}},
		{"empty observed yields nil", GrantDatabase, []string{"ALL"}, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ReconcileGrantPrivileges(tc.objectType, tc.prior, tc.observed)
			if !stringSetEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
