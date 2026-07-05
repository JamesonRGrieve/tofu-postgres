// SPDX-License-Identifier: AGPL-3.0-or-later

package provider

import (
	"strings"
	"testing"
)

func TestBuildServiceCommands(t *testing.T) {
	// started, enabled, no restart → enable + start
	cmds := buildServiceCommands("postgresql@16-main", true, "started", false)
	if !strings.Contains(cmds[0].Cmd, "systemctl enable postgresql@16-main") {
		t.Fatalf("enable = %q", cmds[0].Cmd)
	}
	if !strings.Contains(cmds[1].Cmd, "systemctl start postgresql@16-main") {
		t.Fatalf("start = %q", cmds[1].Cmd)
	}

	// started, enabled, restart → enable + restart
	restart := buildServiceCommands("postgresql@16-main", true, "started", true)
	if !strings.Contains(restart[1].Cmd, "systemctl restart postgresql@16-main") {
		t.Fatalf("restart = %q", restart[1].Cmd)
	}

	// stopped, disabled → disable + stop
	stopped := buildServiceCommands("postgresql@16-main", false, "stopped", false)
	if !strings.Contains(stopped[0].Cmd, "systemctl disable") {
		t.Fatalf("disable = %q", stopped[0].Cmd)
	}
	if !strings.Contains(stopped[1].Cmd, "systemctl stop") {
		t.Fatalf("stop = %q", stopped[1].Cmd)
	}
}
