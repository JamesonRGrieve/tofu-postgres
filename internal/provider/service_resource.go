// SPDX-License-Identifier: AGPL-3.0-or-later

package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/JamesonRGrieve/tofu-postgres/internal/postgres"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// postgres_service manages the per-cluster systemd unit (postgresql@<major>-<cluster>):
// its enabled state, running state, and a restart_triggers map that forces a
// restart on change. Import id is `<version>` or `<version>/<cluster>`.
var (
	_ resource.Resource                = (*serviceResource)(nil)
	_ resource.ResourceWithConfigure   = (*serviceResource)(nil)
	_ resource.ResourceWithImportState = (*serviceResource)(nil)
)

// NewServiceResource constructs the postgres_service resource.
func NewServiceResource() resource.Resource { return &serviceResource{} }

type serviceResource struct {
	client *postgres.Client
}

type serviceModel struct {
	ID              types.String `tfsdk:"id"`
	Version         types.String `tfsdk:"version"`
	Cluster         types.String `tfsdk:"cluster"`
	Enabled         types.Bool   `tfsdk:"enabled"`
	State           types.String `tfsdk:"state"`
	RestartTriggers types.Map    `tfsdk:"restart_triggers"`
}

func (r *serviceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service"
}

func (r *serviceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages the per-cluster systemd unit `postgresql@<version>-<cluster>` — its enabled and " +
			"running state, with a `restart_triggers` map that forces a restart whenever it changes.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"version": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "PostgreSQL major version (component of the systemd unit name).",
			},
			"cluster": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("main"),
				MarkdownDescription: "Debian cluster name (default `main`).",
			},
			"enabled": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(true),
				MarkdownDescription: "Whether the unit is enabled at boot (default true).",
			},
			"state": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("started"),
				MarkdownDescription: "Desired running state: `started` (default) or `stopped`.",
			},
			"restart_triggers": schema.MapAttribute{
				Optional:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Arbitrary key/value map; any change forces a restart of the running cluster on the next apply.",
			},
		},
	}
}

func (r *serviceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req, resp)
}

// buildServiceCommands is the pure command builder (unit-tested via injected
// RunFunc). enable/disable, then reach the desired running state (restart when
// requested and started).
func buildServiceCommands(unit string, enabled bool, state string, restart bool) []postgres.Command {
	var cmds []postgres.Command
	if enabled {
		cmds = append(cmds, postgres.Command{Label: "enable " + unit, Cmd: "systemctl enable " + unit})
	} else {
		cmds = append(cmds, postgres.Command{Label: "disable " + unit, Cmd: "systemctl disable " + unit})
	}
	switch state {
	case "stopped":
		cmds = append(cmds, postgres.Command{Label: "stop " + unit, Cmd: "systemctl stop " + unit})
	default: // started
		action := "start"
		if restart {
			action = "restart"
		}
		cmds = append(cmds, postgres.Command{Label: action + " " + unit, Cmd: "systemctl " + action + " " + unit})
	}
	return cmds
}

func (r *serviceResource) apply(m serviceModel, restart bool, run postgres.RunFunc) error {
	unit := postgres.ServiceUnit(m.Version.ValueString(), m.Cluster.ValueString())
	return postgres.RunCommands(buildServiceCommands(unit, m.Enabled.ValueBool(), m.State.ValueString(), restart), run)
}

func (r *serviceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m serviceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.exec(ctx, &m, false, &resp.Diagnostics) {
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	}
}

func (r *serviceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state serviceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// A changed restart_triggers map forces a restart.
	restart := !plan.RestartTriggers.Equal(state.RestartTriggers)
	if r.exec(ctx, &plan, restart, &resp.Diagnostics) {
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
}

func (r *serviceResource) exec(_ context.Context, m *serviceModel, restart bool, diags *diag.Diagnostics) bool {
	run := runFunc(r.client, diags, "postgres_service")
	if run == nil {
		return false
	}
	if err := r.apply(*m, restart, run); err != nil {
		diags.AddError("postgres_service apply failed", err.Error())
		return false
	}
	m.ID = types.StringValue(m.Version.ValueString() + "/" + m.Cluster.ValueString())
	return true
}

// Delete is a no-op: stopping/disabling on destroy would take the DB offline.
func (r *serviceResource) Delete(_ context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
}

func (r *serviceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	version, cluster := splitImportID(req.ID)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("version"), version)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cluster"), cluster)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), version+"/"+cluster)...)
}

func (r *serviceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m serviceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil || r.client.SSH == nil {
		return
	}
	unit := postgres.ServiceUnit(m.Version.ValueString(), m.Cluster.ValueString())
	// `|| true` keeps a stopped/failed unit (is-active exits non-zero) from erroring
	// the whole read. A genuine SSH failure (the node is down) still errors — and is
	// then degraded to "stopped" below rather than blocking convergence, so an apply
	// can reconcile a dead node instead of a refresh dead-ending on it.
	script := fmt.Sprintf("systemctl is-enabled %s 2>/dev/null || true; echo '%s'; systemctl is-active %s 2>/dev/null || true", unit, readSentinel, unit)
	out, err := r.client.Run(script, nil)
	if err != nil {
		m.Enabled = types.BoolValue(false)
		m.State = types.StringValue("stopped")
		m.ID = types.StringValue(m.Version.ValueString() + "/" + m.Cluster.ValueString())
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
		return
	}
	enabledPart, activePart := splitSentinel(string(out))
	m.Enabled = types.BoolValue(strings.TrimSpace(enabledPart) == "enabled")
	if strings.TrimSpace(activePart) == "active" {
		m.State = types.StringValue("started")
	} else {
		m.State = types.StringValue("stopped")
	}
	m.ID = types.StringValue(m.Version.ValueString() + "/" + m.Cluster.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
