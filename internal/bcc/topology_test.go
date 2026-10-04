package bcc

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func desiredTopologyStore(t *testing.T) *Store {
	t.Helper()
	s := tunnelStore(t)
	for _, n := range []Node{
		{ID: "ir-2", Address: "198.51.100.12:22", Role: "worker"},
		{ID: "ex-2", Address: "203.0.113.12:22", Role: "foreign"},
		{ID: "ex-3", Address: "203.0.113.13:22", Role: "foreign"},
		{ID: "ex-4", Address: "203.0.113.14:22", Role: "foreign"},
		{ID: "ex-5", Address: "203.0.113.15:22", Role: "foreign"},
	} {
		if _, err := s.UpsertNode(n, "tok-"+n.ID); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func desiredSpec(irs []string, disabledRoute string) TopologySpec {
	spec := TopologySpec{}
	for _, id := range irs {
		spec.IRMembers = append(spec.IRMembers, IRPoolMember{NodeID: id, Enabled: true})
	}
	for i, id := range []string{"de", "nl", "uk", "us", "tr"} {
		ex := "ex-" + string(rune('1'+i))
		spec.EXRoutes = append(spec.EXRoutes, ExplicitEXRoute{
			ID: id, EXNode: ex, Country: strings.ToUpper(id), Enabled: id != disabledRoute,
			Target: "127.0.0.1:28443",
		})
	}
	return spec
}

func reportBindings(r TopologyReport) map[string]TopologyBinding {
	out := map[string]TopologyBinding{}
	for _, e := range r.Edges {
		out[e.Binding.Key] = e.Binding
	}
	return out
}

func TestDesiredTopologyTwoIRFiveExplicitEXMakesTenStableEdges(t *testing.T) {
	s := desiredTopologyStore(t)
	now := time.Unix(100, 0).UTC()
	r, err := s.SetTopologySpec(desiredSpec([]string{"ir-1", "ir-2"}, ""), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Edges) != 10 {
		t.Fatalf("edges=%d, want 10", len(r.Edges))
	}
	for _, e := range r.Edges {
		if e.Status != topologyStatusMissing {
			t.Fatalf("%s status=%s", e.Binding.Key, e.Status)
		}
		if e.Binding.InstanceID == "" || e.Binding.EXPort == 0 || e.Binding.IRRouteListen == "" ||
			e.Binding.EXMetricsListen == "" || e.Binding.IRMetricsListen == "" {
			t.Fatalf("incomplete binding: %+v", e.Binding)
		}
	}

	before := reportBindings(r)
	if _, err := s.UpsertNode(Node{ID: "ir-3", Address: "198.51.100.13:22", Role: "worker"}, "tok-ir-3"); err != nil {
		t.Fatal(err)
	}
	r2, err := s.SetTopologySpec(desiredSpec([]string{"ir-1", "ir-2", "ir-3"}, ""), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(r2.Edges) != 15 {
		t.Fatalf("edges=%d, want 15", len(r2.Edges))
	}
	after := reportBindings(r2)
	for key, old := range before {
		if got := after[key]; !reflect.DeepEqual(got, old) {
			t.Fatalf("binding %s churned:\nold=%+v\nnew=%+v", key, old, got)
		}
	}

	perIRRoute := map[string]map[string]bool{}
	perEXPort := map[string]map[int]bool{}
	for _, e := range r2.Edges {
		b := e.Binding
		if perIRRoute[b.IRNode] == nil {
			perIRRoute[b.IRNode] = map[string]bool{}
		}
		if perIRRoute[b.IRNode][b.IRRouteListen] {
			t.Fatalf("duplicate IR route listener on %s: %s", b.IRNode, b.IRRouteListen)
		}
		perIRRoute[b.IRNode][b.IRRouteListen] = true
		if perEXPort[b.EXNode] == nil {
			perEXPort[b.EXNode] = map[int]bool{}
		}
		if perEXPort[b.EXNode][b.EXPort] {
			t.Fatalf("duplicate EX port on %s: %d", b.EXNode, b.EXPort)
		}
		perEXPort[b.EXNode][b.EXPort] = true
	}
}

func TestTopologyReconcileIsIdempotentAndDisableIsNonDestructive(t *testing.T) {
	s := desiredTopologyStore(t)
	now := time.Unix(200, 0).UTC()
	spec := desiredSpec([]string{"ir-1", "ir-2"}, "")
	if _, err := s.SetTopologySpec(spec, now); err != nil {
		t.Fatal(err)
	}
	first, err := s.ReconcileTopology(now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Edges) != 10 {
		t.Fatalf("edges=%d", len(first.Edges))
	}
	for _, e := range first.Edges {
		if e.Status != topologyStatusBuilding {
			t.Fatalf("%s status=%s, want BUILDING", e.Binding.Key, e.Status)
		}
	}
	tunnels1, jobs1 := len(s.ListTunnels()), len(s.ListJobs())
	if tunnels1 != 10 || jobs1 != 10 {
		t.Fatalf("first reconcile tunnels=%d jobs=%d, want 10/10", tunnels1, jobs1)
	}
	second, err := s.ReconcileTopology(now.Add(2 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.ListTunnels()) != tunnels1 || len(s.ListJobs()) != jobs1 {
		t.Fatalf("second reconcile duplicated state: tunnels=%d jobs=%d", len(s.ListTunnels()), len(s.ListJobs()))
	}
	for _, e := range second.Edges {
		if e.Status != topologyStatusBuilding {
			t.Fatalf("%s second status=%s", e.Binding.Key, e.Status)
		}
	}

	disabled := desiredSpec([]string{"ir-1", "ir-2"}, "tr")
	report, err := s.SetTopologySpec(disabled, now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Edges) != 8 {
		t.Fatalf("enabled edges=%d, want 8", len(report.Edges))
	}
	if len(report.Extras) != 2 {
		t.Fatalf("extras=%d, want 2: %+v", len(report.Extras), report.Extras)
	}
	if len(s.ListTunnels()) != 10 {
		t.Fatal("disabling a route destructively removed tunnels")
	}
}

func TestTopologyStatePersistsBindingsAcrossRestart(t *testing.T) {
	s := desiredTopologyStore(t)
	now := time.Unix(300, 0).UTC()
	spec := desiredSpec([]string{"ir-1", "ir-2"}, "")
	before, err := s.SetTopologySpec(spec, now)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := OpenStore(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	after := s2.GetTopology(now.Add(time.Minute))
	if !reflect.DeepEqual(reportBindings(before), reportBindings(after)) {
		t.Fatalf("bindings changed across restart")
	}
	if len(after.Spec.IRMembers) != 2 || len(after.Spec.EXRoutes) != 5 {
		t.Fatalf("spec lost across restart: %+v", after.Spec)
	}
}

func TestTopologyRouteCannotSilentlyChangeEX(t *testing.T) {
	s := desiredTopologyStore(t)
	now := time.Unix(400, 0).UTC()
	spec := TopologySpec{
		IRMembers: []IRPoolMember{{NodeID: "ir-1", Enabled: true}},
		EXRoutes:  []ExplicitEXRoute{{ID: "de", EXNode: "ex-1", Enabled: true}},
	}
	before, err := s.SetTopologySpec(spec, now)
	if err != nil {
		t.Fatal(err)
	}
	spec.EXRoutes[0].EXNode = "ex-2"
	if _, err := s.SetTopologySpec(spec, now.Add(time.Minute)); err == nil || !strings.Contains(err.Error(), "changed EX") {
		t.Fatalf("silent EX replacement not rejected: %v", err)
	}
	after := s.GetTopology(now.Add(2 * time.Minute))
	if after.Spec.EXRoutes[0].EXNode != "ex-1" {
		t.Fatalf("failed update mutated desired state: %+v", after.Spec.EXRoutes)
	}
	if !reflect.DeepEqual(reportBindings(before), reportBindings(after)) {
		t.Fatal("failed update mutated bindings")
	}
}

func TestTopologyRollbackFailedBlocksAutomaticRetry(t *testing.T) {
	s := desiredTopologyStore(t)
	now := time.Unix(500, 0).UTC()
	spec := TopologySpec{
		IRMembers: []IRPoolMember{{NodeID: "ir-1", Enabled: true}},
		EXRoutes:  []ExplicitEXRoute{{ID: "de", EXNode: "ex-1", Enabled: true}},
	}
	if _, err := s.SetTopologySpec(spec, now); err != nil {
		t.Fatal(err)
	}
	r, err := s.ReconcileTopology(now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	id := r.Edges[0].TunnelID
	s.mu.Lock()
	tun := s.st.Tunnels[id]
	tun.Phase = TunnelRollbackFailed
	s.st.Tunnels[id] = tun
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	s.mu.Unlock()

	tunnels := len(s.ListTunnels())
	r2, err := s.ReconcileTopology(now.Add(2 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.ListTunnels()) != tunnels {
		t.Fatal("rollback_failed edge was retried automatically")
	}
	if r2.Edges[0].Status != topologyStatusBlocked {
		t.Fatalf("status=%s, want BLOCKED", r2.Edges[0].Status)
	}
}

func TestTopologyInstanceIDsAreStableAndBounded(t *testing.T) {
	ids := []string{
		topologyInstanceID("ir-main", "de"),
		topologyInstanceID("ir-backup", "de"),
		topologyInstanceID("an-extremely-long-ir-node-name-that-needs-truncation", "very-long-route-name-that-needs-truncation"),
	}
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	if ids[0] == ids[1] {
		t.Fatal("distinct edges share an instance id")
	}
	for _, id := range ids {
		if len(id) > 40 || !strings.HasPrefix(id, "topo-") {
			t.Fatalf("bad instance id %q len=%d", id, len(id))
		}
	}
}

func TestTopologyAPISetReadAndReconcile(t *testing.T) {
	s := desiredTopologyStore(t)
	app, err := NewServer(s, "admin")
	if err != nil {
		t.Fatal(err)
	}
	spec := desiredSpec([]string{"ir-1", "ir-2"}, "")
	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, authReq(http.MethodPost, "/api/topology", "admin", spec))
	if rr.Code != http.StatusOK {
		t.Fatalf("set topology status=%d body=%s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, authReq(http.MethodGet, "/api/topology", "admin", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "ir-1|de") {
		t.Fatalf("get topology status=%d body=%s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, authReq(http.MethodPost, "/api/topology/reconcile", "admin", nil))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("reconcile status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(s.ListTunnels()) != 10 {
		t.Fatalf("reconcile tunnels=%d, want 10", len(s.ListTunnels()))
	}
}
