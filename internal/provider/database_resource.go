// SPDX-License-Identifier: AGPL-3.0-or-later

package provider

import (
	"context"
	"fmt"

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

// postgres_database natively owns a logical database via `psql` run as the
// postgres superuser (CREATE/ALTER/DROP DATABASE), read back from pg_database.
// Encoding/locale are fixed at creation (only the owner is mutable in place);
// changing them requires a replace. Import id is the database name.
var (
	_ resource.Resource                = (*databaseResource)(nil)
	_ resource.ResourceWithConfigure   = (*databaseResource)(nil)
	_ resource.ResourceWithImportState = (*databaseResource)(nil)
)

// NewDatabaseResource constructs the postgres_database resource.
func NewDatabaseResource() resource.Resource { return &databaseResource{} }

type databaseResource struct {
	client *postgres.Client
}

type databaseModel struct {
	ID        types.String `tfsdk:"id"`
	Name      types.String `tfsdk:"name"`
	Owner     types.String `tfsdk:"owner"`
	Encoding  types.String `tfsdk:"encoding"`
	LCCollate types.String `tfsdk:"lc_collate"`
	LCCtype   types.String `tfsdk:"lc_ctype"`
	Template  types.String `tfsdk:"template"`
}

func (m databaseModel) spec() postgres.DatabaseSpec {
	return postgres.DatabaseSpec{
		Name:      m.Name.ValueString(),
		Owner:     m.Owner.ValueString(),
		Encoding:  m.Encoding.ValueString(),
		LCCollate: m.LCCollate.ValueString(),
		LCCtype:   m.LCCtype.ValueString(),
		Template:  m.Template.ValueString(),
	}
}

func (r *databaseResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_database"
}

func (r *databaseResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Natively owns a PostgreSQL database (`CREATE`/`ALTER`/`DROP DATABASE` via `psql` run as the " +
			"postgres superuser). Read-back reconciles from `pg_database`. Only the owner is mutable in place; encoding/" +
			"locale are fixed at creation.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Database name.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"owner": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Owning role (default: the connecting superuser as reported by the server).",
			},
			"encoding": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("UTF8"),
				MarkdownDescription: "Character-set encoding (default `UTF8`). Fixed at creation — a change forces replace.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"lc_collate": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "`LC_COLLATE` string-sort locale. Fixed at creation — a change forces replace. Setting it (or `lc_ctype`) defaults the template to `template0`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"lc_ctype": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "`LC_CTYPE` character-classification locale. Fixed at creation — a change forces replace.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"template": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Template database. Defaults to `template0` when a locale override is set, otherwise the server default. Fixed at creation — a change forces replace.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
		},
	}
}

func (r *databaseResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req, resp)
}

// createCommands renders the CREATE DATABASE statement (fed to psql on stdin).
func createCommands(spec postgres.DatabaseSpec) []postgres.Command {
	return []postgres.Command{{
		Label: "create database " + spec.Name,
		Cmd:   postgres.PsqlExec(""),
		Stdin: []byte(postgres.CreateDatabaseSQL(spec) + ";\n"),
	}}
}

func (r *databaseResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m databaseModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	run := runFunc(r.client, &resp.Diagnostics, "postgres_database")
	if run == nil {
		return
	}
	if err := postgres.RunCommands(createCommands(m.spec()), run); err != nil {
		resp.Diagnostics.AddError("postgres_database create failed", err.Error())
		return
	}
	r.finish(&m, run, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *databaseResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state databaseModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	run := runFunc(r.client, &resp.Diagnostics, "postgres_database")
	if run == nil {
		return
	}
	// Only the owner is mutable in place (encoding/locale/template force replace).
	if plan.Owner.ValueString() != state.Owner.ValueString() {
		sql := postgres.AlterDatabaseOwnerSQL(plan.Name.ValueString(), plan.Owner.ValueString())
		if _, err := run(postgres.PsqlExec(""), []byte(sql+";\n")); err != nil {
			resp.Diagnostics.AddError("postgres_database owner change failed", err.Error())
			return
		}
	}
	r.finish(&plan, run, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete drops the database. Unlike the host-lifecycle resources (package/
// service), a logical database is owned wholesale by this resource, so destroy
// removes it.
func (r *databaseResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m databaseModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	run := runFunc(r.client, &resp.Diagnostics, "postgres_database")
	if run == nil {
		return
	}
	if _, err := run(postgres.PsqlExec(""), []byte(postgres.DropDatabaseSQL(m.Name.ValueString())+";\n")); err != nil {
		resp.Diagnostics.AddError("postgres_database delete failed", err.Error())
	}
}

func (r *databaseResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), req.ID)...)
}

func (r *databaseResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m databaseModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil || r.client.SSH == nil {
		return
	}
	out, err := r.client.Run(postgres.PsqlExec(""), []byte(postgres.ReadDatabaseSQL(m.Name.ValueString())+";\n"))
	if err != nil {
		resp.Diagnostics.AddError("postgres_database read failed", err.Error())
		return
	}
	info, ok := postgres.ParseDatabaseRow(string(out))
	if !ok {
		// The database is gone — drop it from state so a re-apply recreates it.
		resp.State.RemoveResource(ctx)
		return
	}
	r.reconcile(&m, info)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// reconcile refreshes the read-back attributes from the device. Owner/encoding
// are server-computed so always set; locale is set only when it was declared
// (manage-declared-only, so an undeclared locale never introduces a diff).
func (r *databaseResource) reconcile(m *databaseModel, info postgres.DatabaseInfo) {
	m.ID = types.StringValue(info.Name)
	m.Owner = types.StringValue(info.Owner)
	m.Encoding = types.StringValue(info.Encoding)
	if !m.LCCollate.IsNull() {
		m.LCCollate = types.StringValue(info.LCCollate)
	}
	if !m.LCCtype.IsNull() {
		m.LCCtype = types.StringValue(info.LCCtype)
	}
}

// finish sets the computed id and reconciles server-defaulted attributes
// (owner/encoding/locale) after a write.
func (r *databaseResource) finish(m *databaseModel, run postgres.RunFunc, diags *diag.Diagnostics) {
	m.ID = types.StringValue(m.Name.ValueString())
	out, err := run(postgres.PsqlExec(""), []byte(postgres.ReadDatabaseSQL(m.Name.ValueString())+";\n"))
	if err != nil {
		diags.AddError("postgres_database read-back failed", err.Error())
		return
	}
	if info, ok := postgres.ParseDatabaseRow(string(out)); ok {
		r.reconcile(m, info)
	} else {
		diags.AddError("postgres_database not found after create", fmt.Sprintf("database %q missing after apply", m.Name.ValueString()))
	}
}
