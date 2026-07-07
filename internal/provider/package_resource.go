// SPDX-License-Identifier: AGPL-3.0-or-later

package provider

import (
	"context"
	"fmt"

	"github.com/JamesonRGrieve/tofu-postgres/internal/postgres"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// postgres_package installs a PostgreSQL major version via apt and optionally
// apt-mark holds it (pinning against unattended-upgrade bumps). Read resolves
// the installed version via dpkg. Import id is the PG major (e.g. "16").
var (
	_ resource.Resource                = (*packageResource)(nil)
	_ resource.ResourceWithConfigure   = (*packageResource)(nil)
	_ resource.ResourceWithImportState = (*packageResource)(nil)
)

// NewPackageResource constructs the postgres_package resource.
func NewPackageResource() resource.Resource { return &packageResource{} }

type packageResource struct {
	client *postgres.Client
}

type packageModel struct {
	ID      types.String `tfsdk:"id"`
	Version types.String `tfsdk:"version"`
	Hold    types.Bool   `tfsdk:"hold"`
	State   types.String `tfsdk:"state"`
}

func (r *packageResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_package"
}

func (r *packageResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Installs a PostgreSQL major version (`apt-get install postgresql-<major>`) and " +
			"optionally `apt-mark hold`s it. Read resolves the installed version from dpkg.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Resource id — the PostgreSQL major version.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"version": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "PostgreSQL major version to install (e.g. `16`). Selects the `postgresql-<major>` apt package.",
			},
			"hold": schema.BoolAttribute{
				Optional:            true,
				MarkdownDescription: "When true, `apt-mark hold` the package so unattended upgrades cannot bump it; false unholds. Default false.",
			},
			"state": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Installed package version as reported by dpkg (empty when not installed).",
			},
		},
	}
}

func (r *packageResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req, resp)
}

// aptGet is the shared apt-get invocation prefix (non-interactive frontend + a
// bounded wait for the dpkg lock). It is defined once in the transport/pure
// layer (postgres.AptGet) so the package resource and the HA package installs
// use identical semantics; see that constant for the rationale.
const aptGet = postgres.AptGet

// packageCommands is the pure command builder (unit-tested with an injected
// RunFunc). It installs the package, then holds or unholds it.
func packageCommands(version string, hold bool) []postgres.Command {
	pkg := postgres.PackageName(version)
	cmds := []postgres.Command{{
		Label: "apt install " + pkg,
		Cmd: aptGet + " update -qq && " +
			aptGet + " install -y -qq " + pkg,
	}}
	if hold {
		cmds = append(cmds, postgres.Command{Label: "apt-mark hold " + pkg, Cmd: "apt-mark hold " + pkg})
	} else {
		cmds = append(cmds, postgres.Command{Label: "apt-mark unhold " + pkg, Cmd: "apt-mark unhold " + pkg + " 2>/dev/null || true"})
	}
	return cmds
}

func (r *packageResource) apply(m packageModel, run postgres.RunFunc) error {
	return postgres.RunCommands(packageCommands(m.Version.ValueString(), m.Hold.ValueBool()), run)
}

func (r *packageResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m packageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	run := runFunc(r.client, &resp.Diagnostics, "postgres_package")
	if run == nil {
		return
	}
	if err := r.apply(m, run); err != nil {
		resp.Diagnostics.AddError("postgres_package apply failed", err.Error())
		return
	}
	r.finish(&m, run)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *packageResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m packageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	run := runFunc(r.client, &resp.Diagnostics, "postgres_package")
	if run == nil {
		return
	}
	if err := r.apply(m, run); err != nil {
		resp.Diagnostics.AddError("postgres_package apply failed", err.Error())
		return
	}
	r.finish(&m, run)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// Delete is a no-op: uninstalling PostgreSQL would drop the cluster and data.
// Removing the resource simply stops managing the package (mirrors the singleton
// no-op deletes elsewhere in the house providers).
func (r *packageResource) Delete(_ context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
}

func (r *packageResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("version"), req.ID)...)
}

func (r *packageResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m packageModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil || r.client.SSH == nil {
		return
	}
	r.finish(&m, r.client.Run)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// finish resolves the computed id/state by reading the installed dpkg version.
func (r *packageResource) finish(m *packageModel, run postgres.RunFunc) {
	m.ID = m.Version
	out, err := run(fmt.Sprintf("dpkg-query -W -f='${Version}' %s 2>/dev/null || true", postgres.PackageName(m.Version.ValueString())), nil)
	if err != nil {
		// A read failure leaves state unknown rather than fabricating a value.
		m.State = types.StringValue("")
		return
	}
	m.State = types.StringValue(postgres.ParseDpkgVersion(string(out)))
}
