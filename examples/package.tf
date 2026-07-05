# SPDX-License-Identifier: AGPL-3.0-or-later
# Install PostgreSQL 16 and pin it against unattended upgrades.
# Import an existing install:  tofu import postgres_package.pg 16

resource "postgres_package" "pg" {
  version = "16"
  hold    = true
}

output "installed_version" {
  value = postgres_package.pg.state
}
