# SPDX-License-Identifier: AGPL-3.0-or-later
# Provider configuration. The SSH transport is the only path (PostgreSQL has no
# management REST API). Credentials are injected at apply from the secret store
# via TF_VAR_* — never hard-coded here.

terraform {
  required_providers {
    postgres = {
      source  = "jamesonrgrieve/postgres"
      version = "~> 0.1"
    }
  }
}

variable "ssh_key_pem" {
  type      = string
  sensitive = true
}

provider "postgres" {
  ssh_host    = "10.0.0.20"
  ssh_user    = "root"
  ssh_key_pem = var.ssh_key_pem # e.g. an OpenBao-signed key
  # timeout_seconds = 120        # raise for slow ops (a base-backup clone)
}
