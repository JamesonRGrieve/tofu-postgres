# SPDX-License-Identifier: AGPL-3.0-or-later
# Natively own a login role. The password is ephemeral (write-only): it is
# injected at apply from OpenBao via TF_VAR_* and never written to state, while
# password_ref records the path it came from for auditability.
# Import:  tofu import postgres_role.app app

variable "app_role_password" {
  type      = string
  sensitive = true
}

resource "postgres_role" "app" {
  name         = "app"
  login        = true
  createdb     = false
  password     = var.app_role_password # write-only, never stored in state
  password_ref = "openbao:kv/data/postgres/app#password"
}
