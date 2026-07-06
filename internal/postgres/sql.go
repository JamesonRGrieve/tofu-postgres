// SPDX-License-Identifier: AGPL-3.0-or-later

package postgres

import (
	"sort"
	"strings"
)

// This file owns the logical-object layer: pure SQL-statement builders and the
// grant read-back parsing for postgres_database / postgres_role / postgres_grant.
// PostgreSQL exposes no management REST API for logical objects either, so these
// are driven through `psql` run as the postgres superuser over the same SSH
// transport. Every statement is fed to psql on stdin (never argv) so secrets and
// identifiers stay out of the process list; identifiers are always double-quoted
// and string literals single-quoted, so untrusted input is never interpolated
// raw. The builders are pure and table-tested; the provider layer wires them to
// the framework via the injected-exec seam.

// QuoteIdent double-quotes a SQL identifier (role / database / schema / table),
// doubling any embedded double quote per the SQL standard. Use it for every
// identifier that reaches a statement — never interpolate one unquoted.
func QuoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// QuoteLiteral single-quotes a SQL string literal, doubling any embedded single
// quote. Use it for values (encoding, collation, a role/db name inside a WHERE).
func QuoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// shDoubleQuote wraps a value in double quotes for a shell context that is
// itself already single-quoted (the `-d <db>` argument inside `su postgres -c
// '...'`), escaping the characters the shell still expands inside double quotes.
func shDoubleQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", `$`, `\$`)
	return `"` + r.Replace(s) + `"`
}

// PsqlExec returns the shell command that runs psql as the postgres superuser,
// reading its SQL from stdin. db selects the target database (`-d`); empty
// connects to the default maintenance database (database-scoped work such as a
// GRANT ON DATABASE can run from anywhere, schema/table work must connect to the
// owning database). Flags: tuples-only, unaligned (pipe-separated), quiet, no
// startup file, stop on the first error.
func PsqlExec(db string) string {
	inner := "psql -tAqX -v ON_ERROR_STOP=1"
	if db != "" {
		inner += " -d " + shDoubleQuote(db)
	}
	return "su postgres -c " + shQuote(inner)
}

// --- postgres_database ------------------------------------------------------

// DatabaseSpec is the declared shape of a managed database.
type DatabaseSpec struct {
	Name      string
	Owner     string
	Encoding  string
	LCCollate string
	LCCtype   string
	Template  string
}

// effectiveTemplate resolves the template: an explicit value wins; otherwise any
// encoding OR locale override (ENCODING/LC_COLLATE/LC_CTYPE) forces template0 —
// template1 rejects a differing encoding/locale (e.g. a UTF8 database on a
// SQL_ASCII/C cluster) — and a plain database uses the server default (no clause).
func (s DatabaseSpec) effectiveTemplate() string {
	if s.Template != "" {
		return s.Template
	}
	if s.Encoding != "" || s.LCCollate != "" || s.LCCtype != "" {
		return "template0"
	}
	return ""
}

// CreateDatabaseSQL renders `CREATE DATABASE …` with only the declared options.
func CreateDatabaseSQL(s DatabaseSpec) string {
	var b strings.Builder
	b.WriteString("CREATE DATABASE " + QuoteIdent(s.Name))
	if s.Owner != "" {
		b.WriteString(" OWNER " + QuoteIdent(s.Owner))
	}
	if s.Encoding != "" {
		b.WriteString(" ENCODING " + QuoteLiteral(s.Encoding))
	}
	if s.LCCollate != "" {
		b.WriteString(" LC_COLLATE " + QuoteLiteral(s.LCCollate))
	}
	if s.LCCtype != "" {
		b.WriteString(" LC_CTYPE " + QuoteLiteral(s.LCCtype))
	}
	if tmpl := s.effectiveTemplate(); tmpl != "" {
		b.WriteString(" TEMPLATE " + QuoteIdent(tmpl))
	}
	return b.String()
}

// AlterDatabaseOwnerSQL reassigns a database's owner (the only in-place mutable
// attribute — encoding/locale are fixed at creation).
func AlterDatabaseOwnerSQL(name, owner string) string {
	return "ALTER DATABASE " + QuoteIdent(name) + " OWNER TO " + QuoteIdent(owner)
}

// DropDatabaseSQL drops a database if it exists.
func DropDatabaseSQL(name string) string {
	return "DROP DATABASE IF EXISTS " + QuoteIdent(name)
}

// ReadDatabaseSQL selects a database's current owner/encoding/locale for
// read-back reconciliation.
func ReadDatabaseSQL(name string) string {
	return "SELECT d.datname, r.rolname, pg_encoding_to_char(d.encoding), d.datcollate, d.datctype " +
		"FROM pg_database d JOIN pg_roles r ON r.oid = d.datdba WHERE d.datname = " + QuoteLiteral(name)
}

// DatabaseInfo is a database's read-back state.
type DatabaseInfo struct {
	Name      string
	Owner     string
	Encoding  string
	LCCollate string
	LCCtype   string
}

// ParseDatabaseRow parses one pipe-separated `-tA` psql row from ReadDatabaseSQL.
// ok is false when the output is empty (the database does not exist).
func ParseDatabaseRow(out string) (DatabaseInfo, bool) {
	line := strings.TrimSpace(out)
	if line == "" {
		return DatabaseInfo{}, false
	}
	f := strings.Split(line, "|")
	get := func(i int) string {
		if i < len(f) {
			return strings.TrimSpace(f[i])
		}
		return ""
	}
	return DatabaseInfo{
		Name:      get(0),
		Owner:     get(1),
		Encoding:  get(2),
		LCCollate: get(3),
		LCCtype:   get(4),
	}, true
}

// --- postgres_role ----------------------------------------------------------

// RoleSpec is the declared shape of a managed role. Password is ephemeral — it
// comes from a write-only attribute and is never stored in state; an empty
// Password means "leave the password unchanged".
type RoleSpec struct {
	Name       string
	Login      bool
	Superuser  bool
	CreateDB   bool
	CreateRole bool
	Password   string
}

// roleOptionClause renders the WITH-option list common to CREATE/ALTER ROLE,
// always emitting the explicit positive/negative form of each boolean so the
// role converges to exactly the declared attributes. A non-empty password is
// appended as a quoted literal.
func roleOptionClause(s RoleSpec) string {
	opt := func(on bool, yes, no string) string {
		if on {
			return yes
		}
		return no
	}
	parts := []string{
		opt(s.Login, "LOGIN", "NOLOGIN"),
		opt(s.Superuser, "SUPERUSER", "NOSUPERUSER"),
		opt(s.CreateDB, "CREATEDB", "NOCREATEDB"),
		opt(s.CreateRole, "CREATEROLE", "NOCREATEROLE"),
	}
	if s.Password != "" {
		parts = append(parts, "PASSWORD "+QuoteLiteral(s.Password))
	}
	return strings.Join(parts, " ")
}

// CreateRoleSQL renders `CREATE ROLE "name" WITH …`.
func CreateRoleSQL(s RoleSpec) string {
	return "CREATE ROLE " + QuoteIdent(s.Name) + " WITH " + roleOptionClause(s)
}

// AlterRoleSQL renders `ALTER ROLE "name" WITH …`, converging an existing role's
// attributes (and password when one is supplied).
func AlterRoleSQL(s RoleSpec) string {
	return "ALTER ROLE " + QuoteIdent(s.Name) + " WITH " + roleOptionClause(s)
}

// DropRoleSQL drops a role if it exists.
func DropRoleSQL(name string) string {
	return "DROP ROLE IF EXISTS " + QuoteIdent(name)
}

// ReadRoleSQL selects a role's attribute flags for read-back.
func ReadRoleSQL(name string) string {
	return "SELECT rolcanlogin, rolsuper, rolcreatedb, rolcreaterole " +
		"FROM pg_roles WHERE rolname = " + QuoteLiteral(name)
}

// RoleInfo is a role's read-back attribute state (password is never read back).
type RoleInfo struct {
	Login      bool
	Superuser  bool
	CreateDB   bool
	CreateRole bool
}

// ParseRoleRow parses one pipe-separated `-tA` psql row from ReadRoleSQL (`t`/`f`
// booleans). ok is false when the output is empty (the role does not exist).
func ParseRoleRow(out string) (RoleInfo, bool) {
	line := strings.TrimSpace(out)
	if line == "" {
		return RoleInfo{}, false
	}
	f := strings.Split(line, "|")
	b := func(i int) bool {
		return i < len(f) && strings.TrimSpace(f[i]) == "t"
	}
	return RoleInfo{
		Login:      b(0),
		Superuser:  b(1),
		CreateDB:   b(2),
		CreateRole: b(3),
	}, true
}

// --- postgres_grant ---------------------------------------------------------

// Grant object types.
const (
	GrantDatabase  = "database"
	GrantSchema    = "schema"
	GrantTable     = "table"
	GrantAllTables = "all_tables"
)

// allPrivilege is the sorted full privilege set each object type's ALL expands
// to — used to collapse a fully-granted read-back back to ["ALL"] for 0-diff.
var allPrivilege = map[string][]string{
	GrantDatabase:  {"CONNECT", "CREATE", "TEMPORARY"},
	GrantSchema:    {"CREATE", "USAGE"},
	GrantTable:     {"DELETE", "INSERT", "REFERENCES", "SELECT", "TRIGGER", "TRUNCATE", "UPDATE"},
	GrantAllTables: {"DELETE", "INSERT", "REFERENCES", "SELECT", "TRIGGER", "TRUNCATE", "UPDATE"},
}

// GrantSpec is the declared shape of a managed grant. Schema is used for
// schema/table/all_tables object types; Objects lists the specific tables for
// the `table` object type. Privileges is the declared list (e.g. ["ALL"] or
// ["CONNECT","SELECT"]).
type GrantSpec struct {
	Role       string
	Database   string
	ObjectType string
	Schema     string
	Objects    []string
	Privileges []string
}

// GrantDB is the database psql must connect to for this grant: none for a
// database-scoped grant (runnable from the default database), the target
// database for schema/table grants (their catalogs are per-database).
func (s GrantSpec) GrantDB() string {
	if s.ObjectType == GrantDatabase {
		return ""
	}
	return s.Database
}

// objectClause renders the `ON <object>` target for GRANT/REVOKE.
func (s GrantSpec) objectClause() string {
	switch s.ObjectType {
	case GrantSchema:
		return "SCHEMA " + QuoteIdent(s.Schema)
	case GrantAllTables:
		return "ALL TABLES IN SCHEMA " + QuoteIdent(s.Schema)
	case GrantTable:
		refs := make([]string, 0, len(s.Objects))
		for _, t := range s.Objects {
			refs = append(refs, QuoteIdent(s.Schema)+"."+QuoteIdent(t))
		}
		return "TABLE " + strings.Join(refs, ", ")
	default: // GrantDatabase
		return "DATABASE " + QuoteIdent(s.Database)
	}
}

// privilegeCSV renders a privilege list for a GRANT/REVOKE, mapping ALL to the
// canonical `ALL PRIVILEGES`. Empty defaults to `ALL PRIVILEGES`.
func privilegeCSV(privs []string) string {
	if len(privs) == 0 {
		return "ALL PRIVILEGES"
	}
	out := make([]string, 0, len(privs))
	for _, p := range privs {
		u := strings.ToUpper(strings.TrimSpace(p))
		if u == "ALL" || u == "ALL PRIVILEGES" {
			return "ALL PRIVILEGES"
		}
		out = append(out, u)
	}
	return strings.Join(out, ", ")
}

// GrantSQL renders `GRANT <privs> ON <object> TO "role"`.
func GrantSQL(s GrantSpec) string {
	return "GRANT " + privilegeCSV(s.Privileges) + " ON " + s.objectClause() + " TO " + QuoteIdent(s.Role)
}

// RevokeSQL renders `REVOKE <privs> ON <object> FROM "role"`.
func RevokeSQL(s GrantSpec, privs []string) string {
	return "REVOKE " + privilegeCSV(privs) + " ON " + s.objectClause() + " FROM " + QuoteIdent(s.Role)
}

// ReadGrantSQL selects the concrete privilege types the role holds on the object,
// for read-back. Database/schema grants read the object ACL via aclexplode;
// table/all_tables read information_schema.role_table_grants (the union over the
// schema's tables, narrowed to the declared tables for the `table` type).
func ReadGrantSQL(s GrantSpec) string {
	switch s.ObjectType {
	case GrantSchema:
		return "SELECT acl.privilege_type FROM pg_namespace n, aclexplode(n.nspacl) AS acl " +
			"JOIN pg_roles r ON r.oid = acl.grantee " +
			"WHERE n.nspname = " + QuoteLiteral(s.Schema) + " AND r.rolname = " + QuoteLiteral(s.Role)
	case GrantAllTables:
		return "SELECT DISTINCT privilege_type FROM information_schema.role_table_grants " +
			"WHERE grantee = " + QuoteLiteral(s.Role) + " AND table_schema = " + QuoteLiteral(s.Schema)
	case GrantTable:
		q := "SELECT DISTINCT privilege_type FROM information_schema.role_table_grants " +
			"WHERE grantee = " + QuoteLiteral(s.Role) + " AND table_schema = " + QuoteLiteral(s.Schema)
		if len(s.Objects) > 0 {
			lits := make([]string, 0, len(s.Objects))
			for _, t := range s.Objects {
				lits = append(lits, QuoteLiteral(t))
			}
			q += " AND table_name IN (" + strings.Join(lits, ", ") + ")"
		}
		return q
	default: // GrantDatabase
		return "SELECT acl.privilege_type FROM pg_database d, aclexplode(d.datacl) AS acl " +
			"JOIN pg_roles r ON r.oid = acl.grantee " +
			"WHERE d.datname = " + QuoteLiteral(s.Database) + " AND r.rolname = " + QuoteLiteral(s.Role)
	}
}

// ParseGrantPrivileges parses newline-separated privilege_type rows from
// ReadGrantSQL into a sorted, upper-cased, de-duplicated set.
func ParseGrantPrivileges(out string) []string {
	seen := map[string]bool{}
	var privs []string
	for _, line := range strings.Split(out, "\n") {
		p := strings.ToUpper(strings.TrimSpace(line))
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		privs = append(privs, p)
	}
	sort.Strings(privs)
	return privs
}

// ReconcileGrantPrivileges maps the observed concrete privileges into the value
// to store in state so a re-plan is 0-diff: when the observed set exactly covers
// the object type's full privilege set and the prior declaration used ALL (or is
// empty, e.g. a fresh import), it collapses back to ["ALL"]; otherwise it returns
// the sorted observed set. An empty observed set (no grant) yields nil.
func ReconcileGrantPrivileges(objectType string, prior, observed []string) []string {
	norm := ParseGrantPrivileges(strings.Join(observed, "\n"))
	if len(norm) == 0 {
		return nil
	}
	priorAll := len(prior) == 0
	for _, p := range prior {
		if strings.EqualFold(strings.TrimSpace(p), "ALL") {
			priorAll = true
		}
	}
	if priorAll && stringSetEqual(norm, allPrivilege[objectType]) {
		return []string{"ALL"}
	}
	return norm
}

// stringSetEqual reports whether two already-sorted string slices are equal.
func stringSetEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
