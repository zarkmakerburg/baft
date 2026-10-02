package bcc

import (
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zarkmakerburg/baft/internal/tunnelnode"
)

// Drift states. A node is IN_SYNC only when what it runs is exactly what BAFT
// installed for this tunnel; every other state names a specific way it is not.
const (
	DriftInSync    = "IN_SYNC"
	DriftDrifted   = "DRIFTED"   // BAFT-managed, but changed since BAFT wrote it
	DriftMissing   = "MISSING"   // config or unit is gone
	DriftUnmanaged = "UNMANAGED" // files exist but carry no BAFT ownership marker
	DriftUnknown   = "UNKNOWN"   // the node did not answer; nothing is claimed
)

const driftTimeout = 10 * time.Minute

// DriftNode is one node's result.
type DriftNode struct {
	State    string   `json:"state"`
	Problems []string `json:"problems,omitempty"`
}

// DriftReport is the outcome of one check of an active tunnel.
type DriftReport struct {
	State     string               `json:"state"`
	CheckedAt time.Time            `json:"checked_at"`
	Nodes     map[string]DriftNode `json:"nodes"`
}

func driftRank(s string) int {
	switch s {
	case DriftMissing:
		return 5
	case DriftDrifted:
		return 4
	case DriftUnmanaged:
		return 3
	case DriftUnknown:
		return 2
	}
	return 1
}

// classifyLive compares one node's live state with the active tunnel.
func classifyLive(t Tunnel, node, role string, l tunnelnode.Live) DriftNode {
	var miss, p []string
	if !l.ConfigPresent {
		miss = append(miss, "config file is missing")
	}
	if !l.UnitPresent {
		miss = append(miss, "service unit is missing")
	}
	if len(miss) > 0 {
		return DriftNode{State: DriftMissing, Problems: miss}
	}
	if !l.MarkerPresent || l.MarkerManagedBy != "baft" {
		return DriftNode{State: DriftUnmanaged, Problems: []string{"files carry no BAFT ownership marker"}}
	}
	bad := func(format string, a ...any) { p = append(p, fmt.Sprintf(format, a...)) }
	if l.MarkerTunnelID != t.ID {
		bad("marker belongs to tunnel %q, want %q", l.MarkerTunnelID, t.ID)
	}
	if want, ok := t.ObservedGen[node]; ok && l.MarkerGeneration != want {
		bad("marker generation %d, BCC verified %d", l.MarkerGeneration, want)
	}
	if l.NodeGeneration != l.MarkerGeneration {
		bad("node generation %d differs from marker generation %d", l.NodeGeneration, l.MarkerGeneration)
	}
	if !l.MarkerConfigMatches {
		bad("config changed since BAFT wrote it")
	}
	if !l.MarkerUnitMatches {
		bad("service unit changed since BAFT wrote it")
	}
	if !l.ConfigLoads {
		bad("config does not load")
	}
	if !l.ServiceActive {
		bad("service is not active")
	}
	if l.RouteID != t.RouteID {
		bad("route id is %q, want %q", l.RouteID, t.RouteID)
	}
	if role == tunnelnode.RoleIR {
		if l.ConfigRole != "dialer" {
			bad("config role is %q, want dialer", l.ConfigRole)
		}
		if l.RouteListen != t.RouteListen {
			bad("route listener is %q, want %q", l.RouteListen, t.RouteListen)
		}
		if want := net.JoinHostPort(t.PublicAddress, strconv.Itoa(t.Port)); l.PeerAddress != want {
			bad("peer address is %q, want %q", l.PeerAddress, want)
		}
	} else {
		if l.ConfigRole != "listener" {
			bad("config role is %q, want listener", l.ConfigRole)
		}
		if want := "0.0.0.0:" + strconv.Itoa(t.Port); l.Listen != want {
			bad("listener is %q, want %q", l.Listen, want)
		}
		if l.Target != t.Target {
			bad("target is %q, want %q", l.Target, t.Target)
		}
	}
	if len(p) > 0 {
		return DriftNode{State: DriftDrifted, Problems: p}
	}
	return DriftNode{State: DriftInSync}
}

// StartDrift asks both nodes of an active tunnel what they have. It is
// refused for any other phase and while a check is already running.
func (s *Store) StartDrift(id string, now time.Time) (Tunnel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.st.Tunnels[id]
	if !ok {
		return Tunnel{}, fmt.Errorf("unknown tunnel %q", id)
	}
	if err := s.startDriftLocked(&t, now.UTC()); err != nil {
		return Tunnel{}, err
	}
	s.st.Tunnels[id] = t
	return t, s.saveLocked()
}

func (s *Store) startDriftLocked(t *Tunnel, now time.Time) error {
	if t.Phase != TunnelActive {
		return fmt.Errorf("tunnel %s is %s; only an active tunnel can be checked", t.ID, t.Phase)
	}
	if len(t.DriftJobs) > 0 {
		return fmt.Errorf("a drift check of %s is already running", t.ID)
	}
	for _, node := range []string{t.IRNode, t.EXNode} {
		j := s.newJobLocked(Job{Type: JobTunnelInspect, NodeID: node, Params: map[string]string{}})
		t.DriftJobs = append(t.DriftJobs, j.ID)
	}
	t.DriftStarted = now
	return nil
}

// advanceDriftLocked starts due automatic checks and evaluates finished ones.
// It returns an event when the tunnel's drift state changed.
func (s *Store) advanceDriftLocked(t *Tunnel, now time.Time) *TunnelEvent {
	if t.Phase != TunnelActive {
		if len(t.DriftJobs) > 0 {
			t.DriftJobs = nil
		}
		return nil
	}
	if len(t.DriftJobs) == 0 {
		if s.DriftEvery > 0 && (t.Drift == nil || now.Sub(t.Drift.CheckedAt) >= s.DriftEvery) {
			_ = s.startDriftLocked(t, now)
		}
		return nil
	}
	pending := 0
	for _, id := range t.DriftJobs {
		if j := s.st.Jobs[id]; j.Status == "queued" || j.Status == "dispatched" {
			pending++
		}
	}
	if pending > 0 && now.Sub(t.DriftStarted) <= driftTimeout {
		return nil
	}
	rep := &DriftReport{CheckedAt: now, Nodes: map[string]DriftNode{}}
	for _, id := range t.DriftJobs {
		j := s.st.Jobs[id]
		role := tunnelnode.RoleEX
		if j.NodeID == t.IRNode {
			role = tunnelnode.RoleIR
		}
		switch {
		case j.Status == "queued" || j.Status == "dispatched":
			rep.Nodes[j.NodeID] = DriftNode{State: DriftUnknown, Problems: []string{"the node did not answer in time"}}
		case j.Status == "failed":
			rep.Nodes[j.NodeID] = DriftNode{State: DriftUnknown, Problems: []string{"inspect failed: " + j.Message}}
		default:
			var l tunnelnode.Live
			if err := json.Unmarshal([]byte(j.Message), &l); err != nil {
				rep.Nodes[j.NodeID] = DriftNode{State: DriftUnknown, Problems: []string{"the node's report is unreadable"}}
			} else {
				rep.Nodes[j.NodeID] = classifyLive(*t, j.NodeID, role, l)
			}
		}
	}
	rep.State = DriftInSync
	names := make([]string, 0, len(rep.Nodes))
	for n := range rep.Nodes {
		names = append(names, n)
	}
	sort.Strings(names)
	var detail []string
	for _, n := range names {
		r := rep.Nodes[n]
		if driftRank(r.State) > driftRank(rep.State) {
			rep.State = r.State
		}
		if r.State != DriftInSync {
			detail = append(detail, n+": "+r.State+" ("+strings.Join(r.Problems, "; ")+")")
		}
	}
	prev := ""
	if t.Drift != nil {
		prev = t.Drift.State
	}
	t.Drift, t.DriftJobs, t.DriftStarted = rep, nil, time.Time{}
	if rep.State == prev {
		return nil
	}
	if rep.State == DriftInSync {
		return &TunnelEvent{TunnelID: t.ID, Action: "tunnel.in_sync", Detail: "all nodes match what BAFT installed"}
	}
	return &TunnelEvent{TunnelID: t.ID, Action: "tunnel.drift", Detail: rep.State + ": " + strings.Join(detail, "; ")}
}
