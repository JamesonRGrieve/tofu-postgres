# SPDX-License-Identifier: AGPL-3.0-or-later
# Manage the per-cluster systemd unit. A change to restart_triggers forces a
# restart (e.g. after the config drop-in changes a postmaster-context key).
# Import:  tofu import postgres_service.pg 16/main

resource "postgres_service" "pg" {
  version = "16"
  cluster = "main"
  enabled = true
  state   = "started"

  restart_triggers = {
    config = postgres_config.pg.id
  }
}
