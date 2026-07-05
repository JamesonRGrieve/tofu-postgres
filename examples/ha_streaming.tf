# SPDX-License-Identifier: AGPL-3.0-or-later
# Plain physical streaming replication: a primary that offers a slot, and a
# standby that base-backups from it and follows via primary_conninfo.
# NOTE: base-backup/promotion are interruption-unsafe — drive through the
# sanctioned pipeline on a lab twin first (see DESIGN.md).

variable "repl_password" {
  type      = string
  sensitive = true
}

resource "postgres_cluster" "stream" {
  name        = "pg-stream"
  ha_mode     = "streaming"
  synchronous = false
}

resource "postgres_cluster_node" "primary" {
  cluster          = postgres_cluster.stream.name
  ha_mode          = "streaming"
  version          = "16"
  node_name        = "node1"
  host             = "10.0.0.20"
  role             = "primary"
  replication_slot = "node2"
}

# The standby node runs against a second host — instantiate this provider with a
# different ssh_host (alias) in a real config.
resource "postgres_cluster_node" "standby" {
  cluster              = postgres_cluster.stream.name
  ha_mode              = "streaming"
  version              = "16"
  node_name            = "node2"
  host                 = "10.0.0.21"
  role                 = "replica"
  primary_host         = "10.0.0.20"
  replication_user     = "replicator"
  replication_password = var.repl_password
  replication_slot     = "node2"
}
