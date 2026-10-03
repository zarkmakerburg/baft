package bcc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/tunnelnode"
)

func discTunnel() Tunnel {
	return Tunnel{
		ID: "tun-x", EXNode: "ex-1", IRNode: "ir-1", RouteID: "service-main", RouteListen: "127.0.0.1:1443",
		PublicAddress: "203.0.113.5", Port: 8443, Target: "127.0.0.1:2443", Phase: TunnelActive,
		ObservedGen: map[string]int{"ir-1": 4},
		Digests:     map[string]NodeDigests{"ir-1": {Config: strings.Repeat("a", 64), Unit: strings.Repeat("b", 64), Marker: strings.Repeat("c", 64)}},
	}
}

func managedInstance(t Tunnel) tunnelnode.DiscoveredInstance {
	return tunnelnode.DiscoveredInstance{
		Unit: "baft.service", Primary: true, Present: true, Recognized: true, ConfigPresent: true, ConfigLoads: true,
		ConfigPath: "/etc/baft/baft.yaml", ConfigSHA256: strings.Repeat("a", 64), UnitSHA256: strings.Repeat("b", 64), ServiceState: "active",
		UnitHeaderManaged: true, UnitHeaderTunnel: t.ID,
		ConfigRole: "dialer", RouteID: t.RouteID, RouteListen: t.RouteListen, PeerAddress: "203.0.113.5:8443",
		Marker: &tunnelnode.DiscoveredMarker{ManagedBy: "baft", TunnelID: t.ID, Generation: 4, Role: "ir",
			ConfigSHA256: strings.Repeat("a", 64), UnitSHA256: strings.Repeat("b", 64), FileSHA256: strings.Repeat("c", 64)},
	}
}

func discNode() Node { return Node{ID: "ir-1"} }

func classifyOne(t *testing.T, mutateT func(*Tunnel), mutateI func(*tunnelnode.DiscoveredInstance)) DiscoveredView {
	t.Helper()
	tn := discTunnel()
	if mutateT != nil {
		mutateT(&tn)
	}
	in := managedInstance(tn)
	if mutateI != nil {
		mutateI(&in)
	}
	views := classifyDiscovery(discNode(), map[string]Tunnel{tn.ID: tn}, tunnelnode.DiscoveryReport{Version: 1, Service: "baft", NodeGeneration: 4, Instances: []tunnelnode.DiscoveredInstance{in}})
	for _, v := range views {
		if v.Unit == in.Unit {
			return v
		}
	}
	t.Fatalf("no view for %s: %+v", in.Unit, views)
	return DiscoveredView{}
}

func TestDiscoveryClassification(t *testing.T) {
	cases := []struct {
		name string
		t    func(*Tunnel)
		i    func(*tunnelnode.DiscoveredInstance)
		want string
	}{
		{"managed", nil, nil, DiscManaged},
		{"config edited and marker rewritten to match (coupled tamper)", nil, func(i *tunnelnode.DiscoveredInstance) {
			i.ConfigSHA256 = strings.Repeat("d", 64)
			i.Marker.ConfigSHA256 = strings.Repeat("d", 64)
			i.Marker.FileSHA256 = strings.Repeat("e", 64)
		}, DiscDrifted},
		{"service stopped", nil, func(i *tunnelnode.DiscoveredInstance) { i.ServiceState = "inactive" }, DiscDrifted},
		{"wrong peer", nil, func(i *tunnelnode.DiscoveredInstance) { i.PeerAddress = "198.51.100.1:1" }, DiscDrifted},
		{"no verified baseline in BCC", func(t *Tunnel) { t.Digests = nil }, nil, DiscUnknown},
		{"no marker on a primary BCC expects to own", nil, func(i *tunnelnode.DiscoveredInstance) {
			i.Marker = nil
			i.UnitHeaderManaged, i.UnitHeaderTunnel = false, ""
		}, DiscConflict},
		{"no marker, not primary", nil, func(i *tunnelnode.DiscoveredInstance) {
			i.Marker, i.Primary, i.Unit = nil, false, "baft-legacy.service"
			i.UnitHeaderManaged, i.UnitHeaderTunnel = false, ""
		}, DiscUnmanaged},
		{"unit claims BAFT management but no marker", nil, func(i *tunnelnode.DiscoveredInstance) { i.Marker, i.Primary, i.Unit = nil, false, "baft-x.service" }, DiscConflict},
		{"foreign marker", nil, func(i *tunnelnode.DiscoveredInstance) { i.Marker.ManagedBy = "other" }, DiscConflict},
		{"marker names a tunnel BCC does not know", nil, func(i *tunnelnode.DiscoveredInstance) {
			i.Marker.TunnelID, i.UnitHeaderTunnel = "tun-ghost", "tun-ghost"
		}, DiscConflict},
		{"marker tunnel belongs to other nodes", func(t *Tunnel) { t.EXNode, t.IRNode = "ex-9", "ir-9" }, nil, DiscConflict},
		{"marker role contradicts BCC", nil, func(i *tunnelnode.DiscoveredInstance) { i.Marker.Role = "ex" }, DiscConflict},
		{"unit header and marker disagree", nil, func(i *tunnelnode.DiscoveredInstance) { i.UnitHeaderTunnel = "tun-other" }, DiscConflict},
		{"tunnel superseded in BCC", func(t *Tunnel) { t.Phase = TunnelSuperseded }, nil, DiscConflict},
		{"tunnel rolled back in BCC", func(t *Tunnel) { t.Phase = TunnelRolledBack }, nil, DiscConflict},
		{"tunnel mid-change in BCC", func(t *Tunnel) { t.Phase = TunnelCommittingIR }, nil, DiscUnknown},
		{"second unit shares the marker", nil, func(i *tunnelnode.DiscoveredInstance) { i.Primary, i.Unit = false, "baft-copy.service" }, DiscConflict},
		{"unit not recognized", nil, func(i *tunnelnode.DiscoveredInstance) { i.Recognized, i.Problem = false, "not a BAFT transport unit" }, DiscUnknown},
		{"config does not load", nil, func(i *tunnelnode.DiscoveredInstance) { i.ConfigLoads, i.Problem = false, "config does not load" }, DiscUnknown},
		{"marker file unreadable", nil, func(i *tunnelnode.DiscoveredInstance) {
			i.Marker, i.MarkerProblem = nil, "marker file is not valid JSON"
		}, DiscUnknown},
	}
	for _, c := range cases {
		v := classifyOne(t, c.t, c.i)
		if v.State != c.want || len(v.Reasons) == 0 {
			t.Errorf("%s: %s %v, want %s with a reason", c.name, v.State, v.Reasons, c.want)
		}
	}
}

func TestDiscoveryNeverCallsAnythingManagedWithoutBCCsOwnProof(t *testing.T) {
	// A marker is a claim. MANAGED needs an active tunnel in BCC for this node,
	// the same role, and BCC's own digests; any gap is not MANAGED.
	for name, mutate := range map[string]func(*Tunnel){
		"no tunnel": nil, "no digests": func(t *Tunnel) { t.Digests = nil },
		"digest mismatch": func(t *Tunnel) {
			t.Digests["ir-1"] = NodeDigests{Config: strings.Repeat("0", 64), Unit: strings.Repeat("b", 64), Marker: strings.Repeat("c", 64)}
		},
	} {
		tn := discTunnel()
		tunnels := map[string]Tunnel{}
		if mutate != nil {
			mutate(&tn)
			tunnels[tn.ID] = tn
		}
		views := classifyDiscovery(discNode(), tunnels, tunnelnode.DiscoveryReport{Version: 1, Service: "baft", NodeGeneration: 4, Instances: []tunnelnode.DiscoveredInstance{managedInstance(discTunnel())}})
		for _, v := range views {
			if v.State == DiscManaged {
				t.Errorf("%s: classified MANAGED", name)
			}
		}
	}
}

func TestDiscoveryMissingPrimaryUnit(t *testing.T) {
	tn := discTunnel()
	rep := tunnelnode.DiscoveryReport{Version: 1, Service: "baft", Instances: []tunnelnode.DiscoveredInstance{{Unit: "baft.service", Primary: true}}}
	views := classifyDiscovery(discNode(), map[string]Tunnel{tn.ID: tn}, rep)
	if len(views) != 1 || views[0].State != DiscMissing || views[0].Tunnel != tn.ID {
		t.Fatalf("active tunnel but no unit: %+v", views)
	}
	// BCC expects nothing here: nothing is reported as missing.
	if views := classifyDiscovery(discNode(), map[string]Tunnel{}, rep); len(views) != 0 {
		t.Fatalf("nothing expected, nothing missing: %+v", views)
	}
	// A present but unreadable primary is UNKNOWN, never MISSING.
	rep.Instances[0] = tunnelnode.DiscoveredInstance{Unit: "baft.service", Primary: true, Present: true, Problem: "unit file is a symlink; not followed"}
	if views := classifyDiscovery(discNode(), map[string]Tunnel{tn.ID: tn}, rep); len(views) != 1 || views[0].State != DiscUnknown {
		t.Fatalf("unreadable primary: %+v", views)
	}
}

// ---- store, audit, API ----

func discReport(t *testing.T, in ...tunnelnode.DiscoveredInstance) string {
	t.Helper()
	b, err := json.Marshal(tunnelnode.DiscoveryReport{Version: tunnelnode.DiscoveryVersion, Service: "baft", NodeGeneration: 4, Instances: in})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func finishDiscovery(t *testing.T, s *Store, node, status, message string) {
	t.Helper()
	jobs := pullAll(t, s, node)
	if len(jobs) != 1 || jobs[0].Type != JobTunnelDiscover || len(jobs[0].Params) != 0 {
		t.Fatalf("discovery job: %+v", jobs)
	}
	if err := s.AckJob(node, "tok-"+node, jobs[0].ID, status, message); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoveryFlowStoresClassifiedInventoryAndAuditsChanges(t *testing.T) {
	s := tunnelStore(t)
	app, _ := NewServer(s, "admin")
	now := time.Now()
	// Make tun-x active in BCC's own records.
	tn := discTunnel()
	s.mu.Lock()
	if s.st.Tunnels == nil {
		s.st.Tunnels = map[string]Tunnel{}
	}
	s.st.Tunnels[tn.ID] = tn
	s.mu.Unlock()

	if _, err := s.StartDiscovery("ir-1", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartDiscovery("ir-1", now); err == nil {
		t.Fatal("two discoveries at once")
	}
	if _, err := s.StartDiscovery("nope", now); err == nil {
		t.Fatal("unknown node accepted")
	}
	legacy := managedInstance(tn)
	legacy.Unit, legacy.Primary, legacy.Marker, legacy.UnitHeaderManaged, legacy.UnitHeaderTunnel = "baft-legacy.service", false, nil, false, ""
	finishDiscovery(t, s, "ir-1", "succeeded", discReport(t, managedInstance(tn), legacy))
	evs, err := s.AdvanceDiscovery(now)
	if err != nil || len(evs) != 1 {
		t.Fatalf("events %v %v", evs, err)
	}
	app.auditDiscovery(evs)
	d := s.DiscoverySnapshot("ir-1")[0]
	if d.Pending || d.Problem != "" || d.Summary[DiscManaged] != 1 || d.Summary[DiscUnmanaged] != 1 || len(d.Instances) != 2 {
		t.Fatalf("stored: %+v", d)
	}
	if !evs[0].Changed {
		t.Fatal("the first discovery is a change")
	}

	// The same answer again is not a change; a different one is.
	for _, c := range []struct {
		report  string
		changed bool
	}{
		{discReport(t, managedInstance(tn), legacy), false},
		{discReport(t, managedInstance(tn)), true},
	} {
		if _, err := s.StartDiscovery("ir-1", now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		finishDiscovery(t, s, "ir-1", "succeeded", c.report)
		evs, _ = s.AdvanceDiscovery(now)
		if len(evs) != 1 || evs[0].Changed != c.changed {
			t.Fatalf("changed=%v, want %v", evs[0].Changed, c.changed)
		}
		app.auditDiscovery(evs)
	}
	entries, _ := app.audit.List(0)
	n := 0
	for _, e := range entries {
		if e.Action == "discovery.completed" {
			n++
			if e.Details["states"] == nil || e.Details["summary"] == nil {
				t.Fatalf("audit details: %v", e.Details)
			}
		}
	}
	if n != 3 {
		t.Fatalf("%d discovery audit entries, want 3", n)
	}

	// Failure, garbage, and a node that never answers are recorded as problems,
	// not as findings.
	s.StartDiscovery("ir-1", now.Add(2*time.Minute))
	finishDiscovery(t, s, "ir-1", "failed", "tunnel changes are not enabled on this agent")
	s.AdvanceDiscovery(now)
	if d := s.DiscoverySnapshot("ir-1")[0]; !strings.Contains(d.Problem, "not enabled") || len(d.Instances) != 0 {
		t.Fatalf("failed: %+v", d)
	}
	s.StartDiscovery("ir-1", now.Add(3*time.Minute))
	finishDiscovery(t, s, "ir-1", "succeeded", "{not json")
	s.AdvanceDiscovery(now)
	if d := s.DiscoverySnapshot("ir-1")[0]; !strings.Contains(d.Problem, "unreadable") {
		t.Fatalf("garbage: %+v", d)
	}
	late := now.Add(time.Hour)
	s.StartDiscovery("ex-1", late)
	s.AdvanceDiscovery(late.Add(discoveryTimeout + time.Second))
	if d := s.DiscoverySnapshot("ex-1")[0]; d.Pending || !strings.Contains(d.Problem, "did not answer") {
		t.Fatalf("timeout: %+v", d)
	}

	// The inventory survives a restart (state migration 4).
	re, err := OpenStore(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if d := re.DiscoverySnapshot("ex-1")[0]; !strings.Contains(d.Problem, "did not answer") {
		t.Fatalf("after restart: %+v", d)
	}
	// Revoked nodes are not asked.
	s.mu.Lock()
	nd := s.st.Nodes["ir-1"]
	nd.Revoked = true
	s.st.Nodes["ir-1"] = nd
	s.mu.Unlock()
	if _, err := s.StartDiscovery("ir-1", late); err == nil {
		t.Fatal("a revoked node was asked")
	}
}

func TestDiscoveryAPIIsAdminOnlyTakesNoPathAndCreatesOnlyAReadOnlyJob(t *testing.T) {
	s := tunnelStore(t)
	app, _ := NewServer(s, "admin")
	do := func(method, url, token string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		app.Handler().ServeHTTP(rr, authReq(method, url, token, nil))
		return rr
	}
	for _, m := range []string{http.MethodGet, http.MethodPost} {
		if rr := do(m, "/api/discovery?node=ir-1", "wrong"); rr.Code != http.StatusUnauthorized {
			t.Fatalf("%s unauthenticated: %d", m, rr.Code)
		}
	}
	if rr := do(http.MethodPost, "/api/discovery", "admin"); rr.Code != 400 {
		t.Fatalf("POST without a node: %d", rr.Code)
	}
	if rr := do(http.MethodPost, "/api/discovery?node=%2Fetc%2Fshadow", "admin"); rr.Code != 400 {
		t.Fatalf("POST with a path as node: %d", rr.Code)
	}
	if rr := do(http.MethodPut, "/api/discovery?node=ir-1", "admin"); rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT: %d", rr.Code)
	}
	rr := do(http.MethodPost, "/api/discovery?all=1", "admin")
	if rr.Code != 202 {
		t.Fatalf("POST all: %d %s", rr.Code, rr.Body.String())
	}
	jobs := s.ListJobs()
	if len(jobs) != 2 {
		t.Fatalf("%d jobs for two nodes", len(jobs))
	}
	for _, j := range jobs {
		if j.Type != JobTunnelDiscover || len(j.Params) != 0 {
			t.Fatalf("job %+v: discovery must be a parameterless read-only job", j)
		}
	}
	if rr := do(http.MethodGet, "/api/discovery", "admin"); rr.Code != 200 || !strings.Contains(rr.Body.String(), `"pending":true`) {
		t.Fatalf("GET: %d %s", rr.Code, rr.Body.String())
	}
	entries, _ := app.audit.List(0)
	starts, failed := 0, 0
	for _, e := range entries {
		if e.Action == "discovery.start" && e.Outcome == "success" {
			starts++
		}
		if e.Action == "discovery.start" && e.Outcome == "failure" {
			failed++
		}
	}
	if starts != 2 || failed != 1 {
		t.Fatalf("discovery.start audit entries: %d success, %d failure (want 2 and the refused path attempt)", starts, failed)
	}
}
