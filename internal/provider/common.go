// SPDX-License-Identifier: AGPL-3.0-or-later

package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/JamesonRGrieve/tofu-postgres/internal/postgres"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// readSentinel separates the two file dumps in a single-round-trip read script.
const readSentinel = "__TOFU_PG_SENTINEL__"

// splitImportID parses a `<version>` or `<version>/<cluster>` import id,
// defaulting the cluster to `main`.
func splitImportID(id string) (version, cluster string) {
	if i := strings.IndexByte(id, '/'); i >= 0 {
		version, cluster = id[:i], id[i+1:]
		if cluster == "" {
			cluster = "main"
		}
		return version, cluster
	}
	return id, "main"
}

// splitSentinel splits a read script's output on readSentinel into its two
// parts (before, after). A missing sentinel yields the whole output as before.
func splitSentinel(out string) (before, after string) {
	if i := strings.Index(out, readSentinel); i >= 0 {
		return out[:i], out[i+len(readSentinel):]
	}
	return out, ""
}

// hbaObjectType is the element type of the pg_hba list attribute.
func hbaObjectType() types.ObjectType {
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"type":     types.StringType,
		"database": types.StringType,
		"user":     types.StringType,
		"address":  types.StringType,
		"method":   types.StringType,
	}}
}

// hbaListValue converts parsed pg_hba entries back into a framework list value
// (for read-back reconciliation). An empty address/method renders as null.
func hbaListValue(ctx context.Context, entries []postgres.HBAEntry, diags *diag.Diagnostics) types.List {
	objs := make([]attr.Value, 0, len(entries))
	for _, e := range entries {
		obj, d := types.ObjectValue(hbaObjectType().AttrTypes, map[string]attr.Value{
			"type":     types.StringValue(e.Type),
			"database": types.StringValue(e.Database),
			"user":     types.StringValue(e.User),
			"address":  optString(e.Address),
			"method":   optString(e.Method),
		})
		diags.Append(d...)
		objs = append(objs, obj)
	}
	lv, d := types.ListValue(hbaObjectType(), objs)
	diags.Append(d...)
	return lv
}

func optString(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

// configureClient is the shared ResourceWithConfigure body: it extracts the
// *postgres.Client the provider stashed, or records a diagnostic. Returns nil
// when ProviderData is absent (early configure pass) so the caller returns.
func configureClient(req resource.ConfigureRequest, resp *resource.ConfigureResponse) *postgres.Client {
	if req.ProviderData == nil {
		return nil
	}
	client, ok := req.ProviderData.(*postgres.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data",
			fmt.Sprintf("expected *postgres.Client, got %T", req.ProviderData))
		return nil
	}
	return client
}

// runFunc adapts a client to the injectable postgres.RunFunc seam, or records a
// diagnostic and returns nil when the SSH transport is unconfigured.
func runFunc(client *postgres.Client, diags *diag.Diagnostics, resourceName string) postgres.RunFunc {
	if client == nil || client.SSH == nil {
		diags.AddError("postgres SSH transport not configured",
			resourceName+" requires the provider's ssh_host + ssh_key_file or ssh_key_pem.")
		return nil
	}
	return client.Run
}

// listToStrings decodes a types.List of strings (empty for null/unknown).
func listToStrings(ctx context.Context, l types.List) []string {
	var out []string
	if l.IsNull() || l.IsUnknown() {
		return out
	}
	_ = l.ElementsAs(ctx, &out, false)
	return out
}
