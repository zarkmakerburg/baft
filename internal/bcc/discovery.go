package bcc

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/zarkmakerburg/baft/internal/tunnelnode"
)

// Existing-tunnel discovery (HQ A2), report only.
//
//	BCC -> signed read-only job -> agent -> inspect -> observation -> BCC inventory
//
// DISCOVERY != ADOPTION. Nothing here changes a node or BCC's ownership
// records: the agent only reads, and BCC only classifies and stores what it
// was told. There is no adopt, overwrite, delete, restart, marker creation or
// config conversion anywhere in this path; the states below are information.
//
//	MANAGED              BAFT-owned, matches the digests BCC verified for an active tunnel
//	DRIFTED              BAFT-owned (marker agrees with BCC) but changed since
//	MISSING              BCC records an active tunnel but the node has no such unit/config
//	DISCOVERED_UNMANAGED a BAFT unit and config with no ownership marker
//	OWNERSHIP_CONFLICT   contradictory ownership claims (foreign or unknown marker,
//	                     marker role/tunnel contradicting BCC, unit header vs marker, ...)
//	UNKNOWN              not understood, unreadable, or not provable; never touched

const (
	DiscManaged   = "MANAGED"
	DiscUnmanaged = "DISCOVERED_UNMANAGED"
	DiscDrifted   = "DRIFTED"
	DiscMissing   = "MISSING"
	DiscConflict  = "OWNERSHIP_CONFLICT"
	DiscUnknown   = "UNKNOWN"

	discoveryTimeout = 10 * time.Minute
)

// DiscoveredView is one instance as classified by BCC.
type DiscoveredView struct {
	Unit         string   `json:"unit"`
	Primary      bool     `json:"primary"`
	State        string   `json:"state"`
	Tunnel       string   `json:"tunnel,omitempty"` // tunnel id claimed by the ownership marker
	Reasons      []string `json:"reasons,omitempty"`
	ConfigPath   string   `json:"config_path,omitempty"`
	ServiceState string   `json:"service_state,omitempty"`
	ConfigRole   string   `json:"config_role,omitempty"`
	Listen       string   `json:"listen,omitempty"`
	PeerAddress  string   `json:"peer_address,omitempty"`
	RouteID      string   `json:"route_id,omitempty"`
	RouteListen  string   `json:"route_listen,omitempty"`
	Target       string   `json:"target,omitempty"`
}

// NodeDiscovery is the stored result for one node.
type NodeDiscovery struct {
	NodeID    string           `json:"node_id"`
	At        time.Time        `json:"at,omitempty"`
	JobID     string           `json:"job_id,omitempty"`
	Pending   bool             `json:"pending,omitempty"`
	Started   time.Time        `json:"started,omitempty"`
	Problem   string           `json:"problem,omitempty"`
	Instances []DiscoveredView `json:"instances,omitempty"`
	Summary   map[string]int   `json:"summary,omitempty"`
	Truncated bool             `json:"truncated,omitempty"`
	Errors    []string         `json:"errors,omitempty"`
}

// classifyDiscovery turns the agent's facts into states using BCC's own
// inventory of tunnels. It is a pure function.
func classifyDiscovery(node Node, tunnels map[string]Tunnel, rep tunnelnode.DiscoveryReport) []DiscoveredView {
	var activeHere *Tunnel
	for _, t := range tunnels {
		t := t
		if t.Phase == TunnelActive && (t.EXNode == node.ID || t.IRNode == node.ID) {
			activeHere = &t
		}
	}
	roleIn := func(t Tunnel) string {
		if t.EXNode == node.ID {
			return tunnelnode.RoleEX
		}
		return tunnelnode.RoleIR
	}
	out := make([]DiscoveredView, 0, len(rep.Instances)+1)
	primarySeen := false
	for _, in := range rep.Instances {
		v := DiscoveredView{
			Unit: in.Unit, Primary: in.Primary, ConfigPath: in.ConfigPath, ServiceState: in.ServiceState,
			ConfigRole: in.ConfigRole, Listen: in.Listen, PeerAddress: in.PeerAddress, RouteID: in.RouteID, RouteListen: in.RouteListen, Target: in.Target,
		}
		if in.Marker != nil {
			v.Tunnel = in.Marker.TunnelID
		}
		why := func(format string, a ...any) { v.Reasons = append(v.Reasons, fmt.Sprintf(format, a...)) }
		switch {
		case in.Primary && !in.Present:
			// Handled below as MISSING (or nothing, if BCC expects nothing here).
			continue
		case !in.Present:
			continue
		case in.Primary:
			primarySeen = true
			fallthrough
		default:
			v.State = classifyInstance(node, in, tunnels, activeHere, roleIn, rep, why)
		}
		out = append(out, v)
	}
	if activeHere != nil {
		// A primary unit that is present but unreadable or unrecognized was
		// classified UNKNOWN above; only an absent one is MISSING.
		missing := !primarySeen
		if missing {
			out = append(out, DiscoveredView{
				Unit: rep.Service + ".service", Primary: true, State: DiscMissing, Tunnel: activeHere.ID,
				Reasons: []string{fmt.Sprintf("BCC records tunnel %s as active on this node, but there is no %s.service unit file", activeHere.ID, rep.Service)},
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Unit < out[j].Unit })
	return out
}

func classifyInstance(node Node, in tunnelnode.DiscoveredInstance, tunnels map[string]Tunnel, activeHere *Tunnel,
	roleIn func(Tunnel) string, rep tunnelnode.DiscoveryReport, why func(string, ...any)) string {

	if !in.Recognized {
		why("%s", in.Problem)
		return DiscUnknown
	}
	if !in.ConfigPresent || !in.ConfigLoads {
		why("%s", orStr(in.Problem, "the config could not be read"))
		return DiscUnknown
	}
	m := in.Marker
	if m == nil {
		if in.MarkerProblem != "" {
			why("the ownership marker cannot be read: %s", in.MarkerProblem)
			return DiscUnknown
		}
		if in.UnitHeaderManaged || in.UnitHeaderTunnel != "" {
			why("the unit says it is BAFT-managed (tunnel %q) but there is no ownership marker", in.UnitHeaderTunnel)
			return DiscConflict
		}
		if in.Primary && activeHere != nil {
			why("BCC records tunnel %s as active here, but the files carry no ownership marker", activeHere.ID)
			return DiscConflict
		}
		why("a BAFT unit and a valid config with no ownership marker: not created or claimed by BAFT")
		return DiscUnmanaged
	}
	if m.ManagedBy != "baft" {
		why("the marker is claimed by %q, not by baft", m.ManagedBy)
		return DiscConflict
	}
	if in.UnitHeaderTunnel != "" && in.UnitHeaderTunnel != m.TunnelID {
		why("the unit header names tunnel %s but the marker names %s", in.UnitHeaderTunnel, m.TunnelID)
		return DiscConflict
	}
	t, known := tunnels[m.TunnelID]
	switch {
	case !known:
		why("the marker names tunnel %s, which BCC does not know", m.TunnelID)
		return DiscConflict
	case t.EXNode != node.ID && t.IRNode != node.ID:
		why("the marker names tunnel %s, which is not a tunnel of this node", m.TunnelID)
		return DiscConflict
	}
	if want := roleIn(t); m.Role != want {
		why("the marker says role %q but BCC has this node as %q in tunnel %s", m.Role, want, t.ID)
		return DiscConflict
	}
	if !terminalTunnel(t.Phase) {
		why("tunnel %s is being changed by BCC (%s); nothing is concluded until it settles", t.ID, t.Phase)
		return DiscUnknown
	}
	if t.Phase != TunnelActive {
		why("BCC records tunnel %s as %s, not active, yet the files still carry its marker", t.ID, t.Phase)
		return DiscConflict
	}
	if !in.Primary {
		why("a second unit shares the ownership marker of tunnel %s with %s.service", t.ID, rep.Service)
		return DiscConflict
	}
	// Same owner on both sides: BCC's own drift rules decide MANAGED or DRIFTED,
	// against the digests BCC verified, not the ones the marker holds.
	live := tunnelnode.Live{
		ConfigPresent: true, UnitPresent: true, ConfigSHA256: in.ConfigSHA256, ConfigLoads: true,
		MarkerPresent: true, MarkerManagedBy: m.ManagedBy, MarkerTunnelID: m.TunnelID, MarkerGeneration: m.Generation,
		MarkerConfigMatches: m.ConfigSHA256 == in.ConfigSHA256, MarkerUnitMatches: m.UnitSHA256 == in.UnitSHA256,
		NodeGeneration: rep.NodeGeneration, ServiceActive: in.ServiceState == "active",
		UnitSHA256: in.UnitSHA256, MarkerSHA256: m.FileSHA256,
		ConfigRole: in.ConfigRole, Listen: in.Listen, PeerAddress: in.PeerAddress, RouteID: in.RouteID, RouteListen: in.RouteListen, Target: in.Target,
	}
	dn := classifyLive(t, node.ID, roleIn(t), live)
	switch dn.State {
	case DriftInSync:
		why("matches what BCC verified for tunnel %s", t.ID)
		return DiscManaged
	case DriftDrifted:
		for _, p := range dn.Problems {
			why("%s", p)
		}
		return DiscDrifted
	}
	for _, p := range dn.Problems {
		why("%s", p)
	}
	return DiscUnknown
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func summarizeViews(vs []DiscoveredView) map[string]int {
	m := map[string]int{}
	for _, v := range vs {
		m[v.State]++
	}
	return m
}

// ---- store ----

// StartDiscovery queues a read-only discovery job for a node. It is refused
// for an unknown or revoked node and while one is already running.
func (s *Store) StartDiscovery(nodeID string, now time.Time) (NodeDiscovery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.st.Nodes[nodeID]
	if !ok {
		return NodeDiscovery{}, fmt.Errorf("unknown node %q", nodeID)
	}
	if n.Revoked {
		return NodeDiscovery{}, fmt.Errorf("node %q is revoked", nodeID)
	}
	if s.st.Discovery == nil {
		s.st.Discovery = map[string]NodeDiscovery{}
	}
	d := s.st.Discovery[nodeID]
	if d.Pending && now.Sub(d.Started) <= discoveryTimeout {
		return NodeDiscovery{}, fmt.Errorf("a discovery of %q is already running", nodeID)
	}
	j := s.newJobLocked(Job{Type: JobTunnelDiscover, NodeID: nodeID, Params: map[string]string{}})
	d.NodeID, d.Pending, d.JobID, d.Started = nodeID, true, j.ID, now.UTC()
	s.st.Discovery[nodeID] = d
	return d, s.saveLocked()
}

// DiscoveryEvent is a finished discovery, for the audit log.
type DiscoveryEvent struct {
	NodeID  string
	Summary map[string]int
	States  map[string]string // unit -> state
	Changed bool
	Problem string
}

// AdvanceDiscovery completes discoveries whose job finished or timed out.
func (s *Store) AdvanceDiscovery(now time.Time) ([]DiscoveryEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.st.Discovery))
	for id, d := range s.st.Discovery {
		if d.Pending {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	var events []DiscoveryEvent
	for _, id := range ids {
		d := s.st.Discovery[id]
		j, ok := s.st.Jobs[d.JobID]
		prevStates := stateMap(d.Instances)
		finish := func(next NodeDiscovery) {
			next.NodeID, next.JobID, next.At, next.Pending, next.Started = id, d.JobID, now.UTC(), false, time.Time{}
			ev := DiscoveryEvent{NodeID: id, Summary: next.Summary, States: stateMap(next.Instances), Problem: next.Problem}
			ev.Changed = !equalStateMaps(prevStates, ev.States) || next.Problem != d.Problem
			s.st.Discovery[id] = next
			events = append(events, ev)
		}
		switch {
		case !ok:
			finish(NodeDiscovery{Problem: "internal error: the discovery job is missing"})
		case j.Status == "queued" || j.Status == "dispatched":
			if now.Sub(d.Started) > discoveryTimeout {
				finish(NodeDiscovery{Problem: "the node did not answer in time", Instances: d.Instances})
			}
		case j.Status == "failed":
			finish(NodeDiscovery{Problem: "discovery failed on the node: " + j.Message})
		default:
			var rep tunnelnode.DiscoveryReport
			if err := json.Unmarshal([]byte(j.Message), &rep); err != nil || rep.Version != tunnelnode.DiscoveryVersion {
				finish(NodeDiscovery{Problem: "the node's discovery report is unreadable"})
				continue
			}
			views := classifyDiscovery(s.st.Nodes[id], s.st.Tunnels, rep)
			finish(NodeDiscovery{Instances: views, Summary: summarizeViews(views), Truncated: rep.Truncated, Errors: rep.Errors})
		}
	}
	if len(events) == 0 {
		return nil, nil
	}
	return events, s.saveLocked()
}

func stateMap(vs []DiscoveredView) map[string]string {
	m := map[string]string{}
	for _, v := range vs {
		m[v.Unit] = v.State
	}
	return m
}

func equalStateMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// DiscoverySnapshot returns the stored discoveries (one node, or all).
func (s *Store) DiscoverySnapshot(only string) []NodeDiscovery {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []NodeDiscovery
	for id := range s.st.Nodes {
		if only != "" && id != only {
			continue
		}
		d, ok := s.st.Discovery[id]
		if !ok {
			d = NodeDiscovery{NodeID: id}
		}
		d.Instances = append([]DiscoveredView(nil), d.Instances...)
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

// ---- server ----

// discoveryAPI: GET /api/discovery[?node=ID] shows the stored inventory;
// POST /api/discovery?node=ID (or ?all=1) starts a read-only discovery. Both
// are admin-only; neither takes a path or any other input from the caller, and
// nothing here adopts, changes or removes anything on a node.
func (s *Server) discoveryAPI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !s.admin(w, r) {
			return
		}
		writeJSON(w, 200, map[string]any{"nodes": s.store.DiscoverySnapshot(r.URL.Query().Get("node"))})
	case http.MethodPost:
		if !s.admin(w, r) {
			return
		}
		s.mutationMu.Lock()
		defer s.mutationMu.Unlock()
		var ids []string
		if r.URL.Query().Get("all") == "1" {
			for _, n := range s.store.ListNodes() {
				if !n.Revoked {
					ids = append(ids, n.ID)
				}
			}
			sort.Strings(ids)
		} else if id := strings.TrimSpace(r.URL.Query().Get("node")); id != "" {
			ids = []string{id}
		} else {
			http.Error(w, "node or all=1 is required", 400)
			return
		}
		var started []NodeDiscovery
		for _, id := range ids {
			d, err := s.store.StartDiscovery(id, s.now())
			if err != nil {
				if len(ids) == 1 {
					s.auditFailure(w, r, "discovery.start", id, nil, err, 400)
					return
				}
				continue
			}
			if err := s.auditAdmin(r, "discovery.start", id, "success", map[string]any{"job_id": d.JobID}); err != nil {
				http.Error(w, "audit log failure", 500)
				return
			}
			started = append(started, d)
		}
		writeJSON(w, 202, map[string]any{"started": started})
	default:
		http.Error(w, "method not allowed", 405)
	}
}

// auditDiscovery records finished discoveries.
func (s *Server) auditDiscovery(events []DiscoveryEvent) {
	for _, e := range events {
		outcome := "success"
		if e.Problem != "" {
			outcome = "failure"
		}
		_, _ = s.audit.Append(AuditEntry{
			Timestamp: s.now().UTC(), Actor: "bcc", Action: "discovery.completed", Target: e.NodeID, Outcome: outcome,
			Details: map[string]any{"summary": e.Summary, "states": e.States, "changed": e.Changed, "problem": e.Problem},
		})
	}
}
