// SPDX-License-Identifier: AGPL-3.0-or-later

package postgres

import "testing"

func TestBuildPrimaryConninfo(t *testing.T) {
	cases := []struct {
		name string
		in   Conninfo
		want string
	}{
		{
			name: "full",
			in:   Conninfo{Host: "10.0.0.1", Port: 5432, User: "repl", Password: "pw", ApplicationName: "node2", SSLMode: "require"},
			want: "host=10.0.0.1 port=5432 user=repl password=pw application_name=node2 sslmode=require",
		},
		{
			name: "default port and sparse",
			in:   Conninfo{Host: "db-primary", User: "repl"},
			want: "host=db-primary port=5432 user=repl",
		},
		{
			name: "repmgr with dbname",
			in:   Conninfo{Host: "n1", Port: 5432, User: "repmgr", DBName: "repmgr"},
			want: "host=n1 port=5432 user=repmgr dbname=repmgr",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := BuildPrimaryConninfo(tc.in); got != tc.want {
				t.Fatalf("BuildPrimaryConninfo = %q, want %q", got, tc.want)
			}
		})
	}
}
