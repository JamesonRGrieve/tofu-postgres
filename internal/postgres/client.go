// SPDX-License-Identifier: AGPL-3.0-or-later

package postgres

import "fmt"

// Client is the provider's handle to a PostgreSQL host. It carries only the SSH
// transport today (PostgreSQL has no management REST API), but is kept as a
// struct so an additional transport can be added without touching resources.
type Client struct {
	SSH *SSHClient
}

// RunFunc is the injectable command-executor seam. In production it is
// Client.Run (SSH); in tests a fake records the invocations so CRUD dispatch is
// verified hermetically, with no live host. Mirrors the reconcile_resource
// injected-`post` pattern in the sibling providers.
type RunFunc func(cmd string, stdin []byte) ([]byte, error)

// Run executes a single remote command over the SSH transport. It errors when
// the transport was not configured (ssh_host unset) so resources surface a
// clear "SSH not configured" diagnostic rather than a nil dereference.
func (c *Client) Run(cmd string, stdin []byte) ([]byte, error) {
	if c == nil || c.SSH == nil {
		return nil, fmt.Errorf("postgres: SSH transport not configured (set the provider's ssh_host + ssh_key_file or ssh_key_pem)")
	}
	return c.SSH.Run(cmd, stdin)
}

// User returns the SSH login user, or "" when no transport is configured.
func (c *Client) User() string {
	if c == nil || c.SSH == nil {
		return ""
	}
	return c.SSH.User()
}

// Command is one shell command to run on the host, with an optional stdin
// payload (a rendered config file) and a human label for diagnostics. Resource
// apply logic builds an ordered []Command with pure functions (unit-tested
// without a host) and then executes them via a RunFunc.
type Command struct {
	Label string
	Cmd   string
	Stdin []byte
}

// RunCommands executes an ordered command list via run, stopping at the first
// error and wrapping it with the failing command's label. Pure with respect to
// its run argument, so a fake run makes the whole sequence testable.
func RunCommands(cmds []Command, run RunFunc) error {
	for _, c := range cmds {
		if _, err := run(c.Cmd, c.Stdin); err != nil {
			return fmt.Errorf("%s: %w", c.Label, err)
		}
	}
	return nil
}
