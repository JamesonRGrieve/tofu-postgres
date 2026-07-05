// SPDX-License-Identifier: AGPL-3.0-or-later

package postgres

import (
	"fmt"
	"strings"
)

// pg_hba.conf managed-block markers. The tofu-owned lines live between them so
// the provider can rewrite exactly its own block on every apply and leave the
// packaged/operator lines (local peer for postgres, etc.) untouched.
const (
	HBABeginMarker = "# BEGIN tofu-managed — do not edit"
	HBAEndMarker   = "# END tofu-managed"
)

// DefaultHBAMethod is applied to any pg_hba entry that leaves method unset.
// scram-sha-256 is the house default (md5 is deprecated).
const DefaultHBAMethod = "scram-sha-256"

// HBAEntry is one pg_hba.conf access rule. Address is empty for `local`
// (Unix-socket) rules and a CIDR/host for `host*` rules.
type HBAEntry struct {
	Type     string // local | host | hostssl | hostnossl
	Database string // e.g. all, a database name, replication
	User     string // e.g. all, postgres, an application role
	Address  string // CIDR/host for host* rules; empty for local
	Method   string // auth method; DefaultHBAMethod when empty
}

// RenderPgHba renders the marker-delimited managed block. Columns are padded to
// the classic pg_hba widths for readability; an empty address (local rules)
// collapses so the method still aligns.
func RenderPgHba(entries []HBAEntry) string {
	var b strings.Builder
	b.WriteString(HBABeginMarker + "\n")
	for _, e := range entries {
		method := e.Method
		if strings.TrimSpace(method) == "" {
			method = DefaultHBAMethod
		}
		if strings.EqualFold(e.Type, "local") || strings.TrimSpace(e.Address) == "" {
			fmt.Fprintf(&b, "%-8s %-15s %-15s %s\n", e.Type, e.Database, e.User, method)
		} else {
			fmt.Fprintf(&b, "%-8s %-15s %-15s %-23s %s\n", e.Type, e.Database, e.User, e.Address, method)
		}
	}
	b.WriteString(HBAEndMarker + "\n")
	return b.String()
}

// ParseHBABlock parses the managed block out of a full pg_hba.conf (or just the
// block) back into entries for read-back. Only lines between the markers are
// considered; comment/blank lines inside are skipped. A `local` rule (4 fields)
// has no address; `host*` rules (5 fields) do.
func ParseHBABlock(content string) []HBAEntry {
	var entries []HBAEntry
	inBlock := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, HBABeginMarker) {
			inBlock = true
			continue
		}
		if strings.Contains(trimmed, HBAEndMarker) {
			break
		}
		if !inBlock || trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		f := strings.Fields(trimmed)
		switch {
		case strings.EqualFold(f[0], "local") && len(f) >= 4:
			entries = append(entries, HBAEntry{Type: f[0], Database: f[1], User: f[2], Method: f[3]})
		case len(f) >= 5:
			entries = append(entries, HBAEntry{Type: f[0], Database: f[1], User: f[2], Address: f[3], Method: f[4]})
		}
	}
	return entries
}

// PgHbaReassembleCommand builds the shell command that strips any prior managed
// block from pg_hba.conf and appends the new block (fed on stdin). Reading the
// block from stdin keeps the rendered content out of the process argument list.
// The awk filter drops everything between the markers (inclusive); the new block
// is then appended verbatim.
func PgHbaReassembleCommand(hbaPath string) string {
	tmp := hbaPath + ".tofu.tmp"
	return fmt.Sprintf(
		"BLOCK=\"$(cat)\"; "+
			"awk 'f{if($0 ~ /%s/){f=0}; next} $0 ~ /%s/{f=1; next} {print}' %s > %s && "+
			"printf '%%s\\n' \"$BLOCK\" >> %s && mv %s %s",
		HBAEndMarker, HBABeginMarker,
		shQuote(hbaPath), shQuote(tmp), shQuote(tmp), shQuote(tmp), shQuote(hbaPath),
	)
}

// shQuote single-quotes a value for safe use in a remote shell command.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
