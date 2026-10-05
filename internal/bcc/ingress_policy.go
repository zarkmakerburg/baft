package bcc

import (
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/zarkmakerburg/baft/internal/telemetry"
)

const (
	IngressReady         = "READY"
	IngressEXUnavailable = "EX_UNAVAILABLE"
	IngressNoHealthyIR   = "NO_HEALTHY_IR"
	IngressUnknown       = "UNKNOWN"
)

// IngressSelection is the durable routing decision for one explicit EX route.
// EX identity is part of the record and is never substituted by this policy.
type IngressSelection struct {
	RouteID     string    `json:"route_id"`
	EXNode      string    `json:"ex_node"`
	State       string    `json:"state"`
	ActiveIR    string    `json:"active_ir,omitempty"`
	Generation uint64    `json:"generation"`
	Reason      string    `json:"reason,omitempty"`
	ChangedAt   time.Time `json:"changed_at,omitempty"`
	EvaluatedAt time.Time `json:"evaluated_at,omitempty"`
}

// IngressCandidate explains why one IR is or is not eligible for a route.
type IngressCandidate struct {
	IRNode                     string   `json:"ir_node"`
	Eligible                   bool     `json:"eligible"`
	Score                      int      `json:"score"`
	Health                     string   `json:"health"`
	TopologyStatus             string   `json:"topology_status"`
	RouteStatus                string   `json:"route_status,omitempty"`
	LatencyMS                  int64    `json:"latency_ms"`
	RouteLatencyMS             int64    `json:"route_latency_ms,omitempty"`
	NoiseLatencyMS             int64    `json:"noise_latency_ms,omitempty"`
	HandshakeErrorRateMilliMin int64    `json:"handshake_error_rate_milli_per_min,omitempty"`
	Reasons                    []string `json:"reasons,omitempty"`
}

// IngressDecision is the operator-facing selection plus current candidate
// evidence. StandbyIRs are ordered by current candidate score.
type IngressDecision struct {
	IngressSelection
	// Usable is true only when the persisted ACTIVE selection is still backed
	// by current healthy/fresh evidence. Consumers such as Smart Ingress must
	// fail closed when it is false, even before the next evaluation cycle.
	Usable     bool               `json:"usable"`
	StandbyIRs []string           `json:"standby_irs,omitempty"`
	Candidates []IngressCandidate `json:"candidates,omitempty"`
}

// IngressEvent records only a material selection/state change. Re-evaluating
// identical evidence does not advance the generation.
type IngressEvent struct {
	RouteID    string `json:"route_id"`
	EXNode     string `json:"ex_node"`
	FromState  string `json:"from_state,omitempty"`
	ToState    string `json:"to_state"`
	FromIR     string `json:"from_ir,omitempty"`
	ToIR       string `json:"to_ir,omitempty"`
	Generation uint64 `json:"generation"`
	Reason     string `json:"reason"`
}

type IngressPolicy struct {
	AgentStaleAfter     time.Duration
	TelemetryStaleAfter time.Duration
	ProbeStaleAfter     time.Duration
}

func DefaultIngressPolicy() IngressPolicy {
	return IngressPolicy{
		AgentStaleAfter:     3 * time.Minute,
		TelemetryStaleAfter: 3 * time.Minute,
		ProbeStaleAfter:     2 * time.Minute,
	}
}

// EvaluateIngress evaluates every enabled explicit EX route and persists one
// ACTIVE IR (M-015) or a fail-closed unavailable state. An existing eligible
// ACTIVE IR is sticky: score changes alone do not cause failback/flapping.
func (s *Store) EvaluateIngress(now time.Time, p IngressPolicy) ([]IngressEvent, error) {
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.IngressSelections == nil {
		s.st.IngressSelections = map[string]IngressSelection{}
	}
	before := cloneIngressSelections(s.st.IngressSelections)
	ids := make([]string, 0, len(s.st.EXRoutes))
	for id, r := range s.st.EXRoutes {
		if r.Enabled {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	var events []IngressEvent
	for _, id := range ids {
		route := s.st.EXRoutes[id]
		old := s.st.IngressSelections[id]
		next, _ := s.chooseIngressLocked(route, old, now, p)
		next.EvaluatedAt = now
		if old.RouteID == "" {
			old.State = IngressUnknown
		}
		material := old.RouteID == "" || old.State != next.State || old.ActiveIR != next.ActiveIR || old.EXNode != next.EXNode
		if material {
			next.Generation = old.Generation + 1
			if next.Generation == 0 {
				next.Generation = 1
			}
			next.ChangedAt = now
			events = append(events, IngressEvent{
				RouteID: id, EXNode: route.EXNode,
				FromState: old.State, ToState: next.State, FromIR: old.ActiveIR, ToIR: next.ActiveIR,
				Generation: next.Generation, Reason: next.Reason,
			})
		} else {
			next.Generation = old.Generation
			next.ChangedAt = old.ChangedAt
		}
		s.st.IngressSelections[id] = next
	}
	if err := s.saveLocked(); err != nil {
		s.st.IngressSelections = before
		return nil, err
	}
	return events, nil
}

func cloneIngressSelections(in map[string]IngressSelection) map[string]IngressSelection {
	out := make(map[string]IngressSelection, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// IngressSnapshot returns persisted decisions plus current diagnostic evidence.
// It does not change selection or generation.
func (s *Store) IngressSnapshot(now time.Time) []IngressDecision {
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.st.EXRoutes))
	for id, r := range s.st.EXRoutes {
		if r.Enabled {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	out := make([]IngressDecision, 0, len(ids))
	for _, id := range ids {
		r := s.st.EXRoutes[id]
		sel := s.st.IngressSelections[id]
		if sel.RouteID == "" {
			sel = IngressSelection{RouteID: id, EXNode: r.EXNode, State: IngressUnknown}
		}
		_, candidates := s.chooseIngressLocked(r, sel, now, DefaultIngressPolicy())
		exOK, _ := s.nodeEligibleForIngressLocked(r.EXNode, now, DefaultIngressPolicy())
		usable := sel.State == IngressReady && exOK
		if usable {
			usable = false
			for _, c := range candidates {
				if c.IRNode == sel.ActiveIR && c.Eligible {
					usable = true
					break
				}
			}
		}
		out = append(out, IngressDecision{
			IngressSelection: sel,
			Usable:           usable,
			StandbyIRs:       standbyOrder(candidates, sel.ActiveIR),
			Candidates:       candidates,
		})
	}
	return out
}

func (s *Store) chooseIngressLocked(route ExplicitEXRoute, old IngressSelection, now time.Time, p IngressPolicy) (IngressSelection, []IngressCandidate) {
	next := IngressSelection{RouteID: route.ID, EXNode: route.EXNode}
	exOK, exReason := s.nodeEligibleForIngressLocked(route.EXNode, now, p)
	candidates := s.ingressCandidatesLocked(route, now, p)

	if !exOK {
		next.State, next.Reason = IngressEXUnavailable, exReason
		return next, candidates
	}
	var eligible []IngressCandidate
	for _, c := range candidates {
		if c.Eligible {
			eligible = append(eligible, c)
		}
	}
	if len(eligible) == 0 {
		next.State, next.Reason = IngressNoHealthyIR, "no IR has an ACTIVE, healthy, fresh edge to this EX"
		return next, candidates
	}

	// Keep the current ACTIVE while it remains eligible. M-015 intentionally
	// does not fail back merely because another candidate now scores higher.
	for _, c := range eligible {
		if old.ActiveIR != "" && c.IRNode == old.ActiveIR && old.EXNode == route.EXNode {
			next.State, next.ActiveIR = IngressReady, old.ActiveIR
			next.Reason = "current ACTIVE IR remains eligible; hold to avoid flap"
			return next, candidates
		}
	}
	next.State, next.ActiveIR = IngressReady, eligible[0].IRNode
	if old.ActiveIR == "" {
		next.Reason = "selected highest-scoring eligible IR"
	} else {
		next.Reason = fmt.Sprintf("failover: previous ACTIVE IR %s is no longer eligible", old.ActiveIR)
	}
	return next, candidates
}

func (s *Store) ingressCandidatesLocked(route ExplicitEXRoute, now time.Time, p IngressPolicy) []IngressCandidate {
	ids := make([]string, 0, len(s.st.IRPool))
	for id, m := range s.st.IRPool {
		if m.Enabled {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	out := make([]IngressCandidate, 0, len(ids))
	for _, ir := range ids {
		out = append(out, s.ingressCandidateLocked(ir, route, now, p))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Eligible != out[j].Eligible {
			return out[i].Eligible
		}
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].IRNode < out[j].IRNode
	})
	return out
}

func (s *Store) ingressCandidateLocked(ir string, route ExplicitEXRoute, now time.Time, p IngressPolicy) IngressCandidate {
	c := IngressCandidate{IRNode: ir, Score: 0, Health: HealthUnknown, TopologyStatus: topologyStatusMissing, LatencyMS: -1}
	n, ok := s.st.Nodes[ir]
	if !ok {
		c.Reasons = append(c.Reasons, "IR node is missing")
		return c
	}
	c.LatencyMS = n.LatencyMS
	if n.Revoked {
		c.Reasons = append(c.Reasons, "IR node is revoked")
		return c
	}
	b, ok := s.st.TopologyBindings[topologyEdgeKey(ir, route.ID)]
	if !ok {
		c.Reasons = append(c.Reasons, "desired topology binding is missing")
		return c
	}
	edge := s.edgeStatusLocked(b)
	c.TopologyStatus = edge.Status
	if edge.Status != topologyStatusActive {
		c.Reasons = append(c.Reasons, "topology edge is "+edge.Status)
		return c
	}
	t, ok := s.activeTopologyTunnelLocked(b.Key)
	if !ok {
		c.Reasons = append(c.Reasons, "ACTIVE topology tunnel is missing")
		return c
	}
	if t.Drift != nil && t.Drift.State != DriftInSync {
		c.Reasons = append(c.Reasons, "tunnel drift is "+t.Drift.State)
		return c
	}

	okFresh, reason := s.nodeCoreEligibleForIngressLocked(ir, now, p)
	c.Health = s.nodeCoreHealthStateLocked(ir)
	if !okFresh {
		c.Reasons = append(c.Reasons, reason)
		return c
	}
	cur := s.st.Telemetry[ir]
	routeSample, ok := routeSnapshotForIngress(cur, route)
	if !ok {
		c.Reasons = append(c.Reasons, "route-specific telemetry is missing")
		return c
	}
	c.RouteStatus = routeSample.Status
	c.RouteLatencyMS = routeSample.LatencyMS
	if routeSample.Status != "up" {
		if routeSample.Status == "" {
			c.Reasons = append(c.Reasons, "route-specific telemetry has no status")
		} else {
			c.Reasons = append(c.Reasons, "route-specific telemetry is "+routeSample.Status)
		}
		return c
	}
	c.NoiseLatencyMS = cur.NoiseLatencyMS
	c.HandshakeErrorRateMilliMin = cur.HandshakeErrorRateMilliMin

	score := 1000
	if routeSample.LatencyMS > 0 {
		score -= minIngress(int(routeSample.LatencyMS), 500)
	} else if n.LatencyMS > 0 {
		score -= minIngress(int(n.LatencyMS), 500)
	}
	if cur.NoiseLatencyMS > 0 {
		score -= minIngress(int(cur.NoiseLatencyMS), 300)
	}
	if cur.HandshakeErrorRateMilliMin > 0 {
		score -= minIngress(int(cur.HandshakeErrorRateMilliMin/100), 100)
	}
	if score < 1 {
		score = 1
	}
	c.Score, c.Eligible = score, true
	c.Reasons = []string{"ACTIVE topology edge; core node health UP; route-specific probe UP; management and telemetry evidence fresh"}
	return c
}

func routeSnapshotForIngress(cur TelemetryCursor, route ExplicitEXRoute) (telemetry.RouteSnapshot, bool) {
	id := route.RouteID
	if id == "" {
		id = route.ID
	}
	for _, r := range cur.Routes {
		if r.RouteID == id {
			return r, true
		}
	}
	return telemetry.RouteSnapshot{}, false
}

func (s *Store) nodeCoreHealthStateLocked(id string) string {
	rec, ok := s.st.Health[id]
	if !ok {
		return HealthUnknown
	}
	// Old persisted/test records may predate per-layer health. Preserve their
	// meaning, but current records deliberately exclude L4 here because route
	// health is evaluated separately for the explicit route being selected.
	if len(rec.Layers) == 0 {
		if rec.Overall != "" {
			return rec.Overall
		}
		return HealthUnknown
	}
	get := func(layer string) string {
		if r := rec.Layers[layer]; r != nil && r.State != "" {
			return r.State
		}
		return HealthUnknown
	}
	for _, layer := range []string{LayerL0, LayerL1} {
		if get(layer) != HealthUp {
			return get(layer)
		}
	}
	for _, layer := range []string{LayerL2, LayerAgent} {
		switch get(layer) {
		case HealthDown, HealthDegraded, HealthRecovering:
			return HealthDegraded
		}
	}
	return HealthUp
}

func (s *Store) nodeCoreEligibleForIngressLocked(id string, now time.Time, p IngressPolicy) (bool, string) {
	n, ok := s.st.Nodes[id]
	if !ok {
		return false, "node is missing"
	}
	if n.Revoked {
		return false, "node is revoked"
	}
	state := s.nodeCoreHealthStateLocked(id)
	if state != HealthUp {
		return false, "core node health is " + state
	}
	if n.AgentSeen.IsZero() || now.Sub(n.AgentSeen) >= p.AgentStaleAfter {
		return false, "agent evidence is stale or missing"
	}
	if n.LastChecked.IsZero() || now.Sub(n.LastChecked) > p.ProbeStaleAfter {
		return false, "reachability probe is stale or missing"
	}
	cur, ok := s.st.Telemetry[id]
	if !ok || cur.LastTelemetry.IsZero() || now.Sub(cur.LastTelemetry) >= p.TelemetryStaleAfter {
		return false, "telemetry is stale or missing"
	}
	return true, ""
}

func (s *Store) nodeHealthStateLocked(id string) string {
	rec, ok := s.st.Health[id]
	if !ok {
		return HealthUnknown
	}
	if rec.Overall != "" {
		return rec.Overall
	}
	if rec.Layers != nil {
		state, _ := overallHealth(rec.Layers)
		return state
	}
	return HealthUnknown
}

func (s *Store) nodeEligibleForIngressLocked(id string, now time.Time, p IngressPolicy) (bool, string) {
	n, ok := s.st.Nodes[id]
	if !ok {
		return false, "node is missing"
	}
	if n.Revoked {
		return false, "node is revoked"
	}
	state := s.nodeHealthStateLocked(id)
	if state != HealthUp {
		return false, "node health is " + state
	}
	if n.AgentSeen.IsZero() || now.Sub(n.AgentSeen) >= p.AgentStaleAfter {
		return false, "agent evidence is stale or missing"
	}
	if n.LastChecked.IsZero() || now.Sub(n.LastChecked) > p.ProbeStaleAfter {
		return false, "reachability probe is stale or missing"
	}
	cur, ok := s.st.Telemetry[id]
	if !ok || cur.LastTelemetry.IsZero() || now.Sub(cur.LastTelemetry) >= p.TelemetryStaleAfter {
		return false, "telemetry is stale or missing"
	}
	return true, ""
}

func (s *Store) activeTopologyTunnelLocked(key string) (Tunnel, bool) {
	for _, t := range s.st.Tunnels {
		if t.TopologyKey == key && t.Phase == TunnelActive {
			return t, true
		}
	}
	return Tunnel{}, false
}

func standbyOrder(candidates []IngressCandidate, active string) []string {
	var out []string
	for _, c := range candidates {
		if c.Eligible && c.IRNode != active {
			out = append(out, c.IRNode)
		}
	}
	return out
}

func minIngress(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (s *Server) ingressAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.admin(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, s.store.IngressSnapshot(s.now()))
}

func (s *Server) ingressEvaluateAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.admin(w, r) {
		return
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	events, err := s.store.EvaluateIngress(s.now(), DefaultIngressPolicy())
	if err != nil {
		s.auditFailure(w, r, "ingress.evaluate", "cluster", nil, err, http.StatusInternalServerError)
		return
	}
	for _, e := range events {
		if err := s.auditAdmin(r, "ingress.selection", e.RouteID, "success", map[string]any{
			"ex_node": e.EXNode, "from_state": e.FromState, "to_state": e.ToState,
			"from_ir": e.FromIR, "to_ir": e.ToIR, "generation": e.Generation, "reason": e.Reason,
		}); err != nil {
			http.Error(w, "audit log failure", http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, http.StatusOK, s.store.IngressSnapshot(s.now()))
}
