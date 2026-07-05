# SPDX-License-Identifier: AGPL-3.0-or-later
# Natively own a role's privileges on a database object. Each grant converges to
# exactly its declared privilege set. Import:  role:database:object_type
# e.g.  tofu import postgres_grant.app_connect app:appdb:database

# CONNECT on the database itself.
resource "postgres_grant" "app_connect" {
  role        = postgres_role.app.name
  database    = postgres_database.app.name
  object_type = "database"
  privileges  = ["CONNECT"]
}

# USAGE on the public schema.
resource "postgres_grant" "app_schema" {
  role        = postgres_role.app.name
  database    = postgres_database.app.name
  object_type = "schema"
  schema      = "public"
  privileges  = ["USAGE"]
}

# All privileges on every existing table in the schema.
resource "postgres_grant" "app_tables" {
  role        = postgres_role.app.name
  database    = postgres_database.app.name
  object_type = "all_tables"
  schema      = "public"
  privileges  = ["ALL"]
}
