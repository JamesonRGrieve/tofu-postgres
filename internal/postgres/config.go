// SPDX-License-Identifier: AGPL-3.0-or-later

package postgres

import (
	"fmt"
	"strings"
)

// ManagedHeader marks every tofu-owned file so a human editing the box knows
// not to hand-edit it.
const ManagedHeader = "# Managed by OpenTofu — do not edit manually"

// ConfSetting is one postgresql.conf key/value. Quote is true for values that
// must be single-quoted (string literals, memory units like '256MB', address
// lists); false for bare integers and on/off booleans.
type ConfSetting struct {
	Key   string
	Value string
	Quote bool
}

// RenderConfD renders the conf.d drop-in file from an ordered setting list. The
// order is the caller's; keeping it stable is what makes the file byte-identical
// across applies (import-to-0-diff).
func RenderConfD(settings []ConfSetting) string {
	var b strings.Builder
	b.WriteString(ManagedHeader + "\n")
	for _, s := range settings {
		if s.Quote {
			fmt.Fprintf(&b, "%s = '%s'\n", s.Key, s.Value)
		} else {
			fmt.Fprintf(&b, "%s = %s\n", s.Key, s.Value)
		}
	}
	return b.String()
}

// ParseConfD parses a rendered conf.d drop-in back into a key→value map for
// read-back (import-to-0-diff). Surrounding single quotes are stripped so the
// value matches what the configuration declared. Comment and blank lines are
// ignored.
func ParseConfD(content string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.IndexByte(line, '=')
		if i < 0 {
			continue
		}
		key := strings.TrimSpace(line[:i])
		val := strings.TrimSpace(line[i+1:])
		val = strings.TrimSuffix(strings.TrimPrefix(val, "'"), "'")
		m[key] = val
	}
	return m
}

// restartRequiredKeys are postmaster-context settings: they only take effect on
// a full restart, never a reload. Declaring any of them makes an apply restart
// the cluster instead of reloading it.
var restartRequiredKeys = map[string]bool{
	"shared_buffers":   true,
	"max_connections":  true,
	"listen_addresses": true,
}

// NeedsRestart reports whether any of the declared keys is postmaster-context
// (restart-only). When false, a reload suffices.
func NeedsRestart(keys []string) bool {
	for _, k := range keys {
		if restartRequiredKeys[k] {
			return true
		}
	}
	return false
}
