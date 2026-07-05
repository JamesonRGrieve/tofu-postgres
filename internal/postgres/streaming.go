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
	Version       string
	Cluster       string
	MaxWalSenders int      // 0 → PostgreSQL default (10)
	WalKeepSize   string   // e.g. "512MB"; empty → omit
	Slots         []string // physical replication slots to create
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
	for _, slot := range p.Slots {
		// Idempotent slot create: skip when the slot already exists.
		sql := fmt.Sprintf(
			"SELECT pg_create_physical_replication_slot('%s') WHERE NOT EXISTS "+
				"(SELECT 1 FROM pg_replication_slots WHERE slot_name = '%s');", slot, slot)
		cmds = append(cmds, Command{
			Label: "streaming create slot " + slot,
			Cmd:   fmt.Sprintf("su postgres -c %s", shQuote(fmt.Sprintf("psql -p $(pg_lsclusters -h | awk '$1==\"%s\"&&$2==\"%s\"{print $3}') -tAc %s", p.Version, p.Cluster, shQuoteSQL(sql)))),
		})
	}
	return cmds
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
