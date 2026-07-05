// SPDX-License-Identifier: AGPL-3.0-or-later

package provider

import (
	"strings"
	"testing"

	"github.com/JamesonRGrieve/tofu-postgres/internal/postgres"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// recorder is an injectable RunFunc that records every command it receives, so
// resource apply logic is verified hermetically (no live host).
type recorder struct {
	cmds  []string
	stdin [][]byte
	reply map[string][]byte
	err   error
}

func (r *recorder) run(cmd string, stdin []byte) ([]byte, error) {
	r.cmds = append(r.cmds, cmd)
	r.stdin = append(r.stdin, stdin)
	if r.err != nil {
		return nil, r.err
	}
	if r.reply != nil {
		for k, v := range r.reply {
			if strings.Contains(cmd, k) {
				return v, nil
			}
		}
	}
	return nil, nil
}

func (r *recorder) fn() postgres.RunFunc { return r.run }

func (r *recorder) joined() string { return strings.Join(r.cmds, "\n") }

func TestPackageCommands(t *testing.T) {
	held := packageCommands("16", true)
	if !strings.Contains(held[0].Cmd, "apt-get install -y -qq postgresql-16") {
		t.Fatalf("install cmd = %q", held[0].Cmd)
	}
	if !strings.Contains(held[1].Cmd, "apt-mark hold postgresql-16") {
		t.Fatalf("hold cmd = %q", held[1].Cmd)
	}
	unheld := packageCommands("15", false)
	if !strings.Contains(unheld[1].Cmd, "apt-mark unhold postgresql-15") {
		t.Fatalf("unhold cmd = %q", unheld[1].Cmd)
	}
}

func TestPackageApplyInjectedExec(t *testing.T) {
	rec := &recorder{}
	r := &packageResource{}
	m := packageModel{Version: types.StringValue("16"), Hold: types.BoolValue(true)}
	if err := r.apply(m, rec.fn()); err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if len(rec.cmds) != 2 {
		t.Fatalf("want 2 recorded commands, got %d: %v", len(rec.cmds), rec.cmds)
	}
	if !strings.Contains(rec.joined(), "postgresql-16") {
		t.Fatalf("recorded commands missing package: %s", rec.joined())
	}
}

func TestPackageFinishParsesDpkg(t *testing.T) {
	rec := &recorder{reply: map[string][]byte{"dpkg-query": []byte("16+257\n")}}
	r := &packageResource{}
	m := packageModel{Version: types.StringValue("16")}
	r.finish(&m, rec.fn())
	if m.State.ValueString() != "16+257" {
		t.Fatalf("state = %q", m.State.ValueString())
	}
	if m.ID.ValueString() != "16" {
		t.Fatalf("id = %q", m.ID.ValueString())
	}
}
