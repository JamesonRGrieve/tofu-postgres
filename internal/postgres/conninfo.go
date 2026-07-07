// SPDX-License-Identifier: AGPL-3.0-or-later

package postgres

import (
	"fmt"
	"strings"
)

// DefaultPGPort is the canonical PostgreSQL TCP port.
const DefaultPGPort = 5432

// Conninfo describes how a standby reaches its primary. It renders to the
// libpq key/value string used for a standby's primary_conninfo (streaming),
// repmgr's conninfo, and Patroni's postgresql.connect fields.
type Conninfo struct {
	Host            string
	Port            int
	User            string
	Password        string
	DBName          string
	ApplicationName string
	SSLMode         string
}

// BuildPrimaryConninfo renders a libpq conninfo string in a stable key order,
// omitting empty fields. Port 0 defaults to DefaultPGPort. This is the exact
// value a standby writes to primary_conninfo for streaming replication.
func BuildPrimaryConninfo(c Conninfo) string {
	port := c.Port
	if port == 0 {
		port = DefaultPGPort
	}
	parts := []string{fmt.Sprintf("host=%s", c.Host), fmt.Sprintf("port=%d", port)}
	if c.User != "" {
		parts = append(parts, "user="+c.User)
	}
	if c.Password != "" {
		parts = append(parts, "password="+c.Password)
	}
	if c.DBName != "" {
		parts = append(parts, "dbname="+c.DBName)
	}
	if c.ApplicationName != "" {
		parts = append(parts, "application_name="+c.ApplicationName)
	}
	if c.SSLMode != "" {
		parts = append(parts, "sslmode="+c.SSLMode)
	}
	return strings.Join(parts, " ")
}

// pgpassCommand builds the Command that writes a ~postgres/.pgpass from the
// given lines (each already `host:port:database:user:password`). The secret
// travels on stdin, never argv, so it never lands in the host's process list;
// the file is created 0600 owned by postgres. Shared by the streaming standby
// and every repmgr node so the walreceiver / repmgr clone / node-to-node auth
// succeeds even when a conninfo omits the password.
func pgpassCommand(label string, lines ...string) Command {
	return Command{
		Label: label,
		Cmd: "install -d -o postgres -g postgres -m 700 ~postgres && cat > ~postgres/.pgpass && " +
			"chown postgres:postgres ~postgres/.pgpass && chmod 600 ~postgres/.pgpass",
		Stdin: []byte(strings.Join(lines, "\n") + "\n"),
	}
}
