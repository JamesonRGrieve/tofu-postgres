// SPDX-License-Identifier: AGPL-3.0-or-later

package postgres

import (
	"errors"
	"testing"
)

func TestRunCommandsExecutesInOrder(t *testing.T) {
	var seen []string
	run := func(cmd string, stdin []byte) ([]byte, error) {
		seen = append(seen, cmd)
		return nil, nil
	}
	cmds := []Command{{Label: "a", Cmd: "one"}, {Label: "b", Cmd: "two"}}
	if err := RunCommands(cmds, run); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(seen) != 2 || seen[0] != "one" || seen[1] != "two" {
		t.Fatalf("commands ran out of order: %v", seen)
	}
}

func TestRunCommandsStopsAtFirstError(t *testing.T) {
	var count int
	sentinel := errors.New("boom")
	run := func(cmd string, stdin []byte) ([]byte, error) {
		count++
		if count == 2 {
			return nil, sentinel
		}
		return nil, nil
	}
	cmds := []Command{{Label: "a", Cmd: "1"}, {Label: "failing-step", Cmd: "2"}, {Label: "c", Cmd: "3"}}
	err := RunCommands(cmds, run)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("error should wrap sentinel: %v", err)
	}
	if count != 2 {
		t.Fatalf("should stop after the failing step, ran %d", count)
	}
}

func TestClientRunWithoutTransport(t *testing.T) {
	var c *Client
	if _, err := c.Run("echo hi", nil); err == nil {
		t.Fatal("nil client should error, not panic")
	}
	c2 := &Client{}
	if _, err := c2.Run("echo hi", nil); err == nil {
		t.Fatal("client without SSH should error")
	}
	if c2.User() != "" {
		t.Fatal("User() should be empty without transport")
	}
}
