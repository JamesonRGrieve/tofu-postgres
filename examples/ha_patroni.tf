# SPDX-License-Identifier: AGPL-3.0-or-later
# Patroni-managed HA against an etcd DCS. Patroni self-elects the leader; each
# node ships the same config and coordinates through the DCS. See DESIGN.md.

variable "repl_password" {
  type      = string
  sensitive = true
}

variable "super_password" {
  type      = string
  sensitive = true
}

resource "postgres_cluster" "patroni" {
  name          = "pgcluster"
  ha_mode       = "patroni"
  dcs_reference = "10.0.0.10:2379,10.0.0.11:2379,10.0.0.12:2379"
  synchronous   = true
}

resource "postgres_cluster_node" "patroni_node1" {
  cluster                = postgres_cluster.patroni.name
  ha_mode                = "patroni"
  version                = "16"
  node_name              = "node1"
  host                   = "10.0.0.20"
  role                   = "primary"
  dcs_reference          = postgres_cluster.patroni.dcs_reference
  rest_api_connect       = "10.0.0.20:8008"
  pg_connect             = "10.0.0.20:5432"
  is_synchronous_standby = true
  replication_user       = "replicator"
  replication_password   = var.repl_password
  super_user             = "postgres"
  super_password         = var.super_password
}
