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

// DefaultWalKeepSize is the WAL floor a streaming primary retains when the caller
// declares none — enough headroom for a base-backup + a briefly-lagging standby
// on a small cluster, without a replication slot.
const DefaultWalKeepSize = "512MB"

// StreamingPrimaryParams configures the primary side of plain streaming
// replication.
type StreamingPrimaryParams struct {
	Version             string
	Cluster             string
	MaxWalSenders       int      // 0 → PostgreSQL default (10)
	WalKeepSize         string   // e.g. "512MB"; empty → DefaultWalKeepSize
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
	// Always retain a WAL floor. With wal_keep_size=0 (the PostgreSQL default) the
	// primary can recycle a segment the standby still needs — during the initial
	// pg_basebackup (→ "requested WAL segment … has already been removed") or while
	// a lagging standby catches up. A non-zero floor makes streaming bring-up work
	// without a replication slot; a slot (finer-grained retention) is a future knob.
	walKeep := strings.TrimSpace(p.WalKeepSize)
	if walKeep == "" {
		walKeep = DefaultWalKeepSize
	}
	fmt.Fprintf(&b, "wal_keep_size = '%s'\n", walKeep)

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
	// The primary reach + replication credential, used to write a ~postgres/.pgpass
	// so the walreceiver authenticates even if pg_basebackup -R omits the password
	// from primary_conninfo. Empty PrimaryHost/User → no .pgpass is written.
	PrimaryHost         string
	PrimaryPort         int
	ReplicationUser     string
	ReplicationPassword string
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
	// The fresh package install already initialized a standalone cluster in the
	// datadir, and pg_basebackup requires an EMPTY target — so on a node that is
	// not already a standby OF THIS PRIMARY, wipe the local cluster and clone from
	// the primary. pg_basebackup -R writes primary_conninfo + standby.signal. The
	// idempotency guard keys on primary_conninfo referencing the primary host in
	// postgresql.auto.conf — the only state that is true exclusively after a real
	// clone. It deliberately does NOT key on PG_VERSION (every initialized datadir
	// has it — that is what made the old guard skip the clone on first apply) nor
	// on standby.signal alone (a half-brought-up standalone can carry a stale
	// signal with an empty primary_conninfo); so this both converges a fresh node
	// and self-heals a previously-botched standby, while a real re-apply is a no-op.
	clone := fmt.Sprintf(
		"if ! grep -qs %s %s/postgresql.auto.conf; then find %s -mindepth 1 -delete && su postgres -c %s; fi",
		shQuote("host="+p.PrimaryHost), shQuote(dataDir), shQuote(dataDir), shQuote(baseBackup))
	cmds := []Command{
		{Label: "streaming standby stop", Cmd: fmt.Sprintf("pg_ctlcluster %s %s stop || true", p.Version, p.Cluster)},
	}
	// A ~postgres/.pgpass guarantees the walreceiver can authenticate even when
	// pg_basebackup -R leaves the password out of primary_conninfo. The secret is
	// fed on stdin, never argv. Written before the clone so it is in place the
	// moment the standby starts streaming.
	if p.PrimaryHost != "" && p.ReplicationUser != "" && p.ReplicationPassword != "" {
		port := p.PrimaryPort
		if port == 0 {
			port = DefaultPGPort
		}
		pgpass := fmt.Sprintf("%s:%d:replication:%s:%s\n%s:%d:*:%s:%s\n",
			p.PrimaryHost, port, p.ReplicationUser, p.ReplicationPassword,
			p.PrimaryHost, port, p.ReplicationUser, p.ReplicationPassword)
		cmds = append(cmds, Command{
			Label: "streaming standby pgpass",
			Cmd:   "install -d -o postgres -g postgres -m 700 ~postgres && cat > ~postgres/.pgpass && chown postgres:postgres ~postgres/.pgpass && chmod 600 ~postgres/.pgpass",
			Stdin: []byte(pgpass),
		})
	}
	cmds = append(cmds,
		Command{Label: "streaming standby clone", Cmd: clone},
		Command{Label: "streaming standby start", Cmd: fmt.Sprintf("pg_ctlcluster %s %s start", p.Version, p.Cluster)},
	)
	return cmds
}

// shQuoteSQL double-quotes a SQL string for nesting inside an already
// single-quoted `su postgres -c '...'` wrapper.
func shQuoteSQL(s string) string {
	return "\"" + strings.ReplaceAll(s, "\"", "\\\"") + "\""
}
