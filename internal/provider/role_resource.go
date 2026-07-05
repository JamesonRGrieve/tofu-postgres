// SPDX-License-Identifier: AGPL-3.0-or-later

package provider

import (
	"context"

	"github.com/JamesonRGrieve/tofu-postgres/internal/postgres"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// postgres_role natively owns a login/group role via `psql` run as the postgres
// superuser (CREATE/ALTER/DROP ROLE), read back from pg_roles. The password is
// injected through an ephemeral write-only attribute and is never persisted to
// state; password_ref records the secret-store path it came from. Import id is
// the role name.
var (
	_ resource.Resource                = (*roleResource)(nil)
	_ resource.ResourceWithConfigure   = (*roleResource)(nil)
	_ resource.ResourceWithImportState = (*roleResource)(nil)
)

// NewRoleResource constructs the postgres_role resource.
func NewRoleResource() resource.Resource { return &roleResource{} }

type roleResource struct {
	client *postgres.Client
}

type roleModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Login       types.Bool   `tfsdk:"login"`
	Superuser   types.Bool   `tfsdk:"superuser"`
	CreateDB    types.Bool   `tfsdk:"createdb"`
	CreateRole  types.Bool   `tfsdk:"createrole"`
	Password    types.String `tfsdk:"password"`
	PasswordRef types.String `tfsdk:"password_ref"`
}

// spec projects the model plus the ephemeral password into a postgres.RoleSpec.
func (m roleModel) spec(password string) postgres.RoleSpec {
	return postgres.RoleSpec{
		Name:       m.Name.ValueString(),
		Login:      m.Login.ValueBool(),
		Superuser:  m.Superuser.ValueBool(),
		CreateDB:   m.CreateDB.ValueBool(),
		CreateRole: m.CreateRole.ValueBool(),
		Password:   password,
	}
}

func (r *roleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_role"
}

func (r *roleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Natively owns a PostgreSQL role (`CREATE`/`ALTER`/`DROP ROLE` via `psql` run as the postgres " +
			"superuser). Read-back reconciles the attribute flags from `pg_roles`. The password is supplied through the " +
			"ephemeral write-only `password` attribute (never stored in state); `password_ref` records the OpenBao path it " +
			"was injected from.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Role name.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"login": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(true),
				MarkdownDescription: "Whether the role may log in (`LOGIN`/`NOLOGIN`, default true).",
			},
			"superuser": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
				MarkdownDescription: "Whether the role is a superuser (default false).",
			},
			"createdb": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
				MarkdownDescription: "Whether the role may create databases (default false).",
			},
			"createrole": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
				MarkdownDescription: "Whether the role may create/alter other roles (default false).",
			},
			"password": schema.StringAttribute{
				Optional:            true,
				Sensitive:           true,
				WriteOnly:           true,
				MarkdownDescription: "Ephemeral role password. Write-only — supplied at apply from the secret store (OpenBao → `TF_VAR_*` via Semaphore) and never written to plan or state. Omit to leave the password unchanged.",
			},
			"password_ref": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "OpenBao path the password was injected from (metadata only — not the secret itself).",
			},
		},
	}
}

func (r *roleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req, resp)
}

// writeOnlyPassword reads the ephemeral write-only password out of the config
// (it is null in plan/state by construction). Empty means "unchanged".
func writeOnlyPassword(ctx context.Context, cfg tfsdk.Config, diags *diag.Diagnostics) string {
	var pw types.String
	diags.Append(cfg.GetAttribute(ctx, path.Root("password"), &pw)...)
	if pw.IsNull() || pw.IsUnknown() {
		return ""
	}
	return pw.ValueString()
}

func (r *roleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m roleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	run := runFunc(r.client, &resp.Diagnostics, "postgres_role")
	if run == nil {
		return
	}
	pw := writeOnlyPassword(ctx, req.Config, &resp.Diagnostics)
	sql := postgres.CreateRoleSQL(m.spec(pw))
	if _, err := run(postgres.PsqlExec(""), []byte(sql+";\n")); err != nil {
		resp.Diagnostics.AddError("postgres_role create failed", err.Error())
		return
	}
	r.finish(&m, run, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *roleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m roleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	run := runFunc(r.client, &resp.Diagnostics, "postgres_role")
	if run == nil {
		return
	}
	pw := writeOnlyPassword(ctx, req.Config, &resp.Diagnostics)
	sql := postgres.AlterRoleSQL(m.spec(pw))
	if _, err := run(postgres.PsqlExec(""), []byte(sql+";\n")); err != nil {
		resp.Diagnostics.AddError("postgres_role update failed", err.Error())
		return
	}
	r.finish(&m, run, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// Delete drops the role (a logical object this resource wholly owns).
func (r *roleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m roleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	run := runFunc(r.client, &resp.Diagnostics, "postgres_role")
	if run == nil {
		return
	}
	if _, err := run(postgres.PsqlExec(""), []byte(postgres.DropRoleSQL(m.Name.ValueString())+";\n")); err != nil {
		resp.Diagnostics.AddError("postgres_role delete failed", err.Error())
	}
}

func (r *roleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), req.ID)...)
}

func (r *roleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m roleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil || r.client.SSH == nil {
		return
	}
	out, err := r.client.Run(postgres.PsqlExec(""), []byte(postgres.ReadRoleSQL(m.Name.ValueString())+";\n"))
	if err != nil {
		resp.Diagnostics.AddError("postgres_role read failed", err.Error())
		return
	}
	info, ok := postgres.ParseRoleRow(string(out))
	if !ok {
		resp.State.RemoveResource(ctx)
		return
	}
	reconcileRole(&m, info)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// reconcileRole refreshes the attribute flags from pg_roles. The password is
// never read back (it is a one-way hash), so it stays null in state.
func reconcileRole(m *roleModel, info postgres.RoleInfo) {
	m.ID = types.StringValue(m.Name.ValueString())
	m.Login = types.BoolValue(info.Login)
	m.Superuser = types.BoolValue(info.Superuser)
	m.CreateDB = types.BoolValue(info.CreateDB)
	m.CreateRole = types.BoolValue(info.CreateRole)
}

// finish sets the id and reconciles the attribute flags after a write.
func (r *roleResource) finish(m *roleModel, run postgres.RunFunc, diags *diag.Diagnostics) {
	m.ID = types.StringValue(m.Name.ValueString())
	out, err := run(postgres.PsqlExec(""), []byte(postgres.ReadRoleSQL(m.Name.ValueString())+";\n"))
	if err != nil {
		diags.AddError("postgres_role read-back failed", err.Error())
		return
	}
	if info, ok := postgres.ParseRoleRow(string(out)); ok {
		reconcileRole(m, info)
	}
}
