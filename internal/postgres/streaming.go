// SPDX-License-Identifier: AGPL-3.0-or-later

package postgres

import (
	"fmt"
	"strings"
)

// HADropInPath is the tofu-owned HA config drop-in (sorts before 99-tofu.conf
// so an explicit postgres_config value still wins over the HA defaults).
func HADropInPath(version, cluster string) string {
	return ConfDropInDir(version, cluster) + "/95-tofu-ha.conf"
}

// StreamingPrimaryParams configures the primary side of plain streaming
// replication.
type StreamingPrimaryParams struct {
	Version             string
	Cluster             string
	MaxWalSenders       int      // 0 → PostgreSQL default (10)
	WalKeepSize         string   // e.g. "512MB"; empty → omit
	Slots               []string // physical replication slots to create
	ReplicationUser     string   // the LOGIN REPLICATION role standbys connect as; empty → skip
	ReplicationPassword string   // its password (synced on every apply); empty → no password
}

// StreamingPrimaryCommands renders the primary bring-up: write the HA drop-in
// (wal_level=replica + sender/standby knobs), reload, and create each physical
// replication slot idempotently. Real bring-up is verification-owed; this is the
// deterministic command sequence the resource executes via an injected RunFunc.
func StreamingPrimaryCommands(p StreamingPrimaryParams) []Command {
	senders := p.MaxWalSenders
	if senders == 0 {
		senders = 10
	}
	var b strings.Builder
	b.WriteString(ManagedHeader + "\n")
	b.WriteString("wal_level = replica\n")
	fmt.Fprintf(&b, "max_wal_senders = %d\n", senders)
	fmt.Fprintf(&b, "max_replication_slots = %d\n", senders)
	b.WriteString("hot_standby = on\n")
	if strings.TrimSpace(p.WalKeepSize) != "" {
		fmt.Fprintf(&b, "wal_keep_size = '%s'\n", p.WalKeepSize)
	}

	cmds := []Command{{
		Label: "streaming primary ha config",
		Cmd:   fmt.Sprintf("mkdir -p %s && cat > %s", shQuote(ConfDropInDir(p.Version, p.Cluster)), shQuote(HADropInPath(p.Version, p.Cluster))),
		Stdin: []byte(b.String()),
	}, {
		Label: "streaming primary reload",
		Cmd:   fmt.Sprintf("pg_ctlcluster %s %s reload", p.Version, p.Cluster),
	}}
	// The standby's pg_basebackup connects as the replication role, so the primary
	// must own that role. Create-or-sync it idempotently over the local socket
	// (peer auth — no superuser password needed). This is HA plumbing (like the
	// physical slots below), not a user-facing logical role. The password travels
	// on stdin, never argv, so it never lands in the primary's process list.
	if strings.TrimSpace(p.ReplicationUser) != "" {
		cmds = append(cmds, Command{
			Label: "streaming primary replication role",
			Cmd:   fmt.Sprintf("su postgres -c %s", shQuote(primaryPsql(p.Version, p.Cluster, "-f -"))),
			Stdin: []byte(replicationRoleSQL(p.ReplicationUser, p.ReplicationPassword)),
		})
	}
	for _, slot := range p.Slots {
		// Idempotent slot create: skip when the slot already exists.
		sql := fmt.Sprintf(
			"SELECT pg_create_physical_replication_slot('%s') WHERE NOT EXISTS "+
				"(SELECT 1 FROM pg_replication_slots WHERE slot_name = '%s');", slot, slot)
		cmds = append(cmds, Command{
			Label: "streaming create slot " + slot,
			Cmd:   fmt.Sprintf("su postgres -c %s", shQuote(primaryPsql(p.Version, p.Cluster, "-tAc "+shQuoteSQL(sql)))),
		})
	}
	return cmds
}

// primaryPsql builds a `psql` invocation whose -p port is resolved at runtime
// from pg_lsclusters for the given version/cluster (the local cluster's port is
// not knowable at plan time). args are appended verbatim (already quoted).
func primaryPsql(version, cluster, args string) string {
	return fmt.Sprintf("psql -p $(pg_lsclusters -h | awk '$1==\"%s\"&&$2==\"%s\"{print $3}') %s", version, cluster, args)
}

// replicationRoleSQL renders idempotent create-or-sync DDL for the streaming
// replication role: CREATE it with LOGIN REPLICATION when absent, otherwise
// ALTER it so a rotated password propagates. The identifier and password are
// escaped for SQL (double-quoted ident, single-quoted literal); the SQL is fed
// on stdin so the password never appears in a process argument.
func replicationRoleSQL(user, password string) string {
	ident := `"` + strings.ReplaceAll(user, `"`, `""`) + `"`
	var pw string
	if password != "" {
		pw = " PASSWORD '" + strings.ReplaceAll(password, "'", "''") + "'"
	}
	return fmt.Sprintf(
		"DO $do$ BEGIN "+
			"IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = %s) THEN CREATE ROLE %s WITH LOGIN REPLICATION%s; "+
			"ELSE ALTER ROLE %s WITH LOGIN REPLICATION%s; END IF; END $do$;",
		"'"+strings.ReplaceAll(user, "'", "''")+"'", ident, pw, ident, pw)
}

// StreamingStandbyParams configures the standby side of plain streaming
// replication.
type StreamingStandbyParams struct {
	Version  string
	Cluster  string
	Conninfo string // primary_conninfo (from BuildPrimaryConninfo)
	Slot     string // primary slot the standby consumes
}

// StreamingStandbyCommands renders the standby bootstrap: stop the local
// cluster, pg_basebackup from the primary (writing primary_conninfo +
// standby.signal via -R), then start. The base-backup is guarded so a
// re-apply against an already-cloned standby is a no-op (the datadir keeps its
// PG_VERSION marker). pg_basebackup is interruption-unsafe — see DESIGN.md.
func StreamingStandbyCommands(p StreamingStandbyParams) []Command {
	dataDir := DataDir(p.Version, p.Cluster)
	baseBackup := fmt.Sprintf("pg_basebackup -D %s -d %s -R -X stream", shQuote(dataDir), shQuote(p.Conninfo))
	if p.Slot != "" {
		baseBackup += " --slot=" + shQuote(p.Slot)
	}
	// Guard: only clone when the datadir has no cluster yet.
	guarded := fmt.Sprintf("[ -f %s/PG_VERSION ] || su postgres -c %s", shQuote(dataDir), shQuote(baseBackup))
	return []Command{
		{Label: "streaming standby stop", Cmd: fmt.Sprintf("pg_ctlcluster %s %s stop || true", p.Version, p.Cluster)},
		{Label: "streaming standby basebackup", Cmd: guarded},
		{Label: "streaming standby signal", Cmd: fmt.Sprintf("touch %s/standby.signal && chown postgres:postgres %s/standby.signal", shQuote(dataDir), shQuote(dataDir))},
		{Label: "streaming standby start", Cmd: fmt.Sprintf("pg_ctlcluster %s %s start", p.Version, p.Cluster)},
	}
}

// shQuoteSQL double-quotes a SQL string for nesting inside an already
// single-quoted `su postgres -c '...'` wrapper.
func shQuoteSQL(s string) string {
	return "\"" + strings.ReplaceAll(s, "\"", "\\\"") + "\""
}
