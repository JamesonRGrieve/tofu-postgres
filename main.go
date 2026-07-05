// SPDX-License-Identifier: AGPL-3.0-or-later

// Command tofu-postgres is the OpenTofu/Terraform provider plugin entrypoint for
// managing PostgreSQL installed state, config files, service, and HA over an
// SSH/CLI transport. Logical DB/role/grant CRUD is out of scope (compose from
// cyrilgdn/postgresql at the consumer layer).
package main

import (
	"context"
	"flag"
	"log"

	"github.com/JamesonRGrieve/tofu-postgres/internal/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
)

// version is overridden at build time via -ldflags.
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run with support for debuggers like delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/jamesonrgrieve/postgres",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err.Error())
	}
}
