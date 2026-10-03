package tunnelnode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/zarkmakerburg/baft/internal/config"
)

// Discovery (HQ A2): a read-only inventory of what is on this node. It looks
// at the systemd unit directory for BAFT transport units, reads each unit's
// config and ownership marker, and reports facts. It never writes, restarts,
// adopts, converts or deletes anything, runs only `systemctl is-active`, and
// reports no file contents: only parsed, non-secret facts, digests and the
// ownership marker's fields. DISCOVERY != ADOPTION: classification (managed,
// unmanaged, conflict, ...) is done by BCC from these facts.

const (
	DiscoveryVersion   = 1
	maxDiscoveredUnits = 16
	maxUnitFileBytes   = 64 << 10
	maxConfigFileBytes = 1 << 20
	maxMarkerFileBytes = 16 << 10
	maxDiscoveryJSON   = 48 << 10
	maxDiscoveryString = 256
)

var discoverUnitRe = regexp.MustCompile(`^baft[A-Za-z0-9._@-]{0,58}\.service$`)

// DiscoveredMarker is the ownership marker as found; nothing is checked here.
type DiscoveredMarker struct {
	ManagedBy    string `json:"managed_by"`
	TunnelID     string `json:"tunnel_id"`
	Generation   int    `json:"generation"`
	Role         string `json:"role"`
	ConfigSHA256 string `json:"config_sha256"`
	UnitSHA256   string `json:"unit_sha256"`
	FileSHA256   string `json:"file_sha256"`
}

// DiscoveredInstance is one BAFT transport unit (and its config) on the node.
type DiscoveredInstance struct {
	Unit    string `json:"unit"`
	Primary bool   `json:"primary"` // the unit this agent manages
	Present bool   `json:"present"` // the unit file exists
	// Recognized: the unit runs `baft run --file <config>`.
	Recognized   bool   `json:"recognized"`
	Problem      string `json:"problem,omitempty"` // why it was not read or not recognized
	ServiceState string `json:"service_state,omitempty"`
	UnitSHA256   string `json:"unit_sha256,omitempty"`
	// UnitHeaderTunnel is the tunnel id in the `# baft-tunnel:` header, if any;
	// UnitHeaderManaged is whether the header says `# baft-managed: true`.
	UnitHeaderTunnel  string `json:"unit_header_tunnel,omitempty"`
	UnitHeaderManaged bool   `json:"unit_header_managed,omitempty"`

	ConfigPath    string `json:"config_path,omitempty"`
	ConfigPresent bool   `json:"config_present"`
	ConfigLoads   bool   `json:"config_loads"`
	ConfigSHA256  string `json:"config_sha256,omitempty"`
	ConfigRole    string `json:"config_role,omitempty"`
	Listen        string `json:"listen,omitempty"`
	PeerAddress   string `json:"peer_address,omitempty"`
	RouteID       string `json:"route_id,omitempty"`
	RouteListen   string `json:"route_listen,omitempty"`
	Target        string `json:"target,omitempty"`

	Marker        *DiscoveredMarker `json:"marker,omitempty"`
	MarkerProblem string            `json:"marker_problem,omitempty"`
}

// DiscoveryReport is the whole answer. It carries no timestamp, so two runs on
// an unchanged node are byte-identical.
type DiscoveryReport struct {
	Version        int                  `json:"version"`
	Service        string               `json:"service"`
	ConfigDir      string               `json:"config_dir"`
	UnitDir        string               `json:"unit_dir"`
	NodeGeneration int                  `json:"node_generation"`
	Instances      []DiscoveredInstance `json:"instances"`
	Truncated      bool                 `json:"truncated,omitempty"`
	Errors         []string             `json:"errors,omitempty"`
}

func clip(s string) string {
	if len(s) > maxDiscoveryString {
		return s[:maxDiscoveryString] + "…"
	}
	return s
}

// regularFile reads a bounded regular file without following a symlink.
func regularFile(path string, max int64) ([]byte, string) {
	st, err := os.Lstat(path)
	switch {
	case err != nil:
		return nil, "not readable: " + clip(err.Error())
	case st.Mode()&os.ModeSymlink != 0:
		return nil, "is a symlink; not followed"
	case !st.Mode().IsRegular():
		return nil, "is not a regular file"
	case st.Size() > max:
		return nil, "is larger than the discovery limit"
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, "not readable: " + clip(err.Error())
	}
	return b, ""
}

// Discover inventories the node. It changes nothing.
func (m *Manager) Discover(ctx context.Context) DiscoveryReport {
	m.mu.Lock()
	defer m.mu.Unlock()
	rep := DiscoveryReport{Version: DiscoveryVersion, Service: m.Service, ConfigDir: m.ConfigDir, UnitDir: m.UnitDir, NodeGeneration: m.readGeneration(), Instances: []DiscoveredInstance{}}
	primary := m.Service + ".service"

	names := map[string]bool{primary: true}
	entries, err := os.ReadDir(m.UnitDir)
	if err != nil {
		rep.Errors = append(rep.Errors, "unit directory: "+clip(err.Error()))
	}
	for _, e := range entries {
		n := e.Name()
		// The agent's own unit and anything not named like a BAFT unit are not tunnels.
		if discoverUnitRe.MatchString(n) && !strings.HasPrefix(n, "baft-agent") {
			names[n] = true
		}
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	for _, n := range sorted {
		if len(rep.Instances) >= maxDiscoveredUnits {
			rep.Truncated = true
			break
		}
		rep.Instances = append(rep.Instances, m.discoverUnit(ctx, n, n == primary))
	}
	// Keep the answer bounded, whatever the node holds.
	for {
		b, _ := json.Marshal(rep)
		if len(b) <= maxDiscoveryJSON || len(rep.Instances) <= 1 {
			break
		}
		rep.Instances = rep.Instances[:len(rep.Instances)-1]
		rep.Truncated = true
	}
	return rep
}

func (m *Manager) discoverUnit(ctx context.Context, name string, primary bool) DiscoveredInstance {
	in := DiscoveredInstance{Unit: name, Primary: primary}
	raw, problem := regularFile(filepath.Join(m.UnitDir, name), maxUnitFileBytes)
	if problem != "" {
		if _, err := os.Lstat(filepath.Join(m.UnitDir, name)); err == nil {
			in.Present, in.Problem = true, "unit file "+problem
		}
		return in
	}
	in.Present = true
	in.UnitSHA256 = shaHex(raw)
	if state, _ := m.System.Systemctl(ctx, "is-active", name); state != "" {
		in.ServiceState = clip(state)
	} else {
		in.ServiceState = "unknown"
	}
	var execStart string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "ExecStart=") && execStart == "":
			execStart = strings.TrimPrefix(line, "ExecStart=")
		case strings.HasPrefix(line, "# baft-managed:"):
			in.UnitHeaderManaged = strings.TrimSpace(strings.TrimPrefix(line, "# baft-managed:")) == "true"
		case strings.HasPrefix(line, "# baft-tunnel:"):
			in.UnitHeaderTunnel = clip(strings.TrimSpace(strings.TrimPrefix(line, "# baft-tunnel:")))
		}
	}
	f := strings.Fields(execStart)
	if len(f) != 4 || filepath.Base(f[0]) != "baft" || f[1] != "run" || f[2] != "--file" ||
		!filepath.IsAbs(f[3]) || strings.Contains(f[3], "..") || !(strings.HasSuffix(f[3], ".yaml") || strings.HasSuffix(f[3], ".yml") || strings.HasSuffix(f[3], ".json")) {
		in.Problem = "not a BAFT transport unit: ExecStart is not `baft run --file <absolute config path>`"
		return in
	}
	in.Recognized = true
	in.ConfigPath = clip(f[3])

	cfgRaw, problem := regularFile(f[3], maxConfigFileBytes)
	if problem != "" {
		in.Problem = "config " + problem
	} else {
		in.ConfigPresent = true
		in.ConfigSHA256 = shaHex(cfgRaw)
		if cfg, err := config.LoadFile(f[3]); err != nil {
			in.Problem = "config does not load as a BAFT configuration"
		} else {
			in.ConfigLoads = true
			in.ConfigRole = cfg.Node.Role
			if cfg.Server != nil {
				in.Listen = clip(cfg.Server.Listen)
			}
			if cfg.Peer != nil {
				in.PeerAddress = clip(cfg.Peer.Address)
			}
			if len(cfg.Routes) > 0 {
				in.RouteID, in.RouteListen, in.Target = clip(cfg.Routes[0].ID), clip(cfg.Routes[0].Listen), clip(cfg.Routes[0].Target)
			}
		}
	}
	// The marker lives next to the config.
	mpath := filepath.Join(filepath.Dir(f[3]), "baft.managed.json")
	if mraw, problem := regularFile(mpath, maxMarkerFileBytes); problem != "" {
		if _, err := os.Lstat(mpath); err == nil {
			in.MarkerProblem = "marker file " + problem
		}
	} else {
		var mk Marker
		if err := json.Unmarshal(mraw, &mk); err != nil {
			in.MarkerProblem = "marker file is not valid JSON"
		} else {
			in.Marker = &DiscoveredMarker{
				ManagedBy: clip(mk.ManagedBy), TunnelID: clip(mk.TunnelID), Generation: mk.Generation, Role: clip(mk.Role),
				ConfigSHA256: clip(mk.ConfigSHA256), UnitSHA256: clip(mk.UnitSHA256), FileSHA256: shaHex(mraw),
			}
		}
	}
	return in
}
