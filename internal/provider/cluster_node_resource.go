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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// postgres_cluster_node brings up one node of an HA cluster, dispatching on the
// cluster's ha_mode (streaming | repmgr | patroni). Promotion/failover and
// base-backup clones are interruption-unsafe — see DESIGN.md; run them through
// the sanctioned pipeline on a lab twin first. Import id is `<cluster>/<node_name>`.
var (
	_ resource.Resource                = (*clusterNodeResource)(nil)
	_ resource.ResourceWithConfigure   = (*clusterNodeResource)(nil)
	_ resource.ResourceWithImportState = (*clusterNodeResource)(nil)
)

// NewClusterNodeResource constructs the postgres_cluster_node resource.
func NewClusterNodeResource() resource.Resource { return &clusterNodeResource{} }

type clusterNodeResource struct {
	client *postgres.Client
}

type clusterNodeModel struct {
	ID                  types.String `tfsdk:"id"`
	Cluster             types.String `tfsdk:"cluster"`
	HAMode              types.String `tfsdk:"ha_mode"`
	Version             types.String `tfsdk:"version"`
	PGCluster           types.String `tfsdk:"pg_cluster"`
	NodeName            types.String `tfsdk:"node_name"`
	NodeID              types.Int64  `tfsdk:"node_id"`
	Host                types.String `tfsdk:"host"`
	Port                types.Int64  `tfsdk:"port"`
	Role                types.String `tfsdk:"role"`
	PrimaryHost         types.String `tfsdk:"primary_host"`
	PrimaryPort         types.Int64  `tfsdk:"primary_port"`
	ReplicationUser     types.String `tfsdk:"replication_user"`
	ReplicationPassword types.String `tfsdk:"replication_password"`
	ReplicationSlot     types.String `tfsdk:"replication_slot"`
	IsSyncStandby       types.Bool   `tfsdk:"is_synchronous_standby"`
	DCSReference        types.String `tfsdk:"dcs_reference"`
	RestAPIConnect      types.String `tfsdk:"rest_api_connect"`
	PGConnect           types.String `tfsdk:"pg_connect"`
	SuperUser           types.String `tfsdk:"super_user"`
	SuperPassword       types.String `tfsdk:"super_password"`
}

func (r *clusterNodeResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cluster_node"
}

func (r *clusterNodeResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Brings up one node of a PostgreSQL HA cluster, dispatching on `ha_mode` " +
			"(`streaming` | `repmgr` | `patroni`). Base-backup clones, promotion, and failover are " +
			"interruption-unsafe — drive them through the sanctioned pipeline on a lab twin first.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"cluster": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Logical HA cluster name (matches a `postgres_cluster.name` / Patroni scope).",
			},
			"ha_mode": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "HA strategy this node participates in: `streaming`, `repmgr`, or `patroni`.",
			},
			"version": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "PostgreSQL major version (locates data/config dirs and the systemd unit).",
			},
			"pg_cluster": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("main"),
				MarkdownDescription: "Debian cluster name for this node's local instance (default `main`).",
			},
			"node_name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "This node's name (application_name / repmgr node_name / Patroni name).",
			},
			"node_id": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "Numeric node id (required by repmgr; ignored otherwise).",
			},
			"host": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "This node's routable address (used to build its own conninfo).",
			},
			"port": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "This node's PostgreSQL port (default 5432).",
			},
			"role": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Node role: `primary`, `replica`, or `witness` (repmgr).",
			},
			"primary_host": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "The primary this replica follows (required for `replica`/`witness`).",
			},
			"primary_port": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "The primary's PostgreSQL port (default 5432).",
			},
			"replication_user": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Replication role used for streaming/clone (default `repmgr` in repmgr mode).",
			},
			"replication_password": schema.StringAttribute{
				Optional:            true,
				Sensitive:           true,
				MarkdownDescription: "Replication role password (injected at apply from the secret store).",
			},
			"replication_slot": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Physical replication slot: created on the primary, consumed by the standby.",
			},
			"is_synchronous_standby": schema.BoolAttribute{
				Optional:            true,
				MarkdownDescription: "Whether this standby participates as a synchronous standby (drives synchronous_mode for Patroni).",
			},
			"dcs_reference": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "DCS endpoint(s) for Patroni (etcd/consul). Required in `patroni` mode.",
			},
			"rest_api_connect": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Patroni REST API connect address for this node (e.g. `10.0.0.20:8008`).",
			},
			"pg_connect": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Patroni PostgreSQL connect address for this node (e.g. `10.0.0.20:5432`).",
			},
			"super_user": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Superuser name for Patroni's authentication block.",
			},
			"super_password": schema.StringAttribute{
				Optional:            true,
				Sensitive:           true,
				MarkdownDescription: "Superuser password for Patroni's authentication block (injected at apply).",
			},
		},
	}
}

func (r *clusterNodeResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req, resp)
}

// nodeSpec projects the model onto the mode-agnostic postgres.NodeSpec. Pure, so
// the projection + dispatch is unit-tested without a device.
func (m clusterNodeModel) nodeSpec() postgres.NodeSpec {
	return postgres.NodeSpec{
		Mode:                postgres.HAMode(m.HAMode.ValueString()),
		Role:                postgres.NodeRole(m.Role.ValueString()),
		Version:             m.Version.ValueString(),
		Cluster:             m.PGCluster.ValueString(),
		ClusterName:         m.Cluster.ValueString(),
		NodeName:            m.NodeName.ValueString(),
		NodeID:              int(m.NodeID.ValueInt64()),
		Host:                m.Host.ValueString(),
		Port:                int(m.Port.ValueInt64()),
		PrimaryHost:         m.PrimaryHost.ValueString(),
		PrimaryPort:         int(m.PrimaryPort.ValueInt64()),
		ReplicationUser:     m.ReplicationUser.ValueString(),
		ReplicationPassword: m.ReplicationPassword.ValueString(),
		ReplicationSlot:     m.ReplicationSlot.ValueString(),
		Synchronous:         m.IsSyncStandby.ValueBool(),
		DCSReference:        m.DCSReference.ValueString(),
		RestAPIConnect:      m.RestAPIConnect.ValueString(),
		PGConnect:           m.PGConnect.ValueString(),
		SuperUser:           m.SuperUser.ValueString(),
		SuperPassword:       m.SuperPassword.ValueString(),
	}
}

func (r *clusterNodeResource) apply(m clusterNodeModel, run postgres.RunFunc) error {
	cmds, err := postgres.NodeCommands(m.nodeSpec())
	if err != nil {
		return err
	}
	return postgres.RunCommands(cmds, run)
}

func (r *clusterNodeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m clusterNodeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.exec(&m, &resp.Diagnostics) {
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	}
}

func (r *clusterNodeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m clusterNodeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.exec(&m, &resp.Diagnostics) {
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	}
}

func (r *clusterNodeResource) exec(m *clusterNodeModel, diags *diag.Diagnostics) bool {
	run := runFunc(r.client, diags, "postgres_cluster_node")
	if run == nil {
		return false
	}
	if err := r.apply(*m, run); err != nil {
		diags.AddError("postgres_cluster_node apply failed", err.Error())
		return false
	}
	m.ID = types.StringValue(m.Cluster.ValueString() + "/" + m.NodeName.ValueString())
	return true
}

// Delete is a no-op: tearing a node out of a live cluster is a failover-class
// operation that must be driven deliberately, not on a `terraform destroy`.
func (r *clusterNodeResource) Delete(_ context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
}

func (r *clusterNodeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	cluster, node := splitImportID(req.ID)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cluster"), cluster)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("node_name"), node)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

func (r *clusterNodeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	// Node role/replication state cannot be reconciled generically across the
	// three modes without mode-specific probes (verification-owed — see
	// DESIGN.md). Keep prior state verbatim and set the id.
	var m clusterNodeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m.ID = types.StringValue(m.Cluster.ValueString() + "/" + m.NodeName.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
