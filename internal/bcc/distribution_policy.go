package bcc

import (
	"fmt"
	"net/http"
	"sort"
	"time"
)

const (
	DistributionReady       = "READY"
	DistributionUnavailable = "UNAVAILABLE"
)

// IngressDistribution is M-016's durable new-connection weight vector for one
// explicit EX route. It never substitutes the EX and never migrates an
// existing session.
type IngressDistribution struct {
	RouteID     string         `json:"route_id"`
	EXNode      string         `json:"ex_node"`
	State       string         `json:"state"`
	Weights     map[string]int `json:"weights,omitempty"`
	Generation uint64         `json:"generation"`
	Reason      string         `json:"reason,omitempty"`
	ChangedAt   time.Time      `json:"changed_at,omitempty"`
	EvaluatedAt time.Time      `json:"evaluated_at,omitempty"`
}

type DistributionCandidate struct {
	IRNode         string   `json:"ir_node"`
	Eligible       bool     `json:"eligible"`
	Weight         int      `json:"weight"`
	RawScore       int64    `json:"raw_score"`
	CapacityWeight int      `json:"capacity_weight"`
	TrafficKbps    int64    `json:"traffic_kbps"`
	ActiveSessions uint64   `json:"active_sessions"`
	RouteLatencyMS int64    `json:"route_latency_ms"`
	HealthScore    int      `json:"health_score"`
	Reasons        []string `json:"reasons,omitempty"`
}

type DistributionView struct {
	IngressDistribution
	Usable     bool                    `json:"usable"`
	Candidates []DistributionCandidate `json:"candidates,omitempty"`
}

type DistributionEvent struct {
	RouteID    string         `json:"route_id"`
	EXNode      string         `json:"ex_node"`
	FromState  string         `json:"from_state,omitempty"`
	ToState    string         `json:"to_state"`
	From       map[string]int `json:"from,omitempty"`
	To         map[string]int `json:"to,omitempty"`
	Generation uint64         `json:"generation"`
	Reason     string         `json:"reason"`
}

type DistributionPolicy struct {
	LoadWindow     time.Duration
	HoldDown       time.Duration
	MinWeightDelta int
}

func DefaultDistributionPolicy() DistributionPolicy {
	return DistributionPolicy{LoadWindow: 5 * time.Minute, HoldDown: 30 * time.Second, MinWeightDelta: 10}
}

func (s *Store) EvaluateDistributions(now time.Time, p DistributionPolicy) ([]DistributionEvent, error) {
	now = now.UTC()
	if p.LoadWindow <= 0 {
		p.LoadWindow = 5 * time.Minute
	}
	if p.HoldDown < 0 {
		p.HoldDown = 0
	}
	if p.MinWeightDelta < 1 {
		p.MinWeightDelta = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.IngressDistributions == nil {
		s.st.IngressDistributions = map[string]IngressDistribution{}
	}
	before := cloneIngressDistributions(s.st.IngressDistributions)
	ids := make([]string, 0, len(s.st.EXRoutes))
	for id, route := range s.st.EXRoutes {
		if route.Enabled {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	var events []DistributionEvent
	for _, id := range ids {
		route := s.st.EXRoutes[id]
		old := s.st.IngressDistributions[id]
		next, _ := s.chooseDistributionLocked(route, old, now, p)
		next.EvaluatedAt = now
		if old.RouteID == "" {
			old.State = DistributionUnavailable
		}

		change := old.RouteID == "" || old.State != next.State || old.EXNode != next.EXNode || !sameWeights(old.Weights, next.Weights)
		if change && old.RouteID != "" && old.State == DistributionReady && next.State == DistributionReady &&
			sameWeightMembers(old.Weights, next.Weights) && now.Sub(old.ChangedAt) < p.HoldDown {
			next.Weights = cloneWeights(old.Weights)
			next.Generation = old.Generation
			next.ChangedAt = old.ChangedAt
			next.Reason = "hold-down: keep last stable weights"
			change = false
		}
		if change && old.RouteID != "" && old.State == DistributionReady && next.State == DistributionReady &&
			sameWeightMembers(old.Weights, next.Weights) && maxWeightDelta(old.Weights, next.Weights) < p.MinWeightDelta {
			next.Weights = cloneWeights(old.Weights)
			next.Generation = old.Generation
			next.ChangedAt = old.ChangedAt
			next.Reason = "weight change below material threshold"
			change = false
		}

		if change {
			next.Generation = old.Generation + 1
			if next.Generation == 0 {
				next.Generation = 1
			}
			next.ChangedAt = now
			events = append(events, DistributionEvent{
				RouteID: id, EXNode: route.EXNode, FromState: old.State, ToState: next.State,
				From: cloneWeights(old.Weights), To: cloneWeights(next.Weights),
				Generation: next.Generation, Reason: next.Reason,
			})
		} else {
			next.Generation = old.Generation
			if next.ChangedAt.IsZero() {
				next.ChangedAt = old.ChangedAt
			}
		}
		s.st.IngressDistributions[id] = next
	}
	if err := s.saveLocked(); err != nil {
		s.st.IngressDistributions = before
		return nil, err
	}
	return events, nil
}

func (s *Store) chooseDistributionLocked(route ExplicitEXRoute, old IngressDistribution, now time.Time, p DistributionPolicy) (IngressDistribution, []DistributionCandidate) {
	next := IngressDistribution{RouteID: route.ID, EXNode: route.EXNode, Weights: map[string]int{}}
	exOK, exReason := s.nodeEligibleForIngressLocked(route.EXNode, now, DefaultIngressPolicy())
	ingress := s.ingressCandidatesLocked(route, now, DefaultIngressPolicy())
	candidates := make([]DistributionCandidate, 0, len(ingress))
	if !exOK {
		next.State, next.Reason = DistributionUnavailable, "explicit EX unavailable: "+exReason
		for _, c := range ingress {
			candidates = append(candidates, DistributionCandidate{IRNode: c.IRNode, Eligible: false, HealthScore: c.Score, RouteLatencyMS: c.RouteLatencyMS, Reasons: []string{"explicit EX unavailable"}})
		}
		return next, candidates
	}

	type rawCandidate struct {
		idx int
		raw int64
		id  string
	}
	var raws []rawCandidate
	for _, c := range ingress {
		d := DistributionCandidate{
			IRNode: c.IRNode, Eligible: c.Eligible, HealthScore: c.Score,
			RouteLatencyMS: c.RouteLatencyMS, Reasons: append([]string(nil), c.Reasons...),
		}
		member := s.st.IRPool[c.IRNode]
		d.CapacityWeight = member.CapacityWeight
		if d.CapacityWeight <= 0 {
			d.CapacityWeight = 100
		}
		cur := s.st.Telemetry[c.IRNode]
		d.ActiveSessions = cur.ActiveSessions
		d.TrafficKbps = s.recentTrafficKbpsLocked(c.IRNode, now, p.LoadWindow)
		if c.Eligible {
			loadPenalty := int64(100)
			loadPenalty += min64(int64(d.ActiveSessions)*5, 500)
			loadPenalty += min64((d.TrafficKbps/1000)*4, 600)
			d.RawScore = int64(d.CapacityWeight) * int64(maxInt(c.Score, 1)) * 100 / loadPenalty
			if d.RawScore < 1 {
				d.RawScore = 1
			}
		}
		candidates = append(candidates, d)
		if d.Eligible {
			raws = append(raws, rawCandidate{idx: len(candidates) - 1, raw: d.RawScore, id: d.IRNode})
		}
	}
	if len(raws) == 0 {
		next.State, next.Reason = DistributionUnavailable, "no eligible IR for this explicit EX route"
		return next, candidates
	}

	total := int64(0)
	for _, r := range raws {
		total += r.raw
	}
	allocated := 0
	for _, r := range raws {
		w := int(r.raw * 100 / total)
		next.Weights[r.id] = w
		allocated += w
	}
	sort.Slice(raws, func(i, j int) bool {
		if raws[i].raw != raws[j].raw {
			return raws[i].raw > raws[j].raw
		}
		return raws[i].id < raws[j].id
	})
	for i := 0; allocated < 100; i++ {
		id := raws[i%len(raws)].id
		next.Weights[id]++
		allocated++
	}
	for i := range candidates {
		candidates[i].Weight = next.Weights[candidates[i].IRNode]
	}
	next.State = DistributionReady
	if len(raws) == 1 {
		next.Reason = "single eligible IR receives all new-connection weight"
	} else {
		next.Reason = "weights derived from route health, capacity hint and current IR load"
	}
	return next, candidates
}

func (s *Store) DistributionSnapshot(now time.Time) []DistributionView {
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
	out := make([]DistributionView, 0, len(ids))
	for _, id := range ids {
		route := s.st.EXRoutes[id]
		dist := s.st.IngressDistributions[id]
		if dist.RouteID == "" {
			dist = IngressDistribution{RouteID: id, EXNode: route.EXNode, State: DistributionUnavailable}
		}
		_, candidates := s.chooseDistributionLocked(route, dist, now, DefaultDistributionPolicy())
		eligible := map[string]bool{}
		for _, c := range candidates {
			if c.Eligible {
				eligible[c.IRNode] = true
			}
		}
		sum := 0
		usable := dist.State == DistributionReady && dist.EXNode == route.EXNode
		for ir, w := range dist.Weights {
			if w <= 0 {
				continue
			}
			sum += w
			if !eligible[ir] {
				usable = false
			}
		}
		if sum != 100 {
			usable = false
		}
		for i := range candidates {
			candidates[i].Weight = dist.Weights[candidates[i].IRNode]
		}
		out = append(out, DistributionView{IngressDistribution: dist, Usable: usable, Candidates: candidates})
	}
	return out
}

func (s *Store) recentTrafficKbpsLocked(node string, now time.Time, window time.Duration) int64 {
	h := s.st.History[node]
	if len(h) < 2 {
		return 0
	}
	cut := now.Add(-window)
	var first *HistoryPoint
	var last *HistoryPoint
	for i := range h {
		p := &h[i]
		if p.Timestamp.After(now) || p.Timestamp.Before(cut) {
			continue
		}
		if first == nil {
			first = p
		}
		last = p
	}
	if first == nil || last == nil || !last.Timestamp.After(first.Timestamp) {
		return 0
	}
	if last.IngressBytes < first.IngressBytes || last.EgressBytes < first.EgressBytes {
		return 0
	}
	delta := (last.IngressBytes - first.IngressBytes) + (last.EgressBytes - first.EgressBytes)
	seconds := last.Timestamp.Sub(first.Timestamp).Seconds()
	if seconds <= 0 {
		return 0
	}
	return int64((float64(delta) * 8 / 1000) / seconds)
}

func cloneIngressDistributions(in map[string]IngressDistribution) map[string]IngressDistribution {
	out := make(map[string]IngressDistribution, len(in))
	for k, v := range in {
		v.Weights = cloneWeights(v.Weights)
		out[k] = v
	}
	return out
}

func cloneWeights(in map[string]int) map[string]int {
	if len(in) == 0 {
		return map[string]int{}
	}
	out := make(map[string]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func sameWeights(a, b map[string]int) bool {
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

func sameWeightMembers(a, b map[string]int) bool {
	aa, bb := map[string]bool{}, map[string]bool{}
	for k, v := range a {
		if v > 0 {
			aa[k] = true
		}
	}
	for k, v := range b {
		if v > 0 {
			bb[k] = true
		}
	}
	if len(aa) != len(bb) {
		return false
	}
	for k := range aa {
		if !bb[k] {
			return false
		}
	}
	return true
}

func maxWeightDelta(a, b map[string]int) int {
	max := 0
	seen := map[string]bool{}
	for k, av := range a {
		d := av - b[k]
		if d < 0 {
			d = -d
		}
		if d > max {
			max = d
		}
		seen[k] = true
	}
	for k, bv := range b {
		if seen[k] {
			continue
		}
		d := bv
		if d < 0 {
			d = -d
		}
		if d > max {
			max = d
		}
	}
	return max
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (s *Server) distributionAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.admin(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, s.store.DistributionSnapshot(s.now()))
}

func (s *Server) distributionEvaluateAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.admin(w, r) {
		return
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	events, err := s.store.EvaluateDistributions(s.now(), DefaultDistributionPolicy())
	if err != nil {
		s.auditFailure(w, r, "ingress.distribution.evaluate", "cluster", nil, err, http.StatusInternalServerError)
		return
	}
	for _, e := range events {
		if err := s.auditAdmin(r, "ingress.distribution", e.RouteID, "success", map[string]any{
			"ex_node": e.EXNode, "from_state": e.FromState, "to_state": e.ToState,
			"from": e.From, "to": e.To, "generation": e.Generation, "reason": e.Reason,
		}); err != nil {
			http.Error(w, "audit log failure", http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, http.StatusOK, s.store.DistributionSnapshot(s.now()))
}

// keep fmt referenced for stable gofmt/import grouping when this file grows
var _ = fmt.Sprintf
