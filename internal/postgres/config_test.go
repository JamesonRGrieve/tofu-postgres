// SPDX-License-Identifier: AGPL-3.0-or-later

package postgres

import "testing"

func TestRenderConfD(t *testing.T) {
	got := RenderConfD([]ConfSetting{
		{Key: "shared_buffers", Value: "256MB", Quote: true},
		{Key: "max_connections", Value: "100", Quote: false},
		{Key: "wal_init_zero", Value: "off", Quote: false},
		{Key: "password_encryption", Value: "scram-sha-256", Quote: true},
	})
	want := ManagedHeader + "\n" +
		"shared_buffers = '256MB'\n" +
		"max_connections = 100\n" +
		"wal_init_zero = off\n" +
		"password_encryption = 'scram-sha-256'\n"
	if got != want {
		t.Fatalf("RenderConfD =\n%q\nwant\n%q", got, want)
	}
}

func TestNeedsRestart(t *testing.T) {
	cases := []struct {
		name string
		keys []string
		want bool
	}{
		{"reload-only", []string{"work_mem", "effective_cache_size", "wal_recycle"}, false},
		{"shared_buffers forces restart", []string{"work_mem", "shared_buffers"}, true},
		{"max_connections forces restart", []string{"max_connections"}, true},
		{"listen_addresses forces restart", []string{"listen_addresses"}, true},
		{"empty", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NeedsRestart(tc.keys); got != tc.want {
				t.Fatalf("NeedsRestart(%v) = %v, want %v", tc.keys, got, tc.want)
			}
		})
	}
}

func TestPaths(t *testing.T) {
	if got := ConfDropInPath("16", "main"); got != "/etc/postgresql/16/main/conf.d/99-tofu.conf" {
		t.Fatalf("ConfDropInPath = %q", got)
	}
	if got := HBAPath("16", "main"); got != "/etc/postgresql/16/main/pg_hba.conf" {
		t.Fatalf("HBAPath = %q", got)
	}
	if got := DataDir("15", "replica"); got != "/var/lib/postgresql/15/replica" {
		t.Fatalf("DataDir = %q", got)
	}
	if got := PackageName("16"); got != "postgresql-16" {
		t.Fatalf("PackageName = %q", got)
	}
	if got := ServiceUnit("16", "main"); got != "postgresql@16-main" {
		t.Fatalf("ServiceUnit = %q", got)
	}
}

func TestParseConfD(t *testing.T) {
	content := ManagedHeader + "\n" +
		"shared_buffers = '256MB'\n" +
		"max_connections = 100\n" +
		"# a comment\n" +
		"\n" +
		"password_encryption = 'scram-sha-256'\n"
	m := ParseConfD(content)
	if m["shared_buffers"] != "256MB" {
		t.Fatalf("shared_buffers = %q", m["shared_buffers"])
	}
	if m["max_connections"] != "100" {
		t.Fatalf("max_connections = %q", m["max_connections"])
	}
	if m["password_encryption"] != "scram-sha-256" {
		t.Fatalf("password_encryption = %q", m["password_encryption"])
	}
	if _, ok := m["# a comment"]; ok {
		t.Fatal("comment must not parse as a key")
	}
}
