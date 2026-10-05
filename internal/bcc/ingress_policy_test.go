package bcc

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func ingressStore(t *testing.T, now time.Time) *Store {
	t.Helper()
	s := desiredTopologyStore(t)
	spec := TopologySpec{
		IRMembers: []IRPoolMember{
			{NodeID: "ir-1", Enabled: true},
			{NodeID: "ir-2", Enabled: true},
		},
		EXRoutes: []ExplicitEXRoute{
			{ID: "de", EXNode: "ex-1", Country: "DE", Enabled: true, Target: "127.0.0.1:28443"},
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
	setIngressNodeHealthyLocked(s, "ir-1", now.Add(2*time.Second), 20, 8, 0)
	setIngressNodeHealthyLocked(s, "ir-2", now.Add(2*time.Second), 80, 15, 0)
	setIngressNodeHealthyLocked(s, "ex-1", now.Add(2*time.Second), 50, 10, 0)
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	s.mu.Unlock()
	return s
}

func setIngressNodeHealthyLocked(s *Store, id string, now time.Time, latency, noise, errRate int64) {
	if s.st.Health == nil {
		s.st.Health = map[string]NodeHealthRecord{}
	}
	n := s.st.Nodes[id]
	n.AgentSeen = now
	n.LastChecked = now
	n.Health = "up"
	n.LatencyMS = latency
	s.st.Nodes[id] = n
	s.st.Telemetry[id] = TelemetryCursor{
		NodeID: id, LastTelemetry: now, NoiseLatencyMS: noise,
		HandshakeErrorRateMilliMin: errRate, ActiveSessions: 1,
	}
	s.st.Health[id] = NodeHealthRecord{Overall: HealthUp}
}

func setIngressHealthLocked(s *Store, id, health string) {
	r := s.st.Health[id]
	r.Overall = health
	s.st.Health[id] = r
}

func oneIngress(t *testing.T, s *Store, now time.Time) IngressDecision {
	t.Helper()
	got := s.IngressSnapshot(now)
	if len(got) != 1 {
		t.Fatalf("decisions=%d, want 1: %+v", len(got), got)
	}
	return got[0]
}

func TestIngressInitialSelectionFailoverAndStickyRecovery(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	s := ingressStore(t, now)
	at := now.Add(10 * time.Second)

	events, err := s.EvaluateIngress(at, DefaultIngressPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events=%d, want 1", len(events))
	}
	first := oneIngress(t, s, at)
	if first.State != IngressReady || first.ActiveIR != "ir-1" || first.Generation != 1 {
		t.Fatalf("initial decision: %+v", first)
	}
	if len(first.StandbyIRs) != 1 || first.StandbyIRs[0] != "ir-2" {
		t.Fatalf("standby order: %+v", first.StandbyIRs)
	}

	s.mu.Lock()
	setIngressHealthLocked(s, "ir-1", HealthDown)
	s.mu.Unlock()
	events, err = s.EvaluateIngress(at.Add(time.Second), DefaultIngressPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].FromIR != "ir-1" || events[0].ToIR != "ir-2" {
		t.Fatalf("failover event: %+v", events)
	}
	failedOver := oneIngress(t, s, at.Add(time.Second))
	if failedOver.ActiveIR != "ir-2" || failedOver.Generation != 2 {
		t.Fatalf("failover decision: %+v", failedOver)
	}

	// ir-1 comes back with a much better score. M-015 must not flap back while
	// the current ACTIVE ir-2 remains eligible.
	s.mu.Lock()
	setIngressNodeHealthyLocked(s, "ir-1", at.Add(2*time.Second), 1, 1, 0)
	s.mu.Unlock()
	events, err = s.EvaluateIngress(at.Add(2*time.Second), DefaultIngressPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("healthy recovery changed sticky ACTIVE: %+v", events)
	}
	recovered := oneIngress(t, s, at.Add(2*time.Second))
	if recovered.ActiveIR != "ir-2" || recovered.Generation != 2 {
		t.Fatalf("unexpected failback: %+v", recovered)
	}
}

func TestIngressEXFailureNeverSubstitutesAnotherEX(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	s := ingressStore(t, now)
	at := now.Add(10 * time.Second)
	if _, err := s.EvaluateIngress(at, DefaultIngressPolicy()); err != nil {
		t.Fatal(err)
	}

	s.mu.Lock()
	setIngressHealthLocked(s, "ex-1", HealthDown)
	s.mu.Unlock()
	events, err := s.EvaluateIngress(at.Add(time.Second), DefaultIngressPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ToState != IngressEXUnavailable {
		t.Fatalf("EX failure event: %+v", events)
	}
	got := oneIngress(t, s, at.Add(time.Second))
	if got.State != IngressEXUnavailable || got.ActiveIR != "" || got.EXNode != "ex-1" {
		t.Fatalf("EX failure changed explicit egress contract: %+v", got)
	}
}

func TestIngressStaleTelemetryCannotWin(t *testing.T) {
	now := time.Unix(3000, 0).UTC()
	s := ingressStore(t, now)
	at := now.Add(10 * time.Second)
	s.mu.Lock()
	cur := s.st.Telemetry["ir-1"]
	cur.LastTelemetry = at.Add(-10 * time.Minute)
	s.st.Telemetry["ir-1"] = cur
	s.mu.Unlock()

	if _, err := s.EvaluateIngress(at, DefaultIngressPolicy()); err != nil {
		t.Fatal(err)
	}
	got := oneIngress(t, s, at)
	if got.ActiveIR != "ir-2" {
		t.Fatalf("stale higher-scoring IR won: %+v", got)
	}
	var stale *IngressCandidate
	for i := range got.Candidates {
		if got.Candidates[i].IRNode == "ir-1" {
			stale = &got.Candidates[i]
		}
	}
	if stale == nil || stale.Eligible || !strings.Contains(strings.Join(stale.Reasons, " "), "telemetry") {
		t.Fatalf("stale candidate not rejected: %+v", stale)
	}
}

func TestIngressDriftedEdgeCannotBecomeActive(t *testing.T) {
	now := time.Unix(4000, 0).UTC()
	s := ingressStore(t, now)
	at := now.Add(10 * time.Second)
	s.mu.Lock()
	for id, tun := range s.st.Tunnels {
		if tun.IRNode == "ir-1" {
			tun.Drift = &DriftReport{State: DriftDrifted, CheckedAt: at}
			s.st.Tunnels[id] = tun
		}
	}
	s.mu.Unlock()
	if _, err := s.EvaluateIngress(at, DefaultIngressPolicy()); err != nil {
		t.Fatal(err)
	}
	got := oneIngress(t, s, at)
	if got.ActiveIR != "ir-2" {
		t.Fatalf("drifted IR selected: %+v", got)
	}
}

func TestIngressGenerationIsIdempotentAndPersistsAcrossRestart(t *testing.T) {
	now := time.Unix(5000, 0).UTC()
	s := ingressStore(t, now)
	at := now.Add(10 * time.Second)
	if _, err := s.EvaluateIngress(at, DefaultIngressPolicy()); err != nil {
		t.Fatal(err)
	}
	first := oneIngress(t, s, at)
	if _, err := s.EvaluateIngress(at.Add(time.Second), DefaultIngressPolicy()); err != nil {
		t.Fatal(err)
	}
	second := oneIngress(t, s, at.Add(time.Second))
	if second.Generation != first.Generation || second.ActiveIR != first.ActiveIR {
		t.Fatalf("idempotent evaluation churned decision: first=%+v second=%+v", first, second)
	}

	s2, err := OpenStore(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	after := oneIngress(t, s2, at.Add(2*time.Second))
	if after.Generation != first.Generation || after.ActiveIR != first.ActiveIR || after.EXNode != "ex-1" {
		t.Fatalf("selection lost across restart: before=%+v after=%+v", first, after)
	}
}

func TestIngressNoHealthyIRIsFailClosed(t *testing.T) {
	now := time.Unix(6000, 0).UTC()
	s := ingressStore(t, now)
	at := now.Add(10 * time.Second)
	s.mu.Lock()
	setIngressHealthLocked(s, "ir-1", HealthUnknown)
	setIngressHealthLocked(s, "ir-2", HealthDegraded)
	s.mu.Unlock()
	if _, err := s.EvaluateIngress(at, DefaultIngressPolicy()); err != nil {
		t.Fatal(err)
	}
	got := oneIngress(t, s, at)
	if got.State != IngressNoHealthyIR || got.ActiveIR != "" {
		t.Fatalf("unhealthy IR was selected: %+v", got)
	}
}

func TestIngressAPIReadAndEvaluate(t *testing.T) {
	now := time.Unix(7000, 0).UTC()
	s := ingressStore(t, now)
	app, err := NewServer(s, "admin")
	if err != nil {
		t.Fatal(err)
	}
	app.now = func() time.Time { return now.Add(10 * time.Second) }

	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, authReq(http.MethodGet, "/api/ingress", "admin", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), IngressUnknown) {
		t.Fatalf("GET ingress status=%d body=%s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, authReq(http.MethodPost, "/api/ingress/evaluate", "admin", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "\"active_ir\":\"ir-1\"") {
		t.Fatalf("evaluate ingress status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestIngressPersistenceTableMigratesCleanly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.st.IngressSelections["de"] = IngressSelection{
		RouteID: "de", EXNode: "ex-1", State: IngressReady, ActiveIR: "ir-1",
		Generation: 3, ChangedAt: time.Unix(1, 0).UTC(), EvaluatedAt: time.Unix(2, 0).UTC(),
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
	got := s2.st.IngressSelections["de"]
	s2.mu.Unlock()
	if got.Generation != 3 || got.ActiveIR != "ir-1" || got.EXNode != "ex-1" {
		t.Fatalf("persisted selection mismatch: %+v", got)
	}
	if v, err := stateSchemaVersion(path); err != nil || v != len(stateMigrations) {
		t.Fatalf("schema version=%d err=%v want=%d", v, err, len(stateMigrations))
	}
}


func TestIngressTwoIRFiveExplicitEXProducesFiveIndependentDecisions(t *testing.T) {
	now := time.Unix(8000, 0).UTC()
	s := desiredTopologyStore(t)
	if _, err := s.SetTopologySpec(desiredSpec([]string{"ir-1", "ir-2"}, ""), now); err != nil {
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
		latency := int64(50)
		if id == "ir-1" {
			latency = 15
		}
		if id == "ir-2" {
			latency = 70
		}
		setIngressNodeHealthyLocked(s, id, now.Add(2*time.Second), latency, 5, 0)
	}
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	s.mu.Unlock()

	if _, err := s.EvaluateIngress(now.Add(10*time.Second), DefaultIngressPolicy()); err != nil {
		t.Fatal(err)
	}
	got := s.IngressSnapshot(now.Add(10 * time.Second))
	if len(got) != 5 {
		t.Fatalf("decisions=%d, want 5: %+v", len(got), got)
	}
	for _, d := range got {
		if d.State != IngressReady || d.ActiveIR != "ir-1" || d.EXNode == "" || !d.Usable {
			t.Fatalf("route %s bad decision: %+v", d.RouteID, d)
		}
		if len(d.StandbyIRs) != 1 || d.StandbyIRs[0] != "ir-2" {
			t.Fatalf("route %s standby=%v", d.RouteID, d.StandbyIRs)
		}
	}
}

func TestIngressSnapshotMarksPersistedSelectionUnusableBeforeReevaluation(t *testing.T) {
	now := time.Unix(9000, 0).UTC()
	s := ingressStore(t, now)
	at := now.Add(10 * time.Second)
	if _, err := s.EvaluateIngress(at, DefaultIngressPolicy()); err != nil {
		t.Fatal(err)
	}
	if got := oneIngress(t, s, at); !got.Usable {
		t.Fatalf("fresh selection unexpectedly unusable: %+v", got)
	}

	// Simulate evidence aging before the next periodic evaluator runs.
	staleAt := at.Add(10 * time.Minute)
	got := oneIngress(t, s, staleAt)
	if got.Usable {
		t.Fatalf("stale persisted ACTIVE exposed as usable: %+v", got)
	}
}
