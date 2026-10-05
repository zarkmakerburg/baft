package bcc

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/telemetry"
)

func smartIngressStore(t *testing.T, now time.Time) *Store {
	t.Helper()
	s := desiredTopologyStore(t)
	spec := desiredSpec([]string{"ir-1", "ir-2"}, "")
	for i := range spec.IRMembers {
		switch spec.IRMembers[i].NodeID {
		case "ir-1":
			spec.IRMembers[i].IngressIP = "198.51.100.101"
		case "ir-2":
			spec.IRMembers[i].IngressIP = "198.51.100.102"
		}
	}
	for i := range spec.EXRoutes {
		spec.EXRoutes[i].IngressHost = spec.EXRoutes[i].ID + ".ingress.example.test"
	}
	if _, err := s.SetTopologySpec(spec, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReconcileTopology(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	for id, tun := range s.st.Tunnels {
		tun.Phase = TunnelActive
		s.st.Tunnels[id] = tun
	}
	for _, id := range []string{"ir-1", "ir-2", "ex-1", "ex-2", "ex-3", "ex-4", "ex-5"} {
		setIngressNodeHealthyLocked(s, id, now.Add(2*time.Second), 20, 5, 0)
	}
	routes1 := []telemetry.RouteSnapshot{
		{RouteID: "de", Status: "up", LatencyMS: 10, ProbeKind: "tcp"},
		{RouteID: "nl", Status: "up", LatencyMS: 11, ProbeKind: "tcp"},
		{RouteID: "uk", Status: "up", LatencyMS: 12, ProbeKind: "tcp"},
		{RouteID: "us", Status: "up", LatencyMS: 13, ProbeKind: "tcp"},
		{RouteID: "tr", Status: "up", LatencyMS: 14, ProbeKind: "tcp"},
	}
	routes2 := []telemetry.RouteSnapshot{
		{RouteID: "de", Status: "up", LatencyMS: 30, ProbeKind: "tcp"},
		{RouteID: "nl", Status: "up", LatencyMS: 31, ProbeKind: "tcp"},
		{RouteID: "uk", Status: "up", LatencyMS: 32, ProbeKind: "tcp"},
		{RouteID: "us", Status: "up", LatencyMS: 33, ProbeKind: "tcp"},
		{RouteID: "tr", Status: "up", LatencyMS: 34, ProbeKind: "tcp"},
	}
	setIngressRoutesLocked(s, "ir-1", routes1...)
	setIngressRoutesLocked(s, "ir-2", routes2...)
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	s.mu.Unlock()
	at := now.Add(10 * time.Second)
	if _, err := s.EvaluateDistributions(at, DefaultDistributionPolicy()); err != nil {
		t.Fatal(err)
	}
	return s
}

func smartByRoute(v []SmartIngressView) map[string]SmartIngressView {
	out := map[string]SmartIngressView{}
	for _, p := range v {
		out[p.RouteID] = p
	}
	return out
}

func TestSmartIngressTwoIRFiveEXPlansAreIndependent(t *testing.T) {
	now := time.Unix(24000, 0).UTC()
	s := smartIngressStore(t, now)
	at := now.Add(10 * time.Second)
	events, err := s.EvaluateSmartIngress(at)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 5 {
		t.Fatalf("events=%d want 5: %+v", len(events), events)
	}
	plans := smartByRoute(s.SmartIngressSnapshot(at))
	wantEX := map[string]string{"de": "ex-1", "nl": "ex-2", "uk": "ex-3", "us": "ex-4", "tr": "ex-5"}
	if len(plans) != len(wantEX) {
		t.Fatalf("plans=%d want %d: %+v", len(plans), len(wantEX), plans)
	}
	for route, ex := range wantEX {
		p := plans[route]
		if p.EXNode != ex || p.Host != route+".ingress.example.test" || p.State != SmartIngressReady || p.Action != SmartIngressActionPublish || !p.Usable {
			t.Fatalf("route %s invalid plan: %+v", route, p)
		}
		if p.ApplyStatus != SmartIngressApplyPending || p.AppliedGeneration != 0 {
			t.Fatalf("route %s falsely claims applied state: %+v", route, p)
		}
		sum := 0
		for _, ep := range p.Endpoints {
			sum += ep.Weight
			if ep.IP == "" || ep.IRNode == "" {
				t.Fatalf("route %s incomplete endpoint: %+v", route, ep)
			}
		}
		if len(p.Endpoints) != 2 || sum != 100 {
			t.Fatalf("route %s endpoints=%+v", route, p.Endpoints)
		}
	}
}

func TestSmartIngressRouteFailureChangesOnlyAffectedRoute(t *testing.T) {
	now := time.Unix(25000, 0).UTC()
	s := smartIngressStore(t, now)
	at := now.Add(10 * time.Second)
	if _, err := s.EvaluateSmartIngress(at); err != nil {
		t.Fatal(err)
	}
	before := smartByRoute(s.SmartIngressSnapshot(at))

	s.mu.Lock()
	cur := s.st.Telemetry["ir-1"]
	for i := range cur.Routes {
		if cur.Routes[i].RouteID == "tr" {
			cur.Routes[i].Status = "down"
			cur.Routes[i].LatencyMS = 0
		}
	}
	s.st.Telemetry["ir-1"] = cur
	s.mu.Unlock()
	if _, err := s.EvaluateDistributions(at.Add(time.Second), DefaultDistributionPolicy()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EvaluateSmartIngress(at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	after := smartByRoute(s.SmartIngressSnapshot(at.Add(time.Second)))
	for _, route := range []string{"de", "nl", "uk", "us"} {
		if before[route].Generation != after[route].Generation || !sameSmartIngressDesired(before[route].SmartIngressPlan, after[route].SmartIngressPlan) {
			t.Fatalf("unrelated route %s churned: before=%+v after=%+v", route, before[route], after[route])
		}
	}
	tr := after["tr"]
	if tr.Generation <= before["tr"].Generation || !tr.Usable || len(tr.Endpoints) != 1 ||
		tr.Endpoints[0].IRNode != "ir-2" || tr.Endpoints[0].Weight != 100 {
		t.Fatalf("TR failover publication invalid: before=%+v after=%+v", before["tr"], tr)
	}
}

func TestSmartIngressEXFailureIsFailClosedAndNeverSubstitutesEX(t *testing.T) {
	now := time.Unix(26000, 0).UTC()
	s := smartIngressStore(t, now)
	at := now.Add(10 * time.Second)
	if _, err := s.EvaluateSmartIngress(at); err != nil {
		t.Fatal(err)
	}
	before := smartByRoute(s.SmartIngressSnapshot(at))
	s.mu.Lock()
	applied := s.st.SmartIngressPlans["uk"]
	applied.AppliedGeneration = applied.Generation
	applied.AppliedHost = applied.Host
	applied.AppliedEndpoints = append([]SmartIngressEndpoint(nil), applied.Endpoints...)
	applied.ApplyStatus = SmartIngressApplyApplied
	applied.AppliedAt = at
	s.st.SmartIngressPlans["uk"] = applied
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	setIngressHealthLocked(s, "ex-3", HealthDown)
	s.mu.Unlock()
	if _, err := s.EvaluateDistributions(at.Add(time.Second), DefaultDistributionPolicy()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EvaluateSmartIngress(at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	after := smartByRoute(s.SmartIngressSnapshot(at.Add(time.Second)))
	uk := after["uk"]
	if uk.EXNode != "ex-3" || uk.State != SmartIngressUnpublishable || uk.Action != SmartIngressActionWithdraw || uk.Usable || len(uk.Endpoints) != 0 {
		t.Fatalf("failed EX was substituted or remained publishable: %+v", uk)
	}
	if uk.ApplyStatus != SmartIngressApplyPending || uk.AppliedHost != before["uk"].Host ||
		len(uk.AppliedEndpoints) != len(before["uk"].Endpoints) {
		t.Fatalf("withdrawal lost the last applied provider state: before=%+v after=%+v", before["uk"], uk)
	}
	for _, route := range []string{"de", "nl", "us", "tr"} {
		if after[route].Generation != before[route].Generation {
			t.Fatalf("EX failure leaked into route %s: before=%+v after=%+v", route, before[route], after[route])
		}
	}
}

func TestSmartIngressMissingExplicitIRIPFailsClosed(t *testing.T) {
	now := time.Unix(27000, 0).UTC()
	s := smartIngressStore(t, now)
	at := now.Add(10 * time.Second)
	s.mu.Lock()
	m := s.st.IRPool["ir-1"]
	m.IngressIP = ""
	s.st.IRPool["ir-1"] = m
	s.mu.Unlock()
	if _, err := s.EvaluateSmartIngress(at); err != nil {
		t.Fatal(err)
	}
	for _, p := range s.SmartIngressSnapshot(at) {
		if p.State != SmartIngressUnpublishable || p.Usable || p.ApplyStatus != SmartIngressApplyBlocked {
			t.Fatalf("missing explicit IR ingress IP did not fail closed: %+v", p)
		}
	}
}

func TestSmartIngressGenerationTracksM016WithoutNoopChurn(t *testing.T) {
	now := time.Unix(28000, 0).UTC()
	s := smartIngressStore(t, now)
	at := now.Add(10 * time.Second)
	if _, err := s.EvaluateSmartIngress(at); err != nil {
		t.Fatal(err)
	}
	first := smartByRoute(s.SmartIngressSnapshot(at))
	if events, err := s.EvaluateSmartIngress(at.Add(time.Second)); err != nil || len(events) != 0 {
		t.Fatalf("no-op smart ingress evaluation events=%+v err=%v", events, err)
	}
	second := smartByRoute(s.SmartIngressSnapshot(at.Add(time.Second)))
	if second["de"].Generation != first["de"].Generation {
		t.Fatalf("no-op evaluation churned generation: before=%+v after=%+v", first["de"], second["de"])
	}

	afterHold := at.Add(DefaultDistributionPolicy().HoldDown + time.Second)
	s.mu.Lock()
	setDistributionHistoryLocked(s, "ir-1", afterHold, 900*1024*1024, 120)
	setDistributionHistoryLocked(s, "ir-2", afterHold, 10*1024*1024, 2)
	s.mu.Unlock()
	if _, err := s.EvaluateDistributions(afterHold, DefaultDistributionPolicy()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EvaluateSmartIngress(afterHold); err != nil {
		t.Fatal(err)
	}
	third := smartByRoute(s.SmartIngressSnapshot(afterHold))
	if third["de"].DistributionGeneration <= first["de"].DistributionGeneration ||
		third["de"].Generation <= first["de"].Generation ||
		sameSmartIngressDesired(first["de"].SmartIngressPlan, third["de"].SmartIngressPlan) {
		t.Fatalf("M-016 material change did not propagate: before=%+v after=%+v", first["de"], third["de"])
	}
}

func TestSmartIngressPersistsAcrossRestartAndSQLiteMigration(t *testing.T) {
	now := time.Unix(29000, 0).UTC()
	s := smartIngressStore(t, now)
	at := now.Add(10 * time.Second)
	if _, err := s.EvaluateSmartIngress(at); err != nil {
		t.Fatal(err)
	}
	before := smartByRoute(s.SmartIngressSnapshot(at))
	s2, err := OpenStore(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	after := smartByRoute(s2.SmartIngressSnapshot(at.Add(time.Second)))
	if !sameSmartIngressDesired(before["de"].SmartIngressPlan, after["de"].SmartIngressPlan) ||
		before["de"].Generation != after["de"].Generation || before["de"].ApplyStatus != after["de"].ApplyStatus {
		t.Fatalf("Smart Ingress state lost on restart: before=%+v after=%+v", before["de"], after["de"])
	}
	if v, err := stateSchemaVersion(s.Path()); err != nil || v != len(stateMigrations) {
		t.Fatalf("schema version=%d err=%v want=%d", v, err, len(stateMigrations))
	}
}

func TestSmartIngressAPIAuthEvaluateAndAudit(t *testing.T) {
	now := time.Unix(30000, 0).UTC()
	s := smartIngressStore(t, now)
	app, err := NewServer(s, "admin")
	if err != nil {
		t.Fatal(err)
	}
	app.now = func() time.Time { return now.Add(10 * time.Second) }

	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, authReq(http.MethodGet, "/api/ingress/smart", "", nil))
	if rr.Code == http.StatusOK {
		t.Fatal("unauthenticated Smart Ingress read was accepted")
	}

	rr = httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, authReq(http.MethodPost, "/api/ingress/smart/evaluate", "admin", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("evaluate status=%d body=%s", rr.Code, rr.Body.String())
	}
	entries, err := app.audit.List(200)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Action == "ingress.smart.desired" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("Smart Ingress desired-state change was not audited")
	}
}

func TestSmartIngressTopologyInputsValidateButRemainOptional(t *testing.T) {
	now := time.Unix(31000, 0).UTC()
	s := desiredTopologyStore(t)
	spec := desiredSpec([]string{"ir-1", "ir-2"}, "")
	spec.IRMembers[0].IngressIP = "10.0.0.1"
	if _, err := s.SetTopologySpec(spec, now); err == nil {
		t.Fatal("private ingress_ip was accepted")
	}
	spec = desiredSpec([]string{"ir-1", "ir-2"}, "")
	spec.EXRoutes[0].IngressHost = "not a hostname"
	if _, err := s.SetTopologySpec(spec, now); err == nil {
		t.Fatal("invalid ingress_host was accepted")
	}
	// Empty Smart Ingress fields remain valid topology for backward compatibility;
	// publication itself will fail closed until they are explicitly configured.
	spec = desiredSpec([]string{"ir-1", "ir-2"}, "")
	if _, err := s.SetTopologySpec(spec, now); err != nil {
		t.Fatalf("optional Smart Ingress inputs broke existing topology: %v", err)
	}
}

func TestSmartIngressStateDBRoundTripStandalone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.st.SmartIngressPlans["de"] = SmartIngressPlan{
		RouteID: "de", EXNode: "ex-1", Host: "de.ingress.example.test", State: SmartIngressReady, Action: SmartIngressActionPublish,
		Endpoints: []SmartIngressEndpoint{{IRNode: "ir-1", IP: "198.51.100.101", Weight: 100}},
		DistributionGeneration: 4, Generation: 7, AppliedGeneration: 6, AppliedHost: "old.ingress.example.test",
		AppliedEndpoints: []SmartIngressEndpoint{{IRNode: "ir-2", IP: "198.51.100.102", Weight: 100}}, ApplyStatus: SmartIngressApplyPending,
	}
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	s.mu.Unlock()
	s2, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s2.mu.Lock()
	got := s2.st.SmartIngressPlans["de"]
	s2.mu.Unlock()
	if got.Generation != 7 || got.DistributionGeneration != 4 || len(got.Endpoints) != 1 ||
		got.Endpoints[0].IP != "198.51.100.101" || got.AppliedGeneration != 6 ||
		got.AppliedHost != "old.ingress.example.test" || len(got.AppliedEndpoints) != 1 {
		t.Fatalf("standalone Smart Ingress DB round-trip mismatch: %+v", got)
	}
}
