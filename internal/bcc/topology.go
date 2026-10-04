package bcc

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// IRPoolMember is one ingress node participating in the Iran HA fabric.
// Health-based selection is intentionally deferred to M-015.
type IRPoolMember struct {
	NodeID    string    `json:"node_id"`
	Enabled   bool      `json:"enabled"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ExplicitEXRoute is one user-visible egress identity. It is deliberately not
// a pool: one route maps to one explicit foreign node.
type ExplicitEXRoute struct {
	ID            string    `json:"id"`
	EXNode        string    `json:"ex_node"`
	Country       string    `json:"country,omitempty"`
	PublicAddress string    `json:"public_address,omitempty"`
	Target        string    `json:"target,omitempty"`
	RouteID       string    `json:"route_id,omitempty"`
	Enabled       bool      `json:"enabled"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// TopologyBinding is the stable host-local resource assignment for one
// desired IR x EX edge. Bindings are retained even when an IR/route is later
// disabled so re-enabling it does not churn ports or instance identity.
type TopologyBinding struct {
	Key             string    `json:"key"`
	IRNode          string    `json:"ir_node"`
	RouteID         string    `json:"route_id"`
	EXNode          string    `json:"ex_node"`
	InstanceID      string    `json:"instance_id"`
	EXPort          int       `json:"ex_port"`
	IRRouteListen   string    `json:"ir_route_listen"`
	EXMetricsListen string    `json:"ex_metrics_listen"`
	IRMetricsListen string    `json:"ir_metrics_listen"`
	CreatedAt       time.Time `json:"created_at"`
}

// TopologySpec is the declarative topology surface used by the API.
type TopologySpec struct {
	IRMembers []IRPoolMember    `json:"ir_members"`
	EXRoutes  []ExplicitEXRoute `json:"ex_routes"`
}

// TopologyEdgeStatus is BCC's desired-vs-actual view of one desired edge.
type TopologyEdgeStatus struct {
	Binding   TopologyBinding `json:"binding"`
	Status    string          `json:"status"`
	TunnelID  string          `json:"tunnel_id,omitempty"`
	LastPhase string          `json:"last_phase,omitempty"`
	Problems  []string        `json:"problems,omitempty"`
}

// TopologyExtra is a topology-managed tunnel whose desired edge is disabled or
// removed. M-014 reports it but never tears it down automatically.
type TopologyExtra struct {
	Key      string `json:"key"`
	TunnelID string `json:"tunnel_id"`
	Phase    string `json:"phase"`
}

// TopologyReport is the current declarative state plus its reconciliation view.
type TopologyReport struct {
	Spec      TopologySpec         `json:"spec"`
	Edges     []TopologyEdgeStatus `json:"edges"`
	Extras    []TopologyExtra      `json:"extras,omitempty"`
	UpdatedAt time.Time            `json:"updated_at"`
}

var topologyRouteIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

const (
	topologyStatusActive   = "ACTIVE"
	topologyStatusBuilding = "BUILDING"
	topologyStatusMissing  = "MISSING"
	topologyStatusBlocked  = "BLOCKED"
	topologyStatusMismatch = "MISMATCH"

	topologyEXPortMin = 30000
	topologyEXPortMax = 39999
	topologyRouteMin  = 20000
	topologyRouteMax  = 29999
	topologyMetricMin = 10000
	topologyMetricMax = 19999
)

func topologyEdgeKey(ir, route string) string { return ir + "|" + route }

func topologyInstanceID(ir, route string) string {
	base := sanitizeTopologyID(ir) + "-" + sanitizeTopologyID(route)
	sum := sha256Short(ir + "\x00" + route)
	if len(base) > 24 {
		base = strings.Trim(base[:24], "-")
	}
	if base == "" {
		base = "edge"
	}
	return "topo-" + base + "-" + sum
}

func sanitizeTopologyID(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	var b strings.Builder
	lastDash := false
	for _, r := range v {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func sha256Short(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:4])
}

func (s *Store) SetTopologySpec(spec TopologySpec, now time.Time) (TopologyReport, error) {
	s.reconcileMu.Lock()
	defer s.reconcileMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	irs := make(map[string]IRPoolMember, len(spec.IRMembers))
	for _, m := range spec.IRMembers {
		m.NodeID = strings.TrimSpace(m.NodeID)
		if m.NodeID == "" {
			return TopologyReport{}, errors.New("IR member node_id is required")
		}
		if _, dup := irs[m.NodeID]; dup {
			return TopologyReport{}, fmt.Errorf("duplicate IR member %q", m.NodeID)
		}
		n, ok := s.st.Nodes[m.NodeID]
		if !ok {
			return TopologyReport{}, fmt.Errorf("unknown IR node %q", m.NodeID)
		}
		if n.Role != "worker" && n.Role != "master" {
			return TopologyReport{}, fmt.Errorf("IR member %q must have worker or master role", m.NodeID)
		}
		if n.Revoked {
			return TopologyReport{}, fmt.Errorf("IR member %q is revoked", m.NodeID)
		}
		m.UpdatedAt = now.UTC()
		irs[m.NodeID] = m
	}

	routes := make(map[string]ExplicitEXRoute, len(spec.EXRoutes))
	for _, r := range spec.EXRoutes {
		r.ID = strings.TrimSpace(r.ID)
		r.EXNode = strings.TrimSpace(r.EXNode)
		if !topologyRouteIDRe.MatchString(r.ID) {
			return TopologyReport{}, errors.New("EX route id must match [A-Za-z0-9][A-Za-z0-9._-]{0,63}")
		}
		if r.RouteID != "" && !topologyRouteIDRe.MatchString(r.RouteID) {
			return TopologyReport{}, fmt.Errorf("EX route %q route_id is invalid", r.ID)
		}
		if _, dup := routes[r.ID]; dup {
			return TopologyReport{}, fmt.Errorf("duplicate EX route %q", r.ID)
		}
		n, ok := s.st.Nodes[r.EXNode]
		if !ok {
			return TopologyReport{}, fmt.Errorf("unknown EX node %q", r.EXNode)
		}
		if n.Role != "foreign" {
			return TopologyReport{}, fmt.Errorf("EX route %q must reference a foreign node", r.ID)
		}
		if n.Revoked {
			return TopologyReport{}, fmt.Errorf("EX route %q references a revoked node", r.ID)
		}
		if r.Target != "" {
			host, port, err := net.SplitHostPort(r.Target)
			if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || port == "" || port == "0" {
				return TopologyReport{}, fmt.Errorf("EX route %q target must be an explicit loopback IP:port", r.ID)
			}
		}
		r.UpdatedAt = now.UTC()
		routes[r.ID] = r
	}

	oldIR, oldRoutes := s.st.IRPool, s.st.EXRoutes
	oldBindings := cloneTopologyBindings(s.st.TopologyBindings)
	s.st.IRPool = irs
	s.st.EXRoutes = routes
	if err := s.ensureTopologyBindingsLocked(now); err != nil {
		s.st.IRPool, s.st.EXRoutes, s.st.TopologyBindings = oldIR, oldRoutes, oldBindings
		return TopologyReport{}, err
	}
	if err := s.saveLocked(); err != nil {
		s.st.IRPool, s.st.EXRoutes, s.st.TopologyBindings = oldIR, oldRoutes, oldBindings
		return TopologyReport{}, err
	}
	return s.topologyReportLocked(now), nil
}

func cloneTopologyBindings(in map[string]TopologyBinding) map[string]TopologyBinding {
	out := make(map[string]TopologyBinding, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (s *Store) GetTopology(now time.Time) TopologyReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.topologyReportLocked(now)
}

// ReconcileTopology creates only missing desired edges. It never deletes,
// replaces or cancels an existing tunnel. That destructive behavior is
// intentionally outside M-014.
func (s *Store) ReconcileTopology(now time.Time) (TopologyReport, error) {
	s.reconcileMu.Lock()
	defer s.reconcileMu.Unlock()

	s.mu.Lock()
	if err := s.ensureTopologyBindingsLocked(now); err != nil {
		s.mu.Unlock()
		return TopologyReport{}, err
	}
	before := s.topologyReportLocked(now)
	missing := make([]TopologyBinding, 0)
	for _, e := range before.Edges {
		if e.Status == topologyStatusMissing {
			missing = append(missing, e.Binding)
		}
	}
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		return TopologyReport{}, err
	}
	s.mu.Unlock()

	for _, b := range missing {
		s.mu.Lock()
		// Re-check under the same store lock immediately before creation.
		cur := s.edgeStatusLocked(b)
		if cur.Status != topologyStatusMissing {
			s.mu.Unlock()
			continue
		}
		req, err := s.bindingRequestLocked(b)
		if err != nil {
			s.mu.Unlock()
			return TopologyReport{}, err
		}
		_, err = s.createTunnelLocked(req, "", b.Key, now)
		s.mu.Unlock()
		if err != nil {
			return TopologyReport{}, err
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.topologyReportLocked(now), nil
}

func (s *Store) bindingRequestLocked(b TopologyBinding) (TunnelRequest, error) {
	r, ok := s.st.EXRoutes[b.RouteID]
	if !ok {
		return TunnelRequest{}, fmt.Errorf("route %q disappeared", b.RouteID)
	}
	n, ok := s.st.Nodes[b.EXNode]
	if !ok {
		return TunnelRequest{}, fmt.Errorf("EX node %q disappeared", b.EXNode)
	}
	public := strings.TrimSpace(r.PublicAddress)
	if public == "" {
		host, _, err := net.SplitHostPort(n.Address)
		if err != nil {
			host = n.Address
		}
		public = host
	}
	target := strings.TrimSpace(r.Target)
	if target == "" {
		target = "127.0.0.1:2443"
	}
	routeID := strings.TrimSpace(r.RouteID)
	if routeID == "" {
		routeID = r.ID
	}
	return TunnelRequest{
		InstanceID: b.InstanceID, EXNode: b.EXNode, IRNode: b.IRNode,
		PublicAddress: public, Port: b.EXPort, Target: target, RouteID: routeID,
		RouteListen: b.IRRouteListen, EXMetricsListen: b.EXMetricsListen, IRMetricsListen: b.IRMetricsListen,
	}, nil
}

func (s *Store) ensureTopologyBindingsLocked(now time.Time) error {
	if s.st.TopologyBindings == nil {
		s.st.TopologyBindings = map[string]TopologyBinding{}
	}
	irs := make([]string, 0, len(s.st.IRPool))
	for id, m := range s.st.IRPool {
		if m.Enabled {
			irs = append(irs, id)
		}
	}
	routes := make([]string, 0, len(s.st.EXRoutes))
	for id, r := range s.st.EXRoutes {
		if r.Enabled {
			routes = append(routes, id)
		}
	}
	sort.Strings(irs)
	sort.Strings(routes)

	for _, ir := range irs {
		for _, routeID := range routes {
			key := topologyEdgeKey(ir, routeID)
			if b, ok := s.st.TopologyBindings[key]; ok {
				// A route may never silently change which EX an existing
				// binding points at. The admin must use a new route identity.
				if want := s.st.EXRoutes[routeID].EXNode; b.EXNode != want {
					return fmt.Errorf("route %q changed EX from %q to %q; use a new route id for explicit EX replacement", routeID, b.EXNode, want)
				}
				continue
			}
			route := s.st.EXRoutes[routeID]
			exPort, err := s.allocateTopologyPortLocked(route.EXNode, topologyEXPortMin, topologyEXPortMax, "ex")
			if err != nil {
				return err
			}
			routePort, err := s.allocateTopologyPortLocked(ir, topologyRouteMin, topologyRouteMax, "route")
			if err != nil {
				return err
			}
			exMetric, err := s.allocateTopologyPortLocked(route.EXNode, topologyMetricMin, topologyMetricMax, "metric")
			if err != nil {
				return err
			}
			irMetric, err := s.allocateTopologyPortLocked(ir, topologyMetricMin, topologyMetricMax, "metric")
			if err != nil {
				return err
			}
			s.st.TopologyBindings[key] = TopologyBinding{
				Key: key, IRNode: ir, RouteID: routeID, EXNode: route.EXNode,
				InstanceID: topologyInstanceID(ir, routeID), EXPort: exPort,
				IRRouteListen:   "127.0.0.1:" + strconv.Itoa(routePort),
				EXMetricsListen: "127.0.0.1:" + strconv.Itoa(exMetric),
				IRMetricsListen: "127.0.0.1:" + strconv.Itoa(irMetric),
				CreatedAt:       now.UTC(),
			}
		}
	}
	return nil
}

func (s *Store) allocateTopologyPortLocked(node string, min, max int, kind string) (int, error) {
	used := map[int]bool{}
	for _, b := range s.st.TopologyBindings {
		switch kind {
		case "ex":
			if b.EXNode == node {
				used[b.EXPort] = true
			}
		case "route":
			if b.IRNode == node {
				if _, p, err := net.SplitHostPort(b.IRRouteListen); err == nil {
					if n, _ := strconv.Atoi(p); n > 0 {
						used[n] = true
					}
				}
			}
		case "metric":
			if b.EXNode == node {
				if _, p, err := net.SplitHostPort(b.EXMetricsListen); err == nil {
					if n, _ := strconv.Atoi(p); n > 0 {
						used[n] = true
					}
				}
			}
			if b.IRNode == node {
				if _, p, err := net.SplitHostPort(b.IRMetricsListen); err == nil {
					if n, _ := strconv.Atoi(p); n > 0 {
						used[n] = true
					}
				}
			}
		}
	}
	for _, t := range s.st.Tunnels {
		switch kind {
		case "ex":
			if t.EXNode == node {
				used[t.Port] = true
			}
		case "route":
			if t.IRNode == node {
				if _, p, err := net.SplitHostPort(t.RouteListen); err == nil {
					if n, _ := strconv.Atoi(p); n > 0 {
						used[n] = true
					}
				}
			}
		case "metric":
			if t.EXNode == node {
				if _, p, err := net.SplitHostPort(t.EXMetricsListen); err == nil {
					if n, _ := strconv.Atoi(p); n > 0 {
						used[n] = true
					}
				}
			}
			if t.IRNode == node {
				if _, p, err := net.SplitHostPort(t.IRMetricsListen); err == nil {
					if n, _ := strconv.Atoi(p); n > 0 {
						used[n] = true
					}
				}
			}
		}
	}
	for p := min; p <= max; p++ {
		if !used[p] {
			return p, nil
		}
	}
	return 0, fmt.Errorf("no free %s port in %d-%d on node %s", kind, min, max, node)
}

func (s *Store) topologyReportLocked(now time.Time) TopologyReport {
	spec := TopologySpec{}
	for _, m := range s.st.IRPool {
		spec.IRMembers = append(spec.IRMembers, m)
	}
	for _, r := range s.st.EXRoutes {
		spec.EXRoutes = append(spec.EXRoutes, r)
	}
	sort.Slice(spec.IRMembers, func(i, j int) bool { return spec.IRMembers[i].NodeID < spec.IRMembers[j].NodeID })
	sort.Slice(spec.EXRoutes, func(i, j int) bool { return spec.EXRoutes[i].ID < spec.EXRoutes[j].ID })

	desired := map[string]bool{}
	var edges []TopologyEdgeStatus
	for ir, m := range s.st.IRPool {
		if !m.Enabled {
			continue
		}
		for routeID, r := range s.st.EXRoutes {
			if !r.Enabled {
				continue
			}
			key := topologyEdgeKey(ir, routeID)
			desired[key] = true
			if b, ok := s.st.TopologyBindings[key]; ok {
				edges = append(edges, s.edgeStatusLocked(b))
			}
		}
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i].Binding.Key < edges[j].Binding.Key })

	var extras []TopologyExtra
	for _, t := range s.st.Tunnels {
		if t.TopologyKey == "" || desired[t.TopologyKey] {
			continue
		}
		if t.Phase == TunnelActive || !terminalTunnel(t.Phase) || t.Phase == TunnelRollbackFailed {
			extras = append(extras, TopologyExtra{Key: t.TopologyKey, TunnelID: t.ID, Phase: t.Phase})
		}
	}
	sort.Slice(extras, func(i, j int) bool {
		if extras[i].Key == extras[j].Key {
			return extras[i].TunnelID < extras[j].TunnelID
		}
		return extras[i].Key < extras[j].Key
	})
	return TopologyReport{Spec: spec, Edges: edges, Extras: extras, UpdatedAt: now.UTC()}
}

func (s *Store) edgeStatusLocked(b TopologyBinding) TopologyEdgeStatus {
	out := TopologyEdgeStatus{Binding: b, Status: topologyStatusMissing}
	var latest *Tunnel
	for _, raw := range s.st.Tunnels {
		if raw.TopologyKey != b.Key {
			continue
		}
		t := raw
		if latest == nil || t.CreatedAt.After(latest.CreatedAt) {
			latest = &t
		}
		if t.Phase == TunnelActive {
			latest = &t
			break
		}
		if !terminalTunnel(t.Phase) {
			latest = &t
		}
	}
	if latest == nil {
		return out
	}
	t := *latest
	out.TunnelID, out.LastPhase = t.ID, t.Phase
	switch {
	case t.Phase == TunnelRollbackFailed:
		out.Status = topologyStatusBlocked
		out.Problems = []string{"the last topology tunnel has rollback_failed; operator repair is required"}
		return out
	case !terminalTunnel(t.Phase):
		out.Status = topologyStatusBuilding
	case t.Phase == TunnelActive:
		out.Status = topologyStatusActive
	default:
		// rolled_back/superseded are historical, not a live realization.
		out.Status = topologyStatusMissing
		return out
	}
	problems := topologyTunnelMismatch(b, t)
	if len(problems) > 0 {
		out.Status = topologyStatusMismatch
		out.Problems = problems
	}
	return out
}

func topologyTunnelMismatch(b TopologyBinding, t Tunnel) []string {
	var p []string
	bad := func(f string, a ...any) { p = append(p, fmt.Sprintf(f, a...)) }
	if t.TopologyKey != b.Key {
		bad("topology key %q != %q", t.TopologyKey, b.Key)
	}
	if t.InstanceID != b.InstanceID {
		bad("instance %q != %q", t.InstanceID, b.InstanceID)
	}
	if t.IRNode != b.IRNode {
		bad("IR node %q != %q", t.IRNode, b.IRNode)
	}
	if t.EXNode != b.EXNode {
		bad("EX node %q != %q", t.EXNode, b.EXNode)
	}
	if t.Port != b.EXPort {
		bad("EX port %d != %d", t.Port, b.EXPort)
	}
	if t.RouteListen != b.IRRouteListen {
		bad("IR route listen %q != %q", t.RouteListen, b.IRRouteListen)
	}
	if t.EXMetricsListen != b.EXMetricsListen {
		bad("EX metrics %q != %q", t.EXMetricsListen, b.EXMetricsListen)
	}
	if t.IRMetricsListen != b.IRMetricsListen {
		bad("IR metrics %q != %q", t.IRMetricsListen, b.IRMetricsListen)
	}
	return p
}

func (s *Server) topologyAPI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !s.admin(w, r) {
			return
		}
		writeJSON(w, http.StatusOK, s.store.GetTopology(s.now()))
	case http.MethodPost:
		if !s.admin(w, r) {
			return
		}
		s.mutationMu.Lock()
		defer s.mutationMu.Unlock()
		var in TopologySpec
		if err := decodeJSON(r, &in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		details := map[string]any{"ir_members": len(in.IRMembers), "ex_routes": len(in.EXRoutes)}
		report, err := s.store.SetTopologySpec(in, s.now())
		if err != nil {
			s.auditFailure(w, r, "topology.set", "cluster", details, err, http.StatusBadRequest)
			return
		}
		if err := s.auditAdmin(r, "topology.set", "cluster", "success", details); err != nil {
			http.Error(w, "audit log failure", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, report)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) topologyReconcileAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.admin(w, r) {
		return
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	report, err := s.store.ReconcileTopology(s.now())
	if err != nil {
		s.auditFailure(w, r, "topology.reconcile", "cluster", nil, err, http.StatusConflict)
		return
	}
	created := 0
	for _, e := range report.Edges {
		if e.Status == topologyStatusBuilding {
			created++
		}
	}
	details := map[string]any{"edges": len(report.Edges), "building": created, "extras": len(report.Extras)}
	if err := s.auditAdmin(r, "topology.reconcile", "cluster", "success", details); err != nil {
		http.Error(w, "audit log failure", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, report)
}
