// SPDX-License-Identifier: AGPL-3.0-or-later

package postgres

import (
	"strings"
	"testing"
)

func TestRenderPgHba(t *testing.T) {
	got := RenderPgHba([]HBAEntry{
		{Type: "local", Database: "all", User: "postgres", Method: "peer"},
		{Type: "host", Database: "all", User: "all", Address: "10.0.0.0/24"}, // method defaults
		{Type: "host", Database: "replication", User: "repl", Address: "10.0.0.5/32", Method: "scram-sha-256"},
	})
	want := HBABeginMarker + "\n" +
		"local    all             postgres        peer\n" +
		"host     all             all             10.0.0.0/24             scram-sha-256\n" +
		"host     replication     repl            10.0.0.5/32             scram-sha-256\n" +
		HBAEndMarker + "\n"
	if got != want {
		t.Fatalf("RenderPgHba =\n%q\nwant\n%q", got, want)
	}
}

func TestRenderPgHbaDefaultMethod(t *testing.T) {
	got := RenderPgHba([]HBAEntry{{Type: "host", Database: "all", User: "all", Address: "0.0.0.0/0"}})
	if !strings.Contains(got, DefaultHBAMethod) {
		t.Fatalf("default method %q not applied: %q", DefaultHBAMethod, got)
	}
}

func TestParseHBABlockRoundTrip(t *testing.T) {
	entries := []HBAEntry{
		{Type: "local", Database: "all", User: "postgres", Method: "peer"},
		{Type: "host", Database: "all", User: "all", Address: "10.0.0.0/24", Method: "scram-sha-256"},
	}
	// A full pg_hba.conf with unmanaged lines around the managed block.
	full := "# packaged default line\n" +
		"local   all   all   peer\n" +
		RenderPgHba(entries) +
		"# trailing unmanaged\n"
	got := ParseHBABlock(full)
	if len(got) != 2 {
		t.Fatalf("want 2 parsed entries, got %d: %#v", len(got), got)
	}
	if got[0] != entries[0] {
		t.Fatalf("local entry = %#v, want %#v", got[0], entries[0])
	}
	if got[1] != entries[1] {
		t.Fatalf("host entry = %#v, want %#v", got[1], entries[1])
	}
}

func TestPgHbaReassembleCommand(t *testing.T) {
	cmd := PgHbaReassembleCommand("/etc/postgresql/16/main/pg_hba.conf")
	for _, want := range []string{
		`BLOCK="$(cat)"`,
		HBABeginMarker,
		HBAEndMarker,
		"'/etc/postgresql/16/main/pg_hba.conf'",
		"'/etc/postgresql/16/main/pg_hba.conf.tofu.tmp'",
		"mv ",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("reassemble command missing %q in:\n%s", want, cmd)
		}
	}
}
