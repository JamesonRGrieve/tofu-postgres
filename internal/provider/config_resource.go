// SPDX-License-Identifier: AGPL-3.0-or-later

package provider

import (
	"context"
	"fmt"
	"strconv"
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

// postgres_config manages postgresql.conf keys (via a conf.d drop-in) and the
// tofu-owned block of pg_hba.conf. Manage-declared-only: an unset config
// attribute is not written and not reconciled; the pg_hba list fully owns the
// marker block. Import id is `<version>` or `<version>/<cluster>`.
var (
	_ resource.Resource                = (*configResource)(nil)
	_ resource.ResourceWithConfigure   = (*configResource)(nil)
	_ resource.ResourceWithImportState = (*configResource)(nil)
)

// NewConfigResource constructs the postgres_config resource.
func NewConfigResource() resource.Resource { return &configResource{} }

type configResource struct {
	client *postgres.Client
}

type configModel struct {
	ID                 types.String `tfsdk:"id"`
	Version            types.String `tfsdk:"version"`
	Cluster            types.String `tfsdk:"cluster"`
	SharedBuffers      types.String `tfsdk:"shared_buffers"`
	EffectiveCacheSize types.String `tfsdk:"effective_cache_size"`
	WorkMem            types.String `tfsdk:"work_mem"`
	MaintenanceWorkMem types.String `tfsdk:"maintenance_work_mem"`
	MaxConnections     types.Int64  `tfsdk:"max_connections"`
	ListenAddresses    types.String `tfsdk:"listen_addresses"`
	PasswordEncryption types.String `tfsdk:"password_encryption"`
	WalInitZero        types.Bool   `tfsdk:"wal_init_zero"`
	WalRecycle         types.Bool   `tfsdk:"wal_recycle"`
	PgHba              types.List   `tfsdk:"pg_hba"`
}

type hbaEntryModel struct {
	Type     types.String `tfsdk:"type"`
	Database types.String `tfsdk:"database"`
	User     types.String `tfsdk:"user"`
	Address  types.String `tfsdk:"address"`
	Method   types.String `tfsdk:"method"`
}

func (r *configResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_config"
}

func (r *configResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages `postgresql.conf` keys (through a `conf.d` drop-in) and the tofu-owned block of " +
			"`pg_hba.conf`. Only declared config keys are written; the `pg_hba` list fully owns its marker block. " +
			"An update reloads the cluster, or restarts it when a postmaster-context key (shared_buffers, " +
			"max_connections, listen_addresses) is declared.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"version": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "PostgreSQL major version (locates `/etc/postgresql/<version>/<cluster>`).",
			},
			"cluster": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("main"),
				MarkdownDescription: "Debian cluster name (default `main`).",
			},
			"shared_buffers":       optStr("`shared_buffers` (e.g. `256MB`). Restart-context — declaring it makes an update restart."),
			"effective_cache_size": optStr("`effective_cache_size` planner hint (e.g. `1GB`)."),
			"work_mem":             optStr("`work_mem` per-sort memory (e.g. `16MB`)."),
			"maintenance_work_mem": optStr("`maintenance_work_mem` (e.g. `256MB`)."),
			"max_connections": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "`max_connections`. Restart-context — declaring it makes an update restart.",
			},
			"listen_addresses": optStr("`listen_addresses` (e.g. `localhost` or `*`). Restart-context."),
			"password_encryption": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("scram-sha-256"),
				MarkdownDescription: "`password_encryption` (default `scram-sha-256`; md5 is deprecated).",
			},
			"wal_init_zero": schema.BoolAttribute{
				Optional:            true,
				MarkdownDescription: "`wal_init_zero`. Set `false` on ZFS/COW datadirs (zeroing new WAL segments is pure overhead there).",
			},
			"wal_recycle": schema.BoolAttribute{
				Optional:            true,
				MarkdownDescription: "`wal_recycle`. Set `false` on ZFS/COW datadirs (recycling forces full-record rewrites).",
			},
			"pg_hba": schema.ListNestedAttribute{
				Optional:            true,
				MarkdownDescription: "Ordered pg_hba.conf access rules owned by the tofu-managed block. Method defaults to `scram-sha-256`.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"type":     schema.StringAttribute{Required: true, MarkdownDescription: "`local`, `host`, `hostssl`, or `hostnossl`."},
						"database": schema.StringAttribute{Required: true, MarkdownDescription: "Database (e.g. `all`, a name, `replication`)."},
						"user":     schema.StringAttribute{Required: true, MarkdownDescription: "Role (e.g. `all`, `postgres`)."},
						"address":  schema.StringAttribute{Optional: true, MarkdownDescription: "CIDR/host for `host*` rules; omit for `local`."},
						"method":   schema.StringAttribute{Optional: true, MarkdownDescription: "Auth method (default `scram-sha-256`)."},
					},
				},
			},
		},
	}
}

func optStr(desc string) schema.StringAttribute {
	return schema.StringAttribute{Optional: true, MarkdownDescription: desc}
}

func (r *configResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req, resp)
}

// settings collects the declared config keys into an ordered ConfSetting list
// and the parallel key names (for the reload-vs-restart decision).
func (m configModel) settings() ([]postgres.ConfSetting, []string) {
	var out []postgres.ConfSetting
	var keys []string
	add := func(key, val string, quote bool) {
		out = append(out, postgres.ConfSetting{Key: key, Value: val, Quote: quote})
		keys = append(keys, key)
	}
	if !m.SharedBuffers.IsNull() {
		add("shared_buffers", m.SharedBuffers.ValueString(), true)
	}
	if !m.EffectiveCacheSize.IsNull() {
		add("effective_cache_size", m.EffectiveCacheSize.ValueString(), true)
	}
	if !m.WorkMem.IsNull() {
		add("work_mem", m.WorkMem.ValueString(), true)
	}
	if !m.MaintenanceWorkMem.IsNull() {
		add("maintenance_work_mem", m.MaintenanceWorkMem.ValueString(), true)
	}
	if !m.MaxConnections.IsNull() {
		add("max_connections", strconv.FormatInt(m.MaxConnections.ValueInt64(), 10), false)
	}
	if !m.ListenAddresses.IsNull() {
		add("listen_addresses", m.ListenAddresses.ValueString(), true)
	}
	if !m.PasswordEncryption.IsNull() {
		add("password_encryption", m.PasswordEncryption.ValueString(), true)
	}
	if !m.WalInitZero.IsNull() {
		add("wal_init_zero", onOff(m.WalInitZero.ValueBool()), false)
	}
	if !m.WalRecycle.IsNull() {
		add("wal_recycle", onOff(m.WalRecycle.ValueBool()), false)
	}
	return out, keys
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// buildConfigCommands is the pure command builder (unit-tested via injected
// RunFunc). It writes the conf.d drop-in, rewrites the pg_hba managed block (fed
// on stdin), then reloads or restarts.
func buildConfigCommands(version, cluster string, settings []postgres.ConfSetting, hba []postgres.HBAEntry, restart bool) []postgres.Command {
	var cmds []postgres.Command
	if len(settings) > 0 {
		cmds = append(cmds, postgres.Command{
			Label: "write conf.d drop-in",
			Cmd:   fmt.Sprintf("mkdir -p %s && cat > %s", shSingleQuote(postgres.ConfDropInDir(version, cluster)), shSingleQuote(postgres.ConfDropInPath(version, cluster))),
			Stdin: []byte(postgres.RenderConfD(settings)),
		})
	}
	if len(hba) > 0 {
		cmds = append(cmds, postgres.Command{
			Label: "rewrite pg_hba block",
			Cmd:   postgres.PgHbaReassembleCommand(postgres.HBAPath(version, cluster)),
			Stdin: []byte(postgres.RenderPgHba(hba)),
		})
	}
	action := "reload"
	if restart {
		action = "restart"
	}
	cmds = append(cmds, postgres.Command{
		Label: "pg_ctlcluster " + action,
		Cmd:   fmt.Sprintf("pg_ctlcluster %s %s %s", version, cluster, action),
	})
	return cmds
}

// shSingleQuote single-quotes a value for a remote shell command.
func shSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (m configModel) hbaEntries(ctx context.Context) []postgres.HBAEntry {
	if m.PgHba.IsNull() || m.PgHba.IsUnknown() {
		return nil
	}
	var rows []hbaEntryModel
	_ = m.PgHba.ElementsAs(ctx, &rows, false)
	out := make([]postgres.HBAEntry, 0, len(rows))
	for _, e := range rows {
		out = append(out, postgres.HBAEntry{
			Type: e.Type.ValueString(), Database: e.Database.ValueString(), User: e.User.ValueString(),
			Address: e.Address.ValueString(), Method: e.Method.ValueString(),
		})
	}
	return out
}

func (r *configResource) apply(ctx context.Context, m configModel, run postgres.RunFunc) error {
	settings, keys := m.settings()
	hba := m.hbaEntries(ctx)
	cmds := buildConfigCommands(m.Version.ValueString(), m.Cluster.ValueString(), settings, hba, postgres.NeedsRestart(keys))
	return postgres.RunCommands(cmds, run)
}

func (r *configResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m configModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.write(ctx, &m, &resp.Diagnostics) {
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	}
}

func (r *configResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m configModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.write(ctx, &m, &resp.Diagnostics) {
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	}
}

// write applies the plan and sets the computed id, returning true on success.
func (r *configResource) write(ctx context.Context, m *configModel, diags *diag.Diagnostics) bool {
	run := runFunc(r.client, diags, "postgres_config")
	if run == nil {
		return false
	}
	if err := r.apply(ctx, *m, run); err != nil {
		diags.AddError("postgres_config apply failed", err.Error())
		return false
	}
	m.ID = types.StringValue(m.Version.ValueString() + "/" + m.Cluster.ValueString())
	return true
}

// Delete is a no-op: the conf.d drop-in and pg_hba block persist. Removing the
// resource stops managing them (files could be removed, but that would silently
// revert config a consumer still depends on).
func (r *configResource) Delete(_ context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
}

func (r *configResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	version, cluster := splitImportID(req.ID)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("version"), version)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cluster"), cluster)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), version+"/"+cluster)...)
}

func (r *configResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m configModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil || r.client.SSH == nil {
		return
	}
	version, cluster := m.Version.ValueString(), m.Cluster.ValueString()
	// One round-trip: emit the drop-in then a sentinel then pg_hba.conf.
	script := fmt.Sprintf("cat %s 2>/dev/null; echo '%s'; cat %s 2>/dev/null",
		shSingleQuote(postgres.ConfDropInPath(version, cluster)), readSentinel, shSingleQuote(postgres.HBAPath(version, cluster)))
	out, err := r.client.Run(script, nil)
	if err != nil {
		resp.Diagnostics.AddError("postgres_config read failed", err.Error())
		return
	}
	confPart, hbaPart := splitSentinel(string(out))
	cur := postgres.ParseConfD(confPart)
	r.reconcile(ctx, &m, cur, postgres.ParseHBABlock(hbaPart), &resp.Diagnostics)
	m.ID = types.StringValue(version + "/" + cluster)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// reconcile refreshes only the declared (non-null) config attributes from the
// device (subset semantics), and replaces the pg_hba list wholesale when it was
// declared.
func (r *configResource) reconcile(ctx context.Context, m *configModel, cur map[string]string, hba []postgres.HBAEntry, diags *diag.Diagnostics) {
	setStr := func(dst *types.String, key string) {
		if !dst.IsNull() {
			if v, ok := cur[key]; ok {
				*dst = types.StringValue(v)
			}
		}
	}
	setStr(&m.SharedBuffers, "shared_buffers")
	setStr(&m.EffectiveCacheSize, "effective_cache_size")
	setStr(&m.WorkMem, "work_mem")
	setStr(&m.MaintenanceWorkMem, "maintenance_work_mem")
	setStr(&m.ListenAddresses, "listen_addresses")
	setStr(&m.PasswordEncryption, "password_encryption")
	if !m.MaxConnections.IsNull() {
		if v, ok := cur["max_connections"]; ok {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				m.MaxConnections = types.Int64Value(n)
			}
		}
	}
	if !m.WalInitZero.IsNull() {
		if v, ok := cur["wal_init_zero"]; ok {
			m.WalInitZero = types.BoolValue(v == "on")
		}
	}
	if !m.WalRecycle.IsNull() {
		if v, ok := cur["wal_recycle"]; ok {
			m.WalRecycle = types.BoolValue(v == "on")
		}
	}
	if !m.PgHba.IsNull() {
		m.PgHba = hbaListValue(ctx, hba, diags)
	}
}
