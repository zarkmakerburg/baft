package bcc

import (
	"errors"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	SmartIngressReady         = "READY"
	SmartIngressUnpublishable = "UNPUBLISHABLE"

	SmartIngressActionPublish  = "PUBLISH"
	SmartIngressActionWithdraw = "WITHDRAW"

	SmartIngressApplyPending = "PENDING"
	SmartIngressApplyBlocked = "BLOCKED"
	SmartIngressApplyApplied = "APPLIED"
	SmartIngressApplyError   = "ERROR"
)

// SmartIngressEndpoint is one explicit public IR address in a provider-independent
// publication plan. Weight applies only to new ingress connections.
type SmartIngressEndpoint struct {
	IRNode string `json:"ir_node"`
	IP     string `json:"ip"`
	Weight int    `json:"weight"`
}

// SmartIngressPlan is M-017A's durable desired/applied boundary. M-017A only
// computes Desired state; a later provider adapter is responsible for Applied.
type SmartIngressPlan struct {
	RouteID                string                 `json:"route_id"`
	EXNode                 string                 `json:"ex_node"`
	Host                   string                 `json:"host,omitempty"`
	State                  string                 `json:"state"`
	Action                 string                 `json:"action"`
	Endpoints              []SmartIngressEndpoint `json:"endpoints,omitempty"`
	DistributionGeneration uint64                 `json:"distribution_generation,omitempty"`
	Generation             uint64                 `json:"generation"`
	Reason                 string                 `json:"reason,omitempty"`
	ChangedAt              time.Time              `json:"changed_at,omitempty"`
	EvaluatedAt            time.Time              `json:"evaluated_at,omitempty"`
	AppliedGeneration      uint64                 `json:"applied_generation,omitempty"`
	AppliedHost            string                 `json:"applied_host,omitempty"`
	AppliedEndpoints       []SmartIngressEndpoint `json:"applied_endpoints,omitempty"`
	ApplyStatus            string                 `json:"apply_status,omitempty"`
	AppliedAt              time.Time              `json:"applied_at,omitempty"`
}

type SmartIngressView struct {
	SmartIngressPlan
	Usable bool `json:"usable"`
}

type SmartIngressEvent struct {
	RouteID                string `json:"route_id"`
	EXNode                 string `json:"ex_node"`
	Host                   string `json:"host,omitempty"`
	FromState              string `json:"from_state,omitempty"`
	ToState                string `json:"to_state"`
	Generation             uint64 `json:"generation"`
	DistributionGeneration uint64 `json:"distribution_generation,omitempty"`
	Reason                 string `json:"reason"`
}

func normalizeSmartIngressHost(v string) (string, error) {
	h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(v)), ".")
	if h == "" {
		return "", nil
	}
	if len(h) > 253 || net.ParseIP(h) != nil || !strings.Contains(h, ".") {
		return "", errors.New("must be a DNS hostname")
	}
	for _, label := range strings.Split(h, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("contains an invalid DNS label")
		}
		for _, r := range label {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
				continue
			}
			return "", errors.New("contains an invalid DNS label")
		}
	}
	return h, nil
}

func (s *Store) EvaluateSmartIngress(now time.Time) ([]SmartIngressEvent, error) {
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.SmartIngressPlans == nil {
		s.st.SmartIngressPlans = map[string]SmartIngressPlan{}
	}
	before := cloneSmartIngressPlans(s.st.SmartIngressPlans)
	ids := map[string]bool{}
	for id := range s.st.EXRoutes {
		ids[id] = true
	}
	for id := range s.st.SmartIngressPlans {
		ids[id] = true
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)

	var events []SmartIngressEvent
	for _, id := range ordered {
		route, exists := s.st.EXRoutes[id]
		old := s.st.SmartIngressPlans[id]
		next := s.chooseSmartIngressLocked(route, exists, old, now)
		next.EvaluatedAt = now
		material := old.RouteID == "" || !sameSmartIngressDesired(old, next)
		if material {
			next.Generation = old.Generation + 1
			if next.Generation == 0 {
				next.Generation = 1
			}
			next.ChangedAt = now
			next.AppliedGeneration = old.AppliedGeneration
			next.AppliedHost = old.AppliedHost
			next.AppliedEndpoints = append([]SmartIngressEndpoint(nil), old.AppliedEndpoints...)
			next.AppliedAt = old.AppliedAt
			if next.State == SmartIngressReady || old.AppliedGeneration > 0 || old.AppliedHost != "" || len(old.AppliedEndpoints) > 0 {
				next.ApplyStatus = SmartIngressApplyPending
			} else {
				next.ApplyStatus = SmartIngressApplyBlocked
			}
			events = append(events, SmartIngressEvent{
				RouteID: next.RouteID, EXNode: next.EXNode, Host: next.Host,
				FromState: old.State, ToState: next.State, Generation: next.Generation,
				DistributionGeneration: next.DistributionGeneration, Reason: next.Reason,
			})
		} else {
			next.Generation = old.Generation
			next.ChangedAt = old.ChangedAt
			next.AppliedGeneration = old.AppliedGeneration
			next.AppliedHost = old.AppliedHost
			next.AppliedEndpoints = append([]SmartIngressEndpoint(nil), old.AppliedEndpoints...)
			next.ApplyStatus = old.ApplyStatus
			next.AppliedAt = old.AppliedAt
		}
		s.st.SmartIngressPlans[id] = next
	}
	if err := s.saveLocked(); err != nil {
		s.st.SmartIngressPlans = before
		return nil, err
	}
	return events, nil
}

func (s *Store) chooseSmartIngressLocked(route ExplicitEXRoute, exists bool, old SmartIngressPlan, now time.Time) SmartIngressPlan {
	next := SmartIngressPlan{RouteID: route.ID, EXNode: route.EXNode, Host: route.IngressHost, State: SmartIngressUnpublishable, Action: SmartIngressActionWithdraw}
	if !exists || !route.Enabled {
		next.RouteID = old.RouteID
		if next.RouteID == "" {
			next.RouteID = route.ID
		}
		if next.EXNode == "" {
			next.EXNode = old.EXNode
		}
		if next.Host == "" {
			next.Host = old.Host
		}
		next.Reason = "route is disabled or removed"
		return next
	}
	if next.Host == "" {
		next.Reason = "explicit ingress_host is missing"
		return next
	}
	dist, ok := s.st.IngressDistributions[route.ID]
	if !ok || dist.RouteID == "" {
		next.Reason = "M-016 distribution has not been evaluated"
		return next
	}
	next.DistributionGeneration = dist.Generation
	if dist.State != DistributionReady || dist.EXNode != route.EXNode {
		next.Reason = "M-016 distribution is unavailable or belongs to another EX"
		return next
	}
	_, candidates := s.chooseDistributionLocked(route, dist, now, DefaultDistributionPolicy())
	eligible := map[string]bool{}
	for _, c := range candidates {
		if c.Eligible {
			eligible[c.IRNode] = true
		}
	}
	sum := 0
	for ir, weight := range dist.Weights {
		if weight <= 0 {
			continue
		}
		if !eligible[ir] {
			next.Reason = "persisted M-016 weight targets a currently ineligible IR"
			return next
		}
		member, ok := s.st.IRPool[ir]
		if !ok || !member.Enabled || member.IngressIP == "" {
			next.Reason = "eligible IR is missing explicit ingress_ip"
			return next
		}
		ip := net.ParseIP(member.IngressIP)
		if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
			next.Reason = "eligible IR has invalid public ingress_ip"
			return next
		}
		next.Endpoints = append(next.Endpoints, SmartIngressEndpoint{IRNode: ir, IP: ip.String(), Weight: weight})
		sum += weight
	}
	if len(next.Endpoints) == 0 || sum != 100 {
		next.Endpoints = nil
		next.Reason = "M-016 distribution is not a complete 100% publishable vector"
		return next
	}
	sort.Slice(next.Endpoints, func(i, j int) bool { return next.Endpoints[i].IRNode < next.Endpoints[j].IRNode })
	next.State = SmartIngressReady
	next.Action = SmartIngressActionPublish
	next.Reason = "desired publication mirrors the current usable M-016 distribution"
	return next
}

func sameSmartIngressDesired(a, b SmartIngressPlan) bool {
	if a.RouteID != b.RouteID || a.EXNode != b.EXNode || a.Host != b.Host || a.State != b.State || a.Action != b.Action ||
		a.DistributionGeneration != b.DistributionGeneration || len(a.Endpoints) != len(b.Endpoints) {
		return false
	}
	for i := range a.Endpoints {
		if a.Endpoints[i] != b.Endpoints[i] {
			return false
		}
	}
	return true
}

func cloneSmartIngressPlans(in map[string]SmartIngressPlan) map[string]SmartIngressPlan {
	out := make(map[string]SmartIngressPlan, len(in))
	for k, v := range in {
		v.Endpoints = append([]SmartIngressEndpoint(nil), v.Endpoints...)
		v.AppliedEndpoints = append([]SmartIngressEndpoint(nil), v.AppliedEndpoints...)
		out[k] = v
	}
	return out
}

func (s *Store) SmartIngressSnapshot(now time.Time) []SmartIngressView {
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := map[string]bool{}
	for id := range s.st.EXRoutes {
		ids[id] = true
	}
	for id := range s.st.SmartIngressPlans {
		ids[id] = true
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	out := make([]SmartIngressView, 0, len(ordered))
	for _, id := range ordered {
		route, exists := s.st.EXRoutes[id]
		persisted := s.st.SmartIngressPlans[id]
		live := s.chooseSmartIngressLocked(route, exists, persisted, now)
		if persisted.RouteID == "" {
			persisted = live
		}
		usable := persisted.State == SmartIngressReady && sameSmartIngressDesired(persisted, live)
		out = append(out, SmartIngressView{SmartIngressPlan: persisted, Usable: usable})
	}
	return out
}

func (s *Server) smartIngressAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.admin(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, s.store.SmartIngressSnapshot(s.now()))
}

func (s *Server) smartIngressEvaluateAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.admin(w, r) {
		return
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	events, err := s.store.EvaluateSmartIngress(s.now())
	if err != nil {
		s.auditFailure(w, r, "ingress.smart.evaluate", "cluster", nil, err, http.StatusInternalServerError)
		return
	}
	for _, e := range events {
		if err := s.auditAdmin(r, "ingress.smart.desired", e.RouteID, "success", map[string]any{
			"ex_node": e.EXNode, "host": e.Host, "from_state": e.FromState, "to_state": e.ToState,
			"generation": e.Generation, "distribution_generation": e.DistributionGeneration, "reason": e.Reason,
		}); err != nil {
			http.Error(w, "audit log failure", http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, http.StatusOK, s.store.SmartIngressSnapshot(s.now()))
}
