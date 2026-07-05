# SPDX-License-Identifier: AGPL-3.0-or-later
# Natively own a logical database (no cyrilgdn/postgresql dependency). Encoding
# and locale are fixed at creation; only the owner is mutable in place.
# Import:  tofu import postgres_database.app appdb

resource "postgres_database" "app" {
  name     = "appdb"
  owner    = postgres_role.app.name
  encoding = "UTF8"
}
