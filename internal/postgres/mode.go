// SPDX-License-Identifier: AGPL-3.0-or-later

package postgres

import "fmt"

// HAMode selects the high-availability topology a postgres_cluster manages. All
// three are mode-selectable at the cluster level; the node resource dispatches
// its bring-up on the same mode.
type HAMode string

const (
	ModeStreaming HAMode = "streaming" // plain physical streaming replication
	ModeRepmgr    HAMode = "repmgr"    // repmgr-managed streaming + repmgrd failover
	ModePatroni   HAMode = "patroni"   // Patroni + a DCS (etcd/consul)
)

// Valid reports whether m is a recognized HA mode.
func (m HAMode) Valid() bool {
	switch m {
	case ModeStreaming, ModeRepmgr, ModePatroni:
		return true
	default:
		return false
	}
}

// NodeRole is a node's role within the cluster.
type NodeRole string

const (
	RolePrimary NodeRole = "primary"
	RoleReplica NodeRole = "replica"
	RoleWitness NodeRole = "witness"
)

// Valid reports whether r is a recognized role.
func (r NodeRole) Valid() bool {
	switch r {
	case RolePrimary, RoleReplica, RoleWitness:
		return true
	default:
		return false
	}
}

// NodeSpec is the mode-agnostic description of one cluster node. NodeCommands
// dispatches it to the right per-mode command builder. Keeping this a plain
// struct (no framework types) is what makes the whole dispatch unit-testable.
type NodeSpec struct {
	Mode        HAMode
	Role        NodeRole
	Version     string // PG major (e.g. "16")
	Cluster     string // Debian cluster name (e.g. "main")
	ClusterName string // logical HA cluster name (Patroni scope / repmgr cluster)
	NodeName    string
	NodeID      int

	Host string // this node's routable address (for its own conninfo)
	Port int

	PrimaryHost string // the primary this replica follows
	PrimaryPort int

	ReplicationUser     string
	ReplicationPassword string
	ReplicationSlot     string
	Synchronous         bool

	// Patroni-only
	DCSReference   string
	DCS            DCSType
	RestAPIListen  string
	RestAPIConnect string
	PGListen       string
	PGConnect      string
	BinDir         string
	SuperUser      string
	SuperPassword  string
	PgHbaCIDR      string // patroni: node subnet allowed in pg_hba for replication + access
}

// NodeCommands returns the ordered bring-up commands for a node, dispatched by
// (mode, role). An unknown/invalid mode or role is an error rather than a silent
// no-op. Real bring-up (promotion/failover/base-backup) is interruption-unsafe
// and verification-owed — see DESIGN.md; this returns the deterministic command
// sequence the resource executes via an injected RunFunc.
func NodeCommands(s NodeSpec) ([]Command, error) {
	if !s.Mode.Valid() {
		return nil, fmt.Errorf("unknown ha_mode %q", s.Mode)
	}
	if !s.Role.Valid() {
		return nil, fmt.Errorf("unknown role %q", s.Role)
	}
	switch s.Mode {
	case ModeStreaming:
		return streamingNodeCommands(s)
	case ModeRepmgr:
		return repmgrNodeCommands(s)
	case ModePatroni:
		return patroniNodeCommands(s)
	default:
		return nil, fmt.Errorf("unhandled ha_mode %q", s.Mode)
	}
}

func streamingNodeCommands(s NodeSpec) ([]Command, error) {
	switch s.Role {
	case RolePrimary:
		var slots []string
		if s.ReplicationSlot != "" {
			slots = []string{s.ReplicationSlot}
		}
		return StreamingPrimaryCommands(StreamingPrimaryParams{
			Version: s.Version, Cluster: s.Cluster, Slots: slots,
			ReplicationUser: s.ReplicationUser, ReplicationPassword: s.ReplicationPassword,
		}), nil
	case RoleReplica:
		conninfo := BuildPrimaryConninfo(Conninfo{
			Host: s.PrimaryHost, Port: s.PrimaryPort,
			User: s.ReplicationUser, Password: s.ReplicationPassword,
			ApplicationName: s.NodeName,
		})
		return StreamingStandbyCommands(StreamingStandbyParams{
			Version: s.Version, Cluster: s.Cluster, Conninfo: conninfo, Slot: s.ReplicationSlot,
			PrimaryHost: s.PrimaryHost, PrimaryPort: s.PrimaryPort,
			ReplicationUser: s.ReplicationUser, ReplicationPassword: s.ReplicationPassword,
		}), nil
	default:
		return nil, fmt.Errorf("streaming does not support role %q", s.Role)
	}
}

func repmgrNodeCommands(s NodeSpec) ([]Command, error) {
	user := orDefault(s.ReplicationUser, "repmgr")
	confPath := RepmgrConfPath(s.Version, s.Cluster)
	selfConninfo := BuildPrimaryConninfo(Conninfo{
		Host: s.Host, Port: s.Port, User: user, DBName: "repmgr",
	})
	conf := RenderRepmgrConf(RepmgrConfParams{
		NodeID: s.NodeID, NodeName: s.NodeName, Conninfo: selfConninfo,
		DataDir: DataDir(s.Version, s.Cluster), ReplicationUser: user,
		ConfPath: confPath, PGBinDir: PGBinDir(s.Version),
	})
	// Every repmgr node needs the repmgr package before any repmgr/repmgrd call.
	preamble := []Command{RepmgrInstallCommand(s.Version)}
	switch s.Role {
	case RolePrimary:
		return append(preamble, RepmgrPrimaryCommands(RepmgrPrimaryParams{
			Version: s.Version, Cluster: s.Cluster, ConfPath: confPath, Conf: conf,
			SelfHost: s.Host, SelfPort: s.Port,
			ReplUser: user, ReplPassword: s.ReplicationPassword,
		})...), nil
	case RoleReplica:
		return append(preamble, RepmgrStandbyCommands(RepmgrStandbyParams{
			Version: s.Version, Cluster: s.Cluster, ConfPath: confPath, Conf: conf,
			PrimaryHost: s.PrimaryHost, PrimaryPort: s.PrimaryPort,
			ReplUser: user, ReplPassword: s.ReplicationPassword,
		})...), nil
	case RoleWitness:
		return append(preamble, RepmgrWitnessCommands(RepmgrStandbyParams{
			Version: s.Version, Cluster: s.Cluster, ConfPath: confPath, Conf: conf,
			PrimaryHost: s.PrimaryHost, PrimaryPort: s.PrimaryPort,
			ReplUser: user, ReplPassword: s.ReplicationPassword,
		})...), nil
	default:
		return nil, fmt.Errorf("repmgr does not support role %q", s.Role)
	}
}

func patroniNodeCommands(s NodeSpec) ([]Command, error) {
	// Patroni self-elects the leader via the DCS; role is advisory (it does not
	// change the config a node ships with).
	yaml := RenderPatroniYAML(PatroniParams{
		Scope: s.ClusterName, NodeName: s.NodeName, DCS: s.DCS, DCSHosts: s.DCSReference,
		RestAPIListen: orDefault(s.RestAPIListen, "0.0.0.0:8008"), RestAPIConnect: s.RestAPIConnect,
		PGListen: orDefault(s.PGListen, "0.0.0.0:5432"), PGConnect: s.PGConnect,
		DataDir: DataDir(s.Version, s.Cluster), BinDir: orDefault(s.BinDir, PGBinDir(s.Version)),
		Synchronous: s.Synchronous,
		ReplUser:    s.ReplicationUser, ReplPassword: s.ReplicationPassword,
		SuperUser: s.SuperUser, SuperPassword: s.SuperPassword,
		PgHbaCIDR: s.PgHbaCIDR,
	})
	return PatroniCommands(PatroniNodeParams{
		Version: s.Version, Cluster: s.Cluster, ClusterName: s.ClusterName,
		DataDir: DataDir(s.Version, s.Cluster), YAML: yaml, DCS: s.DCS,
	}), nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func orInt(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}
