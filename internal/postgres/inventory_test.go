// SPDX-License-Identifier: AGPL-3.0-or-later

package postgres

import "testing"

func TestParseDpkgVersion(t *testing.T) {
	if got := ParseDpkgVersion("  16+257\n"); got != "16+257" {
		t.Fatalf("ParseDpkgVersion = %q", got)
	}
	if got := ParseDpkgVersion(""); got != "" {
		t.Fatalf("empty should stay empty, got %q", got)
	}
}

func TestParsePgLsclusters(t *testing.T) {
	out := "Ver Cluster Port Status Owner    Data directory              Log file\n" +
		"16  main    5432 online postgres /var/lib/postgresql/16/main /var/log/postgresql/x.log\n" +
		"15  replica 5433 down   postgres /var/lib/postgresql/15/replica /var/log/postgresql/y.log\n" +
		"garbage\n"
	rows := ParsePgLsclusters(out)
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d: %#v", len(rows), rows)
	}
	if rows[0].Version != "16" || rows[0].Name != "main" || rows[0].Port != 5432 || rows[0].Status != "online" {
		t.Fatalf("row0 = %#v", rows[0])
	}
	if rows[1].Port != 5433 || rows[1].Status != "down" {
		t.Fatalf("row1 = %#v", rows[1])
	}

	if c := FindCluster(rows, "16", "main"); c == nil || c.Port != 5432 {
		t.Fatalf("FindCluster(16,main) = %#v", c)
	}
	if c := FindCluster(rows, "16", "nope"); c != nil {
		t.Fatalf("FindCluster miss should be nil, got %#v", c)
	}
}
