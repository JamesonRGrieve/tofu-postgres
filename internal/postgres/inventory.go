// SPDX-License-Identifier: AGPL-3.0-or-later

package postgres

import (
	"strconv"
	"strings"
)

// ParseDpkgVersion trims the raw `dpkg-query -W -f='${Version}'` output. An
// empty result means the package is not installed.
func ParseDpkgVersion(out string) string {
	return strings.TrimSpace(string(out))
}

// LsCluster is one row of `pg_lsclusters` output.
type LsCluster struct {
	Version string
	Name    string
	Port    int
	Status  string
}

// ParsePgLsclusters parses `pg_lsclusters --no-header` (or full) output into
// rows. The header line (starting "Ver") is skipped when present. Malformed
// short lines are ignored rather than erroring — a partial inventory is more
// useful than none.
func ParsePgLsclusters(out string) []LsCluster {
	var rows []LsCluster
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		if f[0] == "Ver" { // header
			continue
		}
		port, err := strconv.Atoi(f[2])
		if err != nil {
			continue
		}
		rows = append(rows, LsCluster{Version: f[0], Name: f[1], Port: port, Status: f[3]})
	}
	return rows
}

// FindCluster returns the row matching (version, name), or nil when absent.
func FindCluster(rows []LsCluster, version, name string) *LsCluster {
	for i := range rows {
		if rows[i].Version == version && rows[i].Name == name {
			return &rows[i]
		}
	}
	return nil
}
