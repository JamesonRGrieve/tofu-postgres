// SPDX-License-Identifier: AGPL-3.0-or-later

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestValidateCluster(t *testing.T) {
	cases := []struct {
		name    string
		model   clusterModel
		wantErr bool
	}{
		{"streaming ok", clusterModel{HAMode: types.StringValue("streaming")}, false},
		{"repmgr ok", clusterModel{HAMode: types.StringValue("repmgr")}, false},
		{"patroni needs dcs", clusterModel{HAMode: types.StringValue("patroni")}, true},
		{"patroni with dcs ok", clusterModel{HAMode: types.StringValue("patroni"), DCSReference: types.StringValue("10.0.0.10:2379")}, false},
		{"unknown mode", clusterModel{HAMode: types.StringValue("galera")}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diags := validateCluster(tc.model)
			if diags.HasError() != tc.wantErr {
				t.Fatalf("validateCluster(%q) HasError=%v, want %v: %v", tc.model.HAMode.ValueString(), diags.HasError(), tc.wantErr, diags)
			}
		})
	}
}
