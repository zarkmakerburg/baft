package bcc

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/zarkmakerburg/baft/internal/telemetry"
)

const healthHistoryCap = 200

// NodeHealthRecord is the persisted layered health of one node: the layers'
// state machines and the transitions that moved them. It lives in the BCC
// state, so counters and history survive a restart.
type NodeHealthRecord struct {
	Overall string                  `json:"overall"`
	Layers  map[string]*LayerRecord `json:"layers"`
	History []LayerTransition       `json:"history,omitempty"`
}

// HealthConfig is what the evaluation needs besides the policy.
type HealthConfig struct {
	Policy              HealthPolicy
	TelemetryStaleAfter time.Duration
	AgentStaleAfter     time.Duration
	// HandshakeErrorRateMilliPerMin is the L2 BAD threshold.
	HandshakeErrorRateMilliPerMin int64
}

func DefaultHealthConfig() HealthConfig {
	return HealthConfig{
		Policy: DefaultHealthPolicy(), TelemetryStaleAfter: 3 * time.Minute, AgentStaleAfter: 3 * time.Minute,
		HandshakeErrorRateMilliPerMin: 5000,
	}
}

// HealthEvent is a transition together with the node it belongs to.
type HealthEvent struct {
	NodeID string
	LayerTransition
}

// sampleLayers reads what BCC actually observed about a node. A layer with no
// evidence gets NONE; only the layers with a real signal appear here.
func sampleLayers(n Node, cur *TelemetryCursor, now time.Time, c HealthConfig) map[string]Sample {
	out := map[string]Sample{}
	age := func(t time.Time) time.Duration { return now.Sub(t).Round(time.Second) }

	// Agent: it polls BCC for jobs, so BCC has first-hand evidence.
	switch {
	case n.AgentSeen.IsZero():
		out[LayerAgent] = Sample{SampleNone, "the agent has never polled"}
	case now.Sub(n.AgentSeen) >= c.AgentStaleAfter:
		out[LayerAgent] = Sample{SampleBad, fmt.Sprintf("last agent poll %s ago", age(n.AgentSeen))}
	default:
		out[LayerAgent] = Sample{SampleOK, fmt.Sprintf("last agent poll %s ago", age(n.AgentSeen))}
	}

	// L1: BCC's own TCP probe of the node's address.
	switch {
	case n.LastChecked.IsZero() || n.Health == "unknown" || n.Health == "":
		out[LayerL1] = Sample{SampleNone, "the node has not been probed yet"}
	case now.Sub(n.LastChecked) > c.Policy.MaxGap:
		out[LayerL1] = Sample{SampleNone, fmt.Sprintf("the last probe result is %s old", age(n.LastChecked))}
	case n.Health == "up":
		out[LayerL1] = Sample{SampleOK, fmt.Sprintf("TCP connect to %s succeeded in %d ms", n.Address, n.LatencyMS)}
	default:
		out[LayerL1] = Sample{SampleBad, fmt.Sprintf("TCP connect to %s failed", n.Address)}
	}

	// L0, L2, L4: the node's own telemetry.
	if cur == nil {
		for _, id := range []string{LayerL0, LayerL2, LayerL4} {
			out[id] = Sample{SampleNone, "no telemetry has ever been received"}
		}
		return out
	}
	if now.Sub(cur.LastTelemetry) >= c.TelemetryStaleAfter {
		// A process that used to report and went silent is the signal for L0;
		// the layers that depend on its contents have no fresh evidence.
		out[LayerL0] = Sample{SampleBad, fmt.Sprintf("telemetry stale for %s", age(cur.LastTelemetry))}
		out[LayerL2] = Sample{SampleNone, "telemetry is stale"}
		out[LayerL4] = Sample{SampleNone, "telemetry is stale"}
		return out
	}
	out[LayerL0] = Sample{SampleOK, fmt.Sprintf("telemetry %s old", age(cur.LastTelemetry))}
	switch {
	case cur.HandshakeErrorRateMilliMin >= c.HandshakeErrorRateMilliPerMin:
		out[LayerL2] = Sample{SampleBad, fmt.Sprintf("handshake error rate %.1f/min", float64(cur.HandshakeErrorRateMilliMin)/1000)}
	case cur.ActiveSessions > 0:
		out[LayerL2] = Sample{SampleOK, fmt.Sprintf("%d active authenticated sessions", cur.ActiveSessions)}
	default:
		out[LayerL2] = Sample{SampleNone, "no active sessions: no evidence either way"}
	}
	out[LayerL4] = routeSample(cur.Routes)
	return out
}

func routeSample(routes []telemetry.RouteSnapshot) Sample {
	var down, up, unknown []string
	for _, r := range routes {
		switch r.Status {
		case "down":
			down = append(down, r.RouteID)
		case "up":
			up = append(up, r.RouteID)
		default:
			unknown = append(unknown, r.RouteID)
		}
	}
	switch {
	case len(down) > 0:
		return Sample{SampleBad, "route probe down: " + strings.Join(down, ", ")}
	case len(up) > 0:
		ev := fmt.Sprintf("%d route probe(s) up", len(up))
		if len(unknown) > 0 {
			ev += fmt.Sprintf(", %d unknown", len(unknown))
		}
		return Sample{SampleOK, ev}
	}
	return Sample{SampleNone, "no route probe has a result"}
}

// EvaluateHealth takes one sample of every layer of every node, applies the
// hysteresis, persists the result once and returns the transitions.
func (s *Store) EvaluateHealth(now time.Time, c HealthConfig) ([]HealthEvent, error) {
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.Health == nil {
		s.st.Health = map[string]NodeHealthRecord{}
	}
	for id := range s.st.Health {
		if _, ok := s.st.Nodes[id]; !ok {
			delete(s.st.Health, id)
		}
	}
	ids := make([]string, 0, len(s.st.Nodes))
	for id := range s.st.Nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var events []HealthEvent
	for _, id := range ids {
		n := s.st.Nodes[id]
		var cur *TelemetryCursor
		if v, ok := s.st.Telemetry[id]; ok {
			cur = &v
		}
		rec := s.st.Health[id]
		if rec.Layers == nil {
			rec.Layers = map[string]*LayerRecord{}
		}
		samples := sampleLayers(n, cur, now, c)
		for _, l := range []string{LayerL0, LayerL1, LayerL2, LayerL4, LayerAgent} {
			lr := rec.Layers[l]
			if lr == nil {
				lr = &LayerRecord{}
				rec.Layers[l] = lr
			}
			before, _ := overallHealth(rec.Layers)
			sm := samples[l]
			from, reason, moved := observe(lr, sm, now, c.Policy)
			if !moved {
				continue
			}
			after, _ := overallHealth(rec.Layers)
			t := LayerTransition{At: now, Layer: l, From: from, To: lr.State, NodeFrom: before, NodeTo: after, Reason: reason, Evidence: sm.Evidence,
				EventID: fmt.Sprintf("hl-%s-%s-%d", id, l, now.UnixNano())}
			rec.History = append(rec.History, t)
			if len(rec.History) > healthHistoryCap {
				rec.History = rec.History[len(rec.History)-healthHistoryCap:]
			}
			events = append(events, HealthEvent{NodeID: id, LayerTransition: t})
		}
		rec.Overall, _ = overallHealth(rec.Layers)
		s.st.Health[id] = rec
	}
	return events, s.saveLocked()
}

// HealthView is what the API and the dashboard show for one node.
type HealthView struct {
	NodeID      string            `json:"node_id"`
	Alias       string            `json:"alias"`
	Overall     string            `json:"overall"`
	Why         []string          `json:"why,omitempty"`
	Layers      []NodeLayerView   `json:"layers"`
	Conformity  string            `json:"conformity,omitempty"` // drift state of the node's active tunnel, if any
	Transitions []LayerTransition `json:"transitions,omitempty"`
}

// HealthSnapshot returns the layered health of every node (or one), with the
// last `history` transitions (all of them when history <= 0 and one node).
func (s *Store) HealthSnapshot(only string, history int) []HealthView {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []HealthView
	for id, n := range s.st.Nodes {
		if only != "" && id != only {
			continue
		}
		rec := s.st.Health[id]
		v := HealthView{NodeID: id, Alias: n.Alias, Overall: HealthUnknown}
		if rec.Layers != nil {
			v.Overall, v.Why = overallHealth(rec.Layers)
		} else {
			v.Why = []string{"no layer has evidence yet"}
		}
		for _, l := range healthLayers {
			lv := NodeLayerView{Layer: l.ID, Name: l.Name}
			if l.NotAssessed != "" {
				lv.State, lv.Note = HealthNotAssessed, l.NotAssessed
			} else if r := rec.Layers[l.ID]; r != nil && r.State != "" {
				lv.State, lv.Since, lv.Evidence = r.State, r.Since, r.LastEvidence
			} else {
				lv.State = HealthUnknown
			}
			v.Layers = append(v.Layers, lv)
		}
		h := rec.History
		if history > 0 && len(h) > history {
			h = h[len(h)-history:]
		}
		v.Transitions = append([]LayerTransition(nil), h...)
		for _, t := range s.st.Tunnels {
			if t.Phase == TunnelActive && t.Drift != nil && (t.EXNode == id || t.IRNode == id) {
				if d, ok := t.Drift.Nodes[id]; ok {
					v.Conformity = d.State
				}
			}
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Alias < out[j].Alias || (out[i].Alias == out[j].Alias && out[i].NodeID < out[j].NodeID)
	})
	return out
}

// ---- server ----

func (s *Server) healthConfig() HealthConfig {
	c := DefaultHealthConfig()
	if s.alertConfig.TelemetryStaleAfter > 0 {
		c.TelemetryStaleAfter = s.alertConfig.TelemetryStaleAfter
	}
	if s.alertConfig.HandshakeErrorRateMilliPerMin > 0 {
		c.HandshakeErrorRateMilliPerMin = s.alertConfig.HandshakeErrorRateMilliPerMin
	}
	return c
}

// EvaluateHealthOnce samples every node now and records each transition in
// the audit log.
func (s *Server) EvaluateHealthOnce() { s.evaluateHealthAt(s.now()) }

func (s *Server) evaluateHealthAt(now time.Time) {
	events, err := s.store.EvaluateHealth(now, s.healthConfig())
	if err != nil {
		return
	}
	for _, e := range events {
		outcome := "success"
		if e.To == HealthDegraded || e.To == HealthDown {
			outcome = "failure"
		}
		_, _ = s.audit.Append(AuditEntry{
			Timestamp: now.UTC(), Actor: "bcc", Action: "health.transition", Target: e.NodeID + "/" + e.Layer, Outcome: outcome,
			Details: map[string]any{
				"layer": e.Layer, "from": e.From, "to": e.To, "node_from": e.NodeFrom, "node_to": e.NodeTo,
				"reason": e.Reason, "evidence": e.Evidence, "event_id": e.EventID,
			},
		})
	}
	// Ingress decisions consume only persisted health/topology evidence. Run the
	// deterministic M-015 selector after health has been committed so a health
	// transition can immediately fail over the affected explicit EX route.
	ingressEvents, err := s.store.EvaluateIngress(now, DefaultIngressPolicy())
	if err != nil {
		return
	}
	for _, e := range ingressEvents {
		_, _ = s.audit.Append(AuditEntry{
			Timestamp: now.UTC(), Actor: "bcc", Action: "ingress.selection", Target: e.RouteID, Outcome: "success",
			Details: map[string]any{
				"ex_node": e.EXNode, "from_state": e.FromState, "to_state": e.ToState,
				"from_ir": e.FromIR, "to_ir": e.ToIR, "generation": e.Generation, "reason": e.Reason,
			},
		})
	}
	distEvents, err := s.store.EvaluateDistributions(now, DefaultDistributionPolicy())
	if err != nil {
		return
	}
	for _, e := range distEvents {
		_, _ = s.audit.Append(AuditEntry{
			Timestamp: now.UTC(), Actor: "bcc", Action: "ingress.distribution", Target: e.RouteID, Outcome: "success",
			Details: map[string]any{
				"ex_node": e.EXNode, "from_state": e.FromState, "to_state": e.ToState,
				"from": e.From, "to": e.To, "generation": e.Generation, "reason": e.Reason,
			},
		})
	}
}

// healthAPI: GET /api/health[?node=ID[&all=1]], admin only, read only.
func (s *Server) healthAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	if !s.admin(w, r) {
		return
	}
	node := r.URL.Query().Get("node")
	history := 10
	if node != "" && r.URL.Query().Get("all") == "1" {
		history = 0
	}
	p := s.healthConfig().Policy
	writeJSON(w, 200, map[string]any{
		"policy": map[string]any{
			"degrade_after": p.DegradeAfter, "down_after": p.DownAfter, "min_down_seconds": int(p.MinDown.Seconds()),
			"recover_after": p.RecoverAfter, "up_after": p.UpAfter, "min_recover_seconds": int(p.MinRecover.Seconds()),
			"unknown_after_seconds": int(p.UnknownAfter.Seconds()), "max_gap_seconds": int(p.MaxGap.Seconds()),
		},
		"nodes": s.store.HealthSnapshot(node, history),
	})
}

// L1Incident describes the current reachability incident of a node from its
// layer record and history: what an operator needs to see in a node_unreachable
// alert. All of it comes from stored health data, never from a raw probe.
type L1Incident struct {
	Address, PreviousState, CurrentState, Reason, EventID string
	FailureStarted, LastReachable                         time.Time
}

func (s *Store) L1Incident(nodeID string) (L1Incident, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.st.Nodes[nodeID]
	if !ok {
		return L1Incident{}, false
	}
	rec := s.st.Health[nodeID]
	inc := L1Incident{Address: n.Address}
	if lr := rec.Layers[LayerL1]; lr != nil {
		inc.LastReachable = lr.LastOKAt
		inc.FailureStarted = lr.Since
	}
	// The transition that made L1 DOWN, and the one that started the failure.
	downAt := -1
	for i := len(rec.History) - 1; i >= 0; i-- {
		if t := rec.History[i]; t.Layer == LayerL1 && t.To == HealthDown {
			downAt = i
			break
		}
	}
	if downAt < 0 {
		return inc, true
	}
	t := rec.History[downAt]
	inc.PreviousState, inc.CurrentState, inc.Reason, inc.EventID, inc.FailureStarted = t.From, t.To, t.Reason, t.EventID, t.At
	for i := downAt - 1; i >= 0; i-- {
		if p := rec.History[i]; p.Layer == LayerL1 {
			if p.To == HealthDegraded {
				inc.FailureStarted = p.At
			}
			break
		}
	}
	return inc, true
}
