# SPDX-License-Identifier: AGPL-3.0-or-later
# postgresql.conf keys (via a conf.d drop-in) + the tofu-owned pg_hba block.
# Only declared keys are managed. wal_init_zero/wal_recycle are turned off for a
# ZFS/COW datadir. Import:  tofu import postgres_config.pg 16/main

resource "postgres_config" "pg" {
  version = "16"
  cluster = "main"

  shared_buffers       = "256MB"
  effective_cache_size = "1GB"
  work_mem             = "16MB"
  maintenance_work_mem = "256MB"
  max_connections      = 100
  listen_addresses     = "*"
  password_encryption  = "scram-sha-256"

  # ZFS/COW tuning (see DESIGN.md).
  wal_init_zero = false
  wal_recycle   = false

  pg_hba = [
    { type = "local", database = "all", user = "postgres", method = "peer" },
    { type = "host", database = "all", user = "all", address = "127.0.0.1/32", method = "scram-sha-256" },
    { type = "host", database = "all", user = "all", address = "10.0.0.0/24", method = "scram-sha-256" },
    { type = "host", database = "replication", user = "replicator", address = "10.0.0.0/24", method = "scram-sha-256" },
  ]
}
