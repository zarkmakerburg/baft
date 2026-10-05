package bcc

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/telemetry"
)

func setDistributionHistoryLocked(s *Store, id string, now time.Time, bytes uint64, sessions uint64) {
	cur := s.st.Telemetry[id]
	cur.ActiveSessions = sessions
	s.st.Telemetry[id] = cur
	s.st.History[id] = []HistoryPoint{
		{Timestamp: now.Add(-2 * time.Minute), IngressBytes: 0, EgressBytes: 0, ActiveSessions: sessions},
		{Timestamp: now, IngressBytes: bytes, EgressBytes: bytes, ActiveSessions: sessions},
	}
}

func oneDistribution(t *testing.T, s *Store, now time.Time) DistributionView {
	t.Helper()
	got := s.DistributionSnapshot(now)
	if len(got) != 1 {
		t.Fatalf("distributions=%d want 1: %+v", len(got), got)
	}
	return got[0]
}

func TestDistributionInitialWeightsSumTo100(t *testing.T) {
	now := time.Unix(11000, 0).UTC()
	s := ingressStore(t, now)
	at := now.Add(10 * time.Second)

	events, err := s.EvaluateDistributions(at, DefaultDistributionPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events=%d want 1: %+v", len(events), events)
	}
	got := oneDistribution(t, s, at)
	if got.State != DistributionReady || !got.Usable {
		t.Fatalf("unexpected distribution: %+v", got)
	}
	if got.Weights["ir-1"]+got.Weights["ir-2"] != 100 {
		t.Fatalf("weights do not sum to 100: %+v", got.Weights)
	}
	if got.Weights["ir-1"] <= got.Weights["ir-2"] {
		t.Fatalf("lower-latency IR did not receive more weight: %+v", got.Weights)
	}
}

func TestDistributionBusyIRReceivesLessNewConnectionWeight(t *testing.T) {
	now := time.Unix(12000, 0).UTC()
	s := ingressStore(t, now)
	at := now.Add(10 * time.Second)

	s.mu.Lock()
	// Equalize route latency so current load is the dominant differentiator.
	setIngressRoutesLocked(s, "ir-1", telemetry.RouteSnapshot{RouteID: "de", Status: "up", LatencyMS: 20, ProbeKind: "tcp"})
	setIngressRoutesLocked(s, "ir-2", telemetry.RouteSnapshot{RouteID: "de", Status: "up", LatencyMS: 20, ProbeKind: "tcp"})
	setDistributionHistoryLocked(s, "ir-1", at, 900*1024*1024, 120)
	setDistributionHistoryLocked(s, "ir-2", at, 30*1024*1024, 10)
	s.mu.Unlock()

	if _, err := s.EvaluateDistributions(at, DefaultDistributionPolicy()); err != nil {
		t.Fatal(err)
	}
	got := oneDistribution(t, s, at)
	if got.Weights["ir-1"] >= got.Weights["ir-2"] {
		t.Fatalf("busy IR did not receive less weight: %+v candidates=%+v", got.Weights, got.Candidates)
	}
}

func TestDistributionCapacityHintAffectsWeights(t *testing.T) {
	now := time.Unix(13000, 0).UTC()
	s := ingressStore(t, now)
	at := now.Add(10 * time.Second)

	s.mu.Lock()
	m1 := s.st.IRPool["ir-1"]
	m2 := s.st.IRPool["ir-2"]
	m1.CapacityWeight = 200
	m2.CapacityWeight = 100
	s.st.IRPool["ir-1"] = m1
	s.st.IRPool["ir-2"] = m2
	setIngressRoutesLocked(s, "ir-1", telemetry.RouteSnapshot{RouteID: "de", Status: "up", LatencyMS: 20, ProbeKind: "tcp"})
	setIngressRoutesLocked(s, "ir-2", telemetry.RouteSnapshot{RouteID: "de", Status: "up", LatencyMS: 20, ProbeKind: "tcp"})
	s.mu.Unlock()

	if _, err := s.EvaluateDistributions(at, DefaultDistributionPolicy()); err != nil {
		t.Fatal(err)
	}
	got := oneDistribution(t, s, at)
	if got.Weights["ir-1"] <= got.Weights["ir-2"] {
		t.Fatalf("capacity hint not reflected: %+v", got.Weights)
	}
}

func TestDistributionRouteFailureOnlyZeroesAffectedRouteIR(t *testing.T) {
	now := time.Unix(14000, 0).UTC()
	s := desiredTopologyStore(t)
	spec := TopologySpec{
		IRMembers: []IRPoolMember{{NodeID: "ir-1", Enabled: true}, {NodeID: "ir-2", Enabled: true}},
		EXRoutes: []ExplicitEXRoute{
			{ID: "de", EXNode: "ex-1", Enabled: true, Target: "127.0.0.1:28443"},
			{ID: "tr", EXNode: "ex-2", Enabled: true, Target: "127.0.0.1:28443"},
		},
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
	for _, id := range []string{"ir-1", "ir-2", "ex-1", "ex-2"} {
		setIngressNodeHealthyLocked(s, id, now.Add(2*time.Second), 30, 5, 0)
	}
	setIngressRoutesLocked(s, "ir-1",
		telemetry.RouteSnapshot{RouteID: "de", Status: "up", LatencyMS: 10, ProbeKind: "tcp"},
		telemetry.RouteSnapshot{RouteID: "tr", Status: "up", LatencyMS: 10, ProbeKind: "tcp"})
	setIngressRoutesLocked(s, "ir-2",
		telemetry.RouteSnapshot{RouteID: "de", Status: "up", LatencyMS: 50, ProbeKind: "tcp"},
		telemetry.RouteSnapshot{RouteID: "tr", Status: "up", LatencyMS: 50, ProbeKind: "tcp"})
	s.mu.Unlock()

	at := now.Add(10 * time.Second)
	if _, err := s.EvaluateDistributions(at, DefaultDistributionPolicy()); err != nil {
		t.Fatal(err)
	}

	s.mu.Lock()
	setIngressRoutesLocked(s, "ir-1",
		telemetry.RouteSnapshot{RouteID: "de", Status: "up", LatencyMS: 10, ProbeKind: "tcp"},
		telemetry.RouteSnapshot{RouteID: "tr", Status: "down", ProbeKind: "tcp"})
	setIngressLayerHealthLocked(s, "ir-1", HealthDown)
	s.mu.Unlock()

	if _, err := s.EvaluateDistributions(at.Add(time.Second), DefaultDistributionPolicy()); err != nil {
		t.Fatal(err)
	}
	got := s.DistributionSnapshot(at.Add(time.Second))
	byRoute := map[string]DistributionView{}
	for _, d := range got {
		byRoute[d.RouteID] = d
	}
	if byRoute["de"].Weights["ir-1"] == 0 || !byRoute["de"].Usable {
		t.Fatalf("unrelated DE distribution was poisoned: %+v", byRoute["de"])
	}
	if byRoute["tr"].Weights["ir-1"] != 0 || byRoute["tr"].Weights["ir-2"] != 100 || !byRoute["tr"].Usable {
		t.Fatalf("TR did not isolate failed IR: %+v", byRoute["tr"])
	}
}

func TestDistributionHoldDownAndMaterialThresholdPreventChurn(t *testing.T) {
	now := time.Unix(15000, 0).UTC()
	s := ingressStore(t, now)
	at := now.Add(10 * time.Second)
	p := DefaultDistributionPolicy()
	if _, err := s.EvaluateDistributions(at, p); err != nil {
		t.Fatal(err)
	}
	first := oneDistribution(t, s, at)

	s.mu.Lock()
	setIngressRoutesLocked(s, "ir-1", telemetry.RouteSnapshot{RouteID: "de", Status: "up", LatencyMS: 22, ProbeKind: "tcp"})
	s.mu.Unlock()
	if _, err := s.EvaluateDistributions(at.Add(5*time.Second), p); err != nil {
		t.Fatal(err)
	}
	second := oneDistribution(t, s, at.Add(5*time.Second))
	if second.Generation != first.Generation || !sameWeights(second.Weights, first.Weights) {
		t.Fatalf("hold-down churned weights: first=%+v second=%+v", first, second)
	}
}

func TestDistributionSingleEligibleIRGets100(t *testing.T) {
	now := time.Unix(16000, 0).UTC()
	s := ingressStore(t, now)
	at := now.Add(10 * time.Second)
	s.mu.Lock()
	setIngressRoutesLocked(s, "ir-1")
	s.mu.Unlock()
	if _, err := s.EvaluateDistributions(at, DefaultDistributionPolicy()); err != nil {
		t.Fatal(err)
	}
	got := oneDistribution(t, s, at)
	if got.Weights["ir-2"] != 100 || got.Weights["ir-1"] != 0 || !got.Usable {
		t.Fatalf("single eligible result: %+v", got)
	}
}

func TestDistributionPersistsAcrossRestart(t *testing.T) {
	now := time.Unix(17000, 0).UTC()
	s := ingressStore(t, now)
	at := now.Add(10 * time.Second)
	if _, err := s.EvaluateDistributions(at, DefaultDistributionPolicy()); err != nil {
		t.Fatal(err)
	}
	first := oneDistribution(t, s, at)

	s2, err := OpenStore(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	after := oneDistribution(t, s2, at.Add(time.Second))
	if after.Generation != first.Generation || !sameWeights(after.Weights, first.Weights) || after.EXNode != first.EXNode {
		t.Fatalf("distribution lost on restart: before=%+v after=%+v", first, after)
	}
}

func TestDistributionAPIReadAndEvaluate(t *testing.T) {
	now := time.Unix(18000, 0).UTC()
	s := ingressStore(t, now)
	app, err := NewServer(s, "admin")
	if err != nil {
		t.Fatal(err)
	}
	app.now = func() time.Time { return now.Add(10 * time.Second) }

	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, authReq(http.MethodPost, "/api/ingress/distribution/evaluate", "admin", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("evaluate status=%d body=%s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, authReq(http.MethodGet, "/api/ingress/distribution", "admin", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestDistributionSQLiteMigrationPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.st.IngressDistributions["de"] = IngressDistribution{
		RouteID: "de", EXNode: "ex-1", State: DistributionReady,
		Weights: map[string]int{"ir-1": 60, "ir-2": 40}, Generation: 4,
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
	got := s2.st.IngressDistributions["de"]
	s2.mu.Unlock()
	if got.Generation != 4 || got.Weights["ir-1"] != 60 || got.Weights["ir-2"] != 40 {
		t.Fatalf("persisted distribution mismatch: %+v", got)
	}
	if v, err := stateSchemaVersion(path); err != nil || v != len(stateMigrations) {
		t.Fatalf("schema version=%d err=%v want=%d", v, err, len(stateMigrations))
	}
}


func TestDistributionTrafficLoadUsesEWMA(t *testing.T) {
	now := time.Unix(19000, 0).UTC()
	s := ingressStore(t, now)
	s.mu.Lock()
	s.st.History["ir-1"] = []HistoryPoint{
		{Timestamp: now.Add(-3 * time.Minute), IngressBytes: 0},
		{Timestamp: now.Add(-2 * time.Minute), IngressBytes: 1_000_000},
		{Timestamp: now.Add(-time.Minute), IngressBytes: 2_000_000},
		{Timestamp: now, IngressBytes: 102_000_000},
	}
	got := s.recentTrafficKbpsLocked("ir-1", now, 5*time.Minute)
	s.mu.Unlock()
	if got <= 133 || got >= 13_333 {
		t.Fatalf("EWMA traffic load=%d kbps; want > baseline and < latest spike", got)
	}
}

func TestDistributionTrafficEWMAResetsAcrossCounterRollback(t *testing.T) {
	now := time.Unix(20000, 0).UTC()
	s := ingressStore(t, now)
	s.mu.Lock()
	s.st.History["ir-1"] = []HistoryPoint{
		{Timestamp: now.Add(-3 * time.Minute), IngressBytes: 50_000_000},
		{Timestamp: now.Add(-2 * time.Minute), IngressBytes: 60_000_000},
		{Timestamp: now.Add(-time.Minute), IngressBytes: 1_000_000},
		{Timestamp: now, IngressBytes: 2_000_000},
	}
	got := s.recentTrafficKbpsLocked("ir-1", now, 5*time.Minute)
	s.mu.Unlock()
	if got <= 0 || got > 2_000 {
		t.Fatalf("counter reset produced implausible smoothed load: %d kbps", got)
	}
}
