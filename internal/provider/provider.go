// SPDX-License-Identifier: AGPL-3.0-or-later

// Package provider implements the postgres OpenTofu/Terraform provider — native
// management of a PostgreSQL host's installed state, config files, service, and
// HA topology over an SSH/CLI transport, plus the logical objects it hosts
// (databases, roles, grants) driven through psql as the postgres superuser over
// the same transport. Logical-object management is natively owned here — no
// dependency on the community cyrilgdn/postgresql provider.
package provider

import (
	"context"
	"time"

	"github.com/JamesonRGrieve/tofu-postgres/internal/postgres"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ provider.Provider = (*postgresProvider)(nil)

// New returns the provider factory for a given version.
func New(version string) func() provider.Provider {
	return func() provider.Provider { return &postgresProvider{version: version} }
}

type postgresProvider struct {
	version string
}

type providerModel struct {
	SSHHost        types.String `tfsdk:"ssh_host"`
	SSHPort        types.Int64  `tfsdk:"ssh_port"`
	SSHUser        types.String `tfsdk:"ssh_user"`
	SSHKeyFile     types.String `tfsdk:"ssh_key_file"`
	SSHKeyPEM      types.String `tfsdk:"ssh_key_pem"`
	TimeoutSeconds types.Int64  `tfsdk:"timeout_seconds"`
}

func (p *postgresProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	// Single-token type name → resources are `postgres_package` etc. (Terraform's
	// prefix-before-first-underscore inference resolves the local name); the
	// source address is still jamesonrgrieve/postgres.
	resp.TypeName = "postgres"
	resp.Version = p.version
}

func (p *postgresProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Native provider for a PostgreSQL host's installed state, config files, service, and " +
			"HA topology, driven over SSH/CLI (PostgreSQL exposes no management REST API). Logical objects " +
			"(databases, roles, grants) are natively owned too — no external provider dependency.",
		Attributes: map[string]schema.Attribute{
			"ssh_host": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "SSH address (host or host:port, no scheme) of the PostgreSQL host. All resources " +
					"drive their CLI over this transport. A relay/jump endpoint can differ from the DB's service address.",
			},
			"ssh_port": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "SSH port (default: `ssh_host`'s `:port`, else 22).",
			},
			"ssh_user": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "SSH login user (default `root`).",
			},
			"ssh_key_file": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Path to an SSH identity file (`ssh -i`). When unset and `ssh_key_pem` is empty, ssh_config/agent is used. Key/cert auth only — never a password.",
			},
			"ssh_key_pem": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				MarkdownDescription: "SSH private-key material (e.g. an OpenBao-signed key from `TF_VAR_*`). Written to a " +
					"temp 0600 file per call and removed after; never persisted to state.",
			},
			"timeout_seconds": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "Per-command SSH timeout in seconds (default 45). Raise it for slow operations (a base-backup clone).",
			},
		},
	}
}

func (p *postgresProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var timeout time.Duration
	if !cfg.TimeoutSeconds.IsNull() && !cfg.TimeoutSeconds.IsUnknown() && cfg.TimeoutSeconds.ValueInt64() > 0 {
		timeout = time.Duration(cfg.TimeoutSeconds.ValueInt64()) * time.Second
	}
	client := &postgres.Client{}
	if !cfg.SSHHost.IsNull() && cfg.SSHHost.ValueString() != "" {
		client.SSH = postgres.NewSSHClient(postgres.SSHConfig{
			Host:    cfg.SSHHost.ValueString(),
			Port:    int(cfg.SSHPort.ValueInt64()),
			User:    cfg.SSHUser.ValueString(),
			KeyFile: cfg.SSHKeyFile.ValueString(),
			KeyPEM:  cfg.SSHKeyPEM.ValueString(),
			Timeout: timeout,
		})
	}
	resp.ResourceData = client
	resp.DataSourceData = client
}

func (p *postgresProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewPackageResource,
		NewConfigResource,
		NewServiceResource,
		NewClusterResource,
		NewClusterNodeResource,
		NewDatabaseResource,
		NewRoleResource,
		NewGrantResource,
	}
}

func (p *postgresProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return nil
}
