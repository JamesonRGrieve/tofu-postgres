// SPDX-License-Identifier: AGPL-3.0-or-later

package provider

import (
	"context"

	"github.com/JamesonRGrieve/tofu-postgres/internal/postgres"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// postgres_cluster declares an HA cluster's topology: its name, mode
// (patroni | repmgr | streaming), optional DCS reference (patroni only), and
// synchronous flag. It is a declarative record that postgres_cluster_node
// resources consume — cluster bring-up is per-node, so this resource validates
// the topology and holds intent rather than driving a device itself. Import id
// is the cluster name.
var (
	_ resource.Resource                = (*clusterResource)(nil)
	_ resource.ResourceWithConfigure   = (*clusterResource)(nil)
	_ resource.ResourceWithImportState = (*clusterResource)(nil)
)

// NewClusterResource constructs the postgres_cluster resource.
func NewClusterResource() resource.Resource { return &clusterResource{} }

type clusterResource struct {
	client *postgres.Client
}

type clusterModel struct {
	ID           types.String `tfsdk:"id"`
	Name         types.String `tfsdk:"name"`
	HAMode       types.String `tfsdk:"ha_mode"`
	DCSReference types.String `tfsdk:"dcs_reference"`
	Synchronous  types.Bool   `tfsdk:"synchronous"`
}

func (r *clusterResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cluster"
}

func (r *clusterResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Declares a PostgreSQL HA cluster's topology (name, mode, DCS reference, synchronous flag). " +
			"A declarative record consumed by `postgres_cluster_node`; bring-up happens per node. `ha_mode` selects " +
			"the replication strategy: `streaming` (plain physical), `repmgr`, or `patroni`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Logical HA cluster name (the Patroni scope / repmgr cluster name). Stable id.",
			},
			"ha_mode": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "HA strategy: `streaming`, `repmgr`, or `patroni`.",
			},
			"dcs_reference": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "DCS endpoint(s) for Patroni (e.g. `10.0.0.10:2379,10.0.0.11:2379`). Required when `ha_mode = patroni`; ignored otherwise.",
			},
			"synchronous": schema.BoolAttribute{
				Optional:            true,
				MarkdownDescription: "Whether the cluster runs synchronous replication (at least one synchronous standby). Default false.",
			},
		},
	}
}

func (r *clusterResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req, resp)
}

// validate checks the declared topology (mode recognized; DCS present for
// patroni). Pure so it is unit-tested without a device.
func validateCluster(m clusterModel) diag.Diagnostics {
	var diags diag.Diagnostics
	mode := postgres.HAMode(m.HAMode.ValueString())
	if !mode.Valid() {
		diags.AddAttributeError(path.Root("ha_mode"), "Unknown ha_mode",
			"ha_mode must be one of streaming, repmgr, patroni; got "+m.HAMode.ValueString())
		return diags
	}
	if mode == postgres.ModePatroni && m.DCSReference.ValueString() == "" {
		diags.AddAttributeError(path.Root("dcs_reference"), "dcs_reference required for patroni",
			"ha_mode = patroni requires a dcs_reference (etcd/consul endpoint list).")
	}
	return diags
}

func (r *clusterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m clusterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(validateCluster(m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m.ID = m.Name
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *clusterResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m clusterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(validateCluster(m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m.ID = m.Name
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *clusterResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	// No device object backs the declarative record; keep prior state verbatim.
	var m clusterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// Delete is a no-op: the record simply stops being managed.
func (r *clusterResource) Delete(_ context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
}

func (r *clusterResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
