# SPDX-License-Identifier: AGPL-3.0-or-later
# repmgr-managed replication with automatic failover (repmgrd).
# The primary registers itself; each standby clones from the primary and
# registers. See DESIGN.md for the repmgr bring-up sequence and risk notes.

resource "postgres_cluster" "repmgr" {
  name    = "pg-repmgr"
  ha_mode = "repmgr"
}

resource "postgres_cluster_node" "repmgr_primary" {
  cluster          = postgres_cluster.repmgr.name
  ha_mode          = "repmgr"
  version          = "16"
  node_name        = "node1"
  node_id          = 1
  host             = "10.0.0.20"
  role             = "primary"
  replication_user = "repmgr"
}

resource "postgres_cluster_node" "repmgr_standby" {
  cluster      = postgres_cluster.repmgr.name
  ha_mode      = "repmgr"
  version      = "16"
  node_name    = "node2"
  node_id      = 2
  host         = "10.0.0.21"
  role         = "replica"
  primary_host = "10.0.0.20"
}
