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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// postgres_grant natively owns a role's privileges on a database object via
// `psql` run as the postgres superuser (GRANT/REVOKE), read back from the object
// ACL / information_schema. It converges to exactly the declared privilege set
// (a prior REVOKE ALL precedes each GRANT). Import id is `role:database:object_type`.
var (
	_ resource.Resource                = (*grantResource)(nil)
	_ resource.ResourceWithConfigure   = (*grantResource)(nil)
	_ resource.ResourceWithImportState = (*grantResource)(nil)
)

// NewGrantResource constructs the postgres_grant resource.
func NewGrantResource() resource.Resource { return &grantResource{} }

type grantResource struct {
	client *postgres.Client
}

type grantModel struct {
	ID         types.String `tfsdk:"id"`
	Role       types.String `tfsdk:"role"`
	Database   types.String `tfsdk:"database"`
	ObjectType types.String `tfsdk:"object_type"`
	Schema     types.String `tfsdk:"schema"`
	Objects    types.List   `tfsdk:"objects"`
	Privileges types.List   `tfsdk:"privileges"`
}

func (m grantModel) spec(ctx context.Context) postgres.GrantSpec {
	return postgres.GrantSpec{
		Role:       m.Role.ValueString(),
		Database:   m.Database.ValueString(),
		ObjectType: m.ObjectType.ValueString(),
		Schema:     m.Schema.ValueString(),
		Objects:    listToStrings(ctx, m.Objects),
		Privileges: listToStrings(ctx, m.Privileges),
	}
}

func (r *grantResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_grant"
}

func (r *grantResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Natively owns a role's privileges on a database object (`GRANT`/`REVOKE` via `psql` run as the " +
			"postgres superuser). Converges to exactly the declared privilege set; read-back reconciles from the object ACL " +
			"(`database`/`schema`) or `information_schema.role_table_grants` (`table`/`all_tables`).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"role": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Role receiving the privileges.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"database": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Target database (the object the grant applies to, or the database schema/table objects live in).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"object_type": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString(postgres.GrantDatabase),
				MarkdownDescription: "Object class: `database` (default), `schema`, `table`, or `all_tables`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"schema": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("public"),
				MarkdownDescription: "Schema for `schema`/`table`/`all_tables` grants (default `public`; unused for `database`).",
			},
			"objects": schema.ListAttribute{
				Optional:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Specific table names within `schema` for `object_type = \"table\"`. Ignored for other object types.",
			},
			"privileges": schema.ListAttribute{
				Required:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Privileges to grant, e.g. `[\"ALL\"]` or `[\"CONNECT\", \"SELECT\"]`. `ALL` collapses to the object type's full set for 0-diff read-back.",
			},
		},
	}
}

func (r *grantResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req, resp)
}

// grantCommands converges the grant to exactly the declared privileges: a REVOKE
// of the full privilege set first (so a shrunk privilege list actually loses the
// dropped ones), then the GRANT. Both run in the object's database.
func grantCommands(spec postgres.GrantSpec) []postgres.Command {
	db := spec.GrantDB()
	return []postgres.Command{
		{Label: "revoke prior privileges", Cmd: postgres.PsqlExec(db), Stdin: []byte(postgres.RevokeSQL(spec, []string{"ALL"}) + ";\n")},
		{Label: "grant privileges", Cmd: postgres.PsqlExec(db), Stdin: []byte(postgres.GrantSQL(spec) + ";\n")},
	}
}

func (r *grantResource) apply(ctx context.Context, m *grantModel, diags *diag.Diagnostics) bool {
	run := runFunc(r.client, diags, "postgres_grant")
	if run == nil {
		return false
	}
	if err := postgres.RunCommands(grantCommands(m.spec(ctx)), run); err != nil {
		diags.AddError("postgres_grant apply failed", err.Error())
		return false
	}
	m.ID = types.StringValue(grantID(*m))
	return true
}

// grantID is the composite identifier `role:database:object_type`.
func grantID(m grantModel) string {
	return fmt.Sprintf("%s:%s:%s", m.Role.ValueString(), m.Database.ValueString(), m.ObjectType.ValueString())
}

func (r *grantResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m grantModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.apply(ctx, &m, &resp.Diagnostics) {
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	}
}

func (r *grantResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m grantModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.apply(ctx, &m, &resp.Diagnostics) {
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	}
}

// Delete revokes the managed privileges.
func (r *grantResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m grantModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	run := runFunc(r.client, &resp.Diagnostics, "postgres_grant")
	if run == nil {
		return
	}
	spec := m.spec(ctx)
	if _, err := run(postgres.PsqlExec(spec.GrantDB()), []byte(postgres.RevokeSQL(spec, spec.Privileges)+";\n")); err != nil {
		resp.Diagnostics.AddError("postgres_grant delete failed", err.Error())
	}
}

func (r *grantResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, ":", 3)
	if len(parts) != 3 {
		resp.Diagnostics.AddError("invalid postgres_grant import id",
			fmt.Sprintf("expected `role:database:object_type`, got %q", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("role"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("database"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("object_type"), parts[2])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

func (r *grantResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m grantModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil || r.client.SSH == nil {
		return
	}
	spec := m.spec(ctx)
	out, err := r.client.Run(postgres.PsqlExec(spec.GrantDB()), []byte(postgres.ReadGrantSQL(spec)+";\n"))
	if err != nil {
		resp.Diagnostics.AddError("postgres_grant read failed", err.Error())
		return
	}
	observed := postgres.ParseGrantPrivileges(string(out))
	prior := listToStrings(ctx, m.Privileges)
	reconciled := postgres.ReconcileGrantPrivileges(spec.ObjectType, prior, observed)
	if reconciled == nil {
		// No privileges remain — the grant is gone.
		resp.State.RemoveResource(ctx)
		return
	}
	lv, d := types.ListValueFrom(ctx, types.StringType, reconciled)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	m.Privileges = lv
	m.ID = types.StringValue(grantID(m))
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
