package bcc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/telemetry"
)

var t0 = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

// feed applies samples one every `step` starting at `at` and returns the
// transitions as "FROM>TO" strings plus the time after the last sample.
func feed(rec *LayerRecord, p HealthPolicy, at time.Time, step time.Duration, kinds ...string) ([]string, time.Time) {
	var out []string
	for _, k := range kinds {
		if from, _, moved := observe(rec, Sample{Kind: k, Evidence: k}, at, p); moved {
			out = append(out, from+">"+rec.State)
		}
		at = at.Add(step)
	}
	return out, at
}

func rep(k string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = k
	}
	return out
}

func TestNoFalseUp(t *testing.T) {
	p := DefaultHealthPolicy()
	rec := &LayerRecord{}
	tr, _ := feed(rec, p, t0, 10*time.Second, rep(SampleOK, p.UpAfter-1)...)
	if len(tr) != 0 || rec.State != HealthUnknown {
		t.Fatalf("UP after only %d OK samples: %v %s", p.UpAfter-1, tr, rec.State)
	}
	// One BAD resets the run: the node still has to prove itself from zero.
	tr, at := feed(rec, p, t0.Add(time.Minute), 10*time.Second, SampleBad)
	tr2, _ := feed(rec, p, at, 10*time.Second, rep(SampleOK, p.UpAfter-1)...)
	if len(tr)+len(tr2) != 0 || rec.State != HealthUnknown {
		t.Fatalf("a BAD sample did not reset the OK run: %v %v %s", tr, tr2, rec.State)
	}
	tr, _ = feed(rec, p, t0.Add(10*time.Minute), 10*time.Second, rep(SampleOK, p.UpAfter)...)
	if !reflect.DeepEqual(tr, []string{"UNKNOWN>UP"}) {
		t.Fatalf("sustained OK: %v", tr)
	}
}

func TestOneBlipAndFlappingNeverLeaveUp(t *testing.T) {
	p := DefaultHealthPolicy()
	rec := &LayerRecord{State: HealthUp, Since: t0}
	// A single BAD between OKs, and strict alternation, are noise.
	seq := []string{SampleBad, SampleOK, SampleBad, SampleOK, SampleBad, SampleOK, SampleBad, SampleOK}
	if tr, _ := feed(rec, p, t0, 10*time.Second, seq...); len(tr) != 0 || rec.State != HealthUp {
		t.Fatalf("flapping moved the state: %v %s", tr, rec.State)
	}
	tr, at := feed(rec, p, t0.Add(time.Hour), 10*time.Second, SampleBad, SampleBad)
	if !reflect.DeepEqual(tr, []string{"UP>DEGRADED"}) {
		t.Fatalf("two consecutive BAD: %v", tr)
	}
	// Recovery from DEGRADED needs RecoverAfter consecutive OK; one OK is not enough.
	if tr, at = feed(rec, p, at, 10*time.Second, SampleOK, SampleOK); len(tr) != 0 {
		t.Fatalf("recovered too early: %v", tr)
	}
	if tr, _ = feed(rec, p, at, 10*time.Second, SampleOK); !reflect.DeepEqual(tr, []string{"DEGRADED>UP"}) {
		t.Fatalf("recovery: %v", tr)
	}
}

func TestDownNeedsCountAndTimeRecoveryNeedsCountAndTime(t *testing.T) {
	p := DefaultHealthPolicy()
	rec := &LayerRecord{State: HealthUp, Since: t0}
	// Many BAD samples in a short burst: degraded, but not DOWN before MinDown.
	tr, at := feed(rec, p, t0, time.Second, rep(SampleBad, 20)...)
	if !reflect.DeepEqual(tr, []string{"UP>DEGRADED"}) {
		t.Fatalf("burst: %v (state %s)", tr, rec.State)
	}
	// The same run continues past MinDown and reaches DOWN.
	if tr, at = feed(rec, p, at, 5*time.Second, rep(SampleBad, 3)...); !reflect.DeepEqual(tr, []string{"DEGRADED>DOWN"}) {
		t.Fatalf("down: %v", tr)
	}
	// First OK: RECOVERING, not UP.
	if tr, at = feed(rec, p, at, time.Second, SampleOK); !reflect.DeepEqual(tr, []string{"DOWN>RECOVERING"}) {
		t.Fatalf("first OK: %v", tr)
	}
	// UpAfter OK samples inside MinRecover are still not enough.
	if tr, at = feed(rec, p, at, time.Second, rep(SampleOK, 8)...); len(tr) != 0 || rec.State != HealthRecovering {
		t.Fatalf("UP before MinRecover: %v %s", tr, rec.State)
	}
	// A single BAD while recovering restarts the count but does not drop to DOWN.
	if tr, at = feed(rec, p, at, time.Second, SampleBad, SampleOK); len(tr) != 0 || rec.State != HealthRecovering || rec.OKRun != 1 {
		t.Fatalf("one BAD while recovering: %v %s okrun=%d", tr, rec.State, rec.OKRun)
	}
	// Two consecutive BAD do.
	if tr, at = feed(rec, p, at, time.Second, SampleBad, SampleBad); !reflect.DeepEqual(tr, []string{"RECOVERING>DOWN"}) {
		t.Fatalf("relapse: %v", tr)
	}
	if _, at = feed(rec, p, at, time.Second, SampleOK); rec.State != HealthRecovering {
		t.Fatal("no recovery after relapse")
	}
	if tr, _ = feed(rec, p, at, 10*time.Second, rep(SampleOK, 5)...); !reflect.DeepEqual(tr, []string{"RECOVERING>UP"}) {
		t.Fatalf("sustained recovery: %v", tr)
	}
}

func TestSilenceIsUnknownNeverDownAndNeverUp(t *testing.T) {
	p := DefaultHealthPolicy()
	for _, start := range []string{HealthUp, HealthDegraded, HealthDown, HealthRecovering} {
		rec := &LayerRecord{State: start, Since: t0}
		tr, _ := feed(rec, p, t0, 10*time.Second, rep(SampleNone, 40)...)
		if rec.State != HealthUnknown {
			t.Fatalf("from %s silence ended in %s (%v)", start, rec.State, tr)
		}
		for _, x := range tr {
			if x == "UP>DOWN" || x == "DEGRADED>DOWN" || x[len(x)-2:] == "UP" {
				t.Fatalf("from %s silence produced %s", start, x)
			}
		}
	}
	// NONE between BADs breaks the run: silence is not failure evidence.
	rec := &LayerRecord{State: HealthDegraded, Since: t0}
	seq := []string{SampleBad, SampleBad, SampleNone, SampleBad, SampleBad, SampleNone, SampleBad, SampleBad}
	if tr, _ := feed(rec, p, t0, 10*time.Second, seq...); len(tr) != 0 || rec.State != HealthDegraded {
		t.Fatalf("interrupted BAD runs reached DOWN: %v %s", tr, rec.State)
	}
	// And NONE never completes a recovery.
	rec = &LayerRecord{State: HealthRecovering, Since: t0}
	if tr, _ := feed(rec, p, t0, 10*time.Second, SampleOK, SampleOK, SampleNone, SampleOK, SampleOK); len(tr) != 0 {
		t.Fatalf("recovery completed across silence: %v", tr)
	}
}

func TestGapWhileBCCWasNotWatchingResetsTheRuns(t *testing.T) {
	p := DefaultHealthPolicy()
	rec := &LayerRecord{State: HealthDegraded, Since: t0}
	_, at := feed(rec, p, t0, 5*time.Second, rep(SampleBad, 4)...)
	// BCC was down for an hour. One BAD sample afterwards must not complete the
	// old run into DOWN.
	if tr, _ := feed(rec, p, at.Add(time.Hour), time.Second, SampleBad); len(tr) != 0 || rec.BadRun != 1 {
		t.Fatalf("a gap did not reset the run: %v badrun=%d", tr, rec.BadRun)
	}
}

func TestModelIsDeterministic(t *testing.T) {
	p := DefaultHealthPolicy()
	seq := append(append(rep(SampleOK, 6), rep(SampleBad, 9)...), append(rep(SampleOK, 7), rep(SampleNone, 30)...)...)
	run := func() ([]string, LayerRecord) {
		rec := &LayerRecord{}
		tr, _ := feed(rec, p, t0, 10*time.Second, seq...)
		return tr, *rec
	}
	a, ra := run()
	b, rb := run()
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(ra, rb) {
		t.Fatalf("same input, different result: %v / %v", a, b)
	}
	if len(a) == 0 {
		t.Fatal("scenario produced no transitions")
	}
}

func TestOverallHealthKeepsLayersApart(t *testing.T) {
	mk := func(m map[string]string) map[string]*LayerRecord {
		out := map[string]*LayerRecord{}
		for k, v := range m {
			out[k] = &LayerRecord{State: v}
		}
		return out
	}
	cases := []struct {
		name   string
		layers map[string]string
		want   string
	}{
		{"nothing known", map[string]string{}, HealthUnknown},
		{"all core up", map[string]string{LayerL0: HealthUp, LayerL1: HealthUp}, HealthUp},
		{"reachable but never reported: not proven", map[string]string{LayerL1: HealthUp}, HealthUnknown},
		{"healthy sessions cannot make a down process look up", map[string]string{LayerL0: HealthDown, LayerL1: HealthUp, LayerL2: HealthUp}, HealthDown},
		{"a down probe cannot be hidden by telemetry", map[string]string{LayerL0: HealthUp, LayerL1: HealthDown}, HealthDown},
		{"route trouble degrades, does not kill", map[string]string{LayerL0: HealthUp, LayerL1: HealthUp, LayerL4: HealthDown}, HealthDegraded},
		{"agent lost degrades only", map[string]string{LayerL0: HealthUp, LayerL1: HealthUp, LayerAgent: HealthDown}, HealthDegraded},
		{"recovering core", map[string]string{LayerL0: HealthUp, LayerL1: HealthRecovering}, HealthRecovering},
		{"unknown peer layer does not block up", map[string]string{LayerL0: HealthUp, LayerL1: HealthUp, LayerL2: HealthUnknown}, HealthUp},
		{"degraded layer blocks up", map[string]string{LayerL0: HealthUp, LayerL1: HealthUp, LayerL2: HealthDegraded}, HealthDegraded},
	}
	for _, c := range cases {
		if got, _ := overallHealth(mk(c.layers)); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

// ---- engine ----

func healthStore(t *testing.T, path string) *Store {
	t.Helper()
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func setNode(t *testing.T, s *Store, id string, mutate func(*Node)) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.st.Nodes[id]
	n.ID, n.Alias, n.Address, n.Role = id, id, "127.0.0.1:1", "foreign"
	if n.Health == "" {
		n.Health = "unknown"
	}
	mutate(&n)
	s.st.Nodes[id] = n
}

func setTelemetry(s *Store, id string, at time.Time, sessions uint64, routes ...telemetry.RouteSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Telemetry[id] = TelemetryCursor{NodeID: id, LastTelemetry: at, ActiveSessions: sessions, Routes: routes}
}

func layerOf(s *Store, id, layer string) string {
	for _, v := range s.HealthSnapshot(id, 0) {
		for _, l := range v.Layers {
			if l.Layer == layer {
				return l.State
			}
		}
	}
	return ""
}

func TestEngineSamplesRealSignalsAndKeepsLayersIndependent(t *testing.T) {
	s := healthStore(t, filepath.Join(t.TempDir(), "state.json"))
	c := DefaultHealthConfig()
	now := t0
	for i := 0; i < 8; i++ {
		setNode(t, s, "n1", func(n *Node) { n.Health, n.LastChecked, n.LatencyMS = "up", now, 3 })
		setTelemetry(s, "n1", now, 2, telemetry.RouteSnapshot{RouteID: "r1", Status: "up"})
		if _, err := s.EvaluateHealth(now, c); err != nil {
			t.Fatal(err)
		}
		now = now.Add(10 * time.Second)
	}
	v := s.HealthSnapshot("n1", 0)[0]
	if v.Overall != HealthUp {
		t.Fatalf("overall %s: %+v", v.Overall, v)
	}
	states := map[string]string{}
	for _, l := range v.Layers {
		states[l.Layer] = l.State
	}
	for _, id := range []string{LayerL3, LayerL5, LayerL6} {
		if states[id] != HealthNotAssessed {
			t.Errorf("%s = %s, want NOT_ASSESSED", id, states[id])
		}
	}
	if states[LayerAgent] != HealthUnknown {
		t.Errorf("agent never polled but layer is %s", states[LayerAgent])
	}
	// The route probe goes down: only L4 (and so the node) degrades.
	for i := 0; i < 3; i++ {
		setNode(t, s, "n1", func(n *Node) { n.Health, n.LastChecked = "up", now })
		setTelemetry(s, "n1", now, 2, telemetry.RouteSnapshot{RouteID: "r1", Status: "down"})
		s.EvaluateHealth(now, c)
		now = now.Add(10 * time.Second)
	}
	if layerOf(s, "n1", LayerL4) != HealthDegraded || layerOf(s, "n1", LayerL0) != HealthUp || layerOf(s, "n1", LayerL1) != HealthUp {
		t.Fatalf("layers leaked into each other: L4=%s L0=%s L1=%s", layerOf(s, "n1", LayerL4), layerOf(s, "n1", LayerL0), layerOf(s, "n1", LayerL1))
	}
	if v := s.HealthSnapshot("n1", 0)[0]; v.Overall != HealthDegraded || len(v.Transitions) == 0 {
		t.Fatalf("overall %s, transitions %d", v.Overall, len(v.Transitions))
	}
}

func TestEngineIsRestartSafe(t *testing.T) {
	// The same samples through one uninterrupted store and through a store that
	// is closed and reopened in the middle must give identical history.
	run := func(restartAt int) []LayerTransition {
		path := filepath.Join(t.TempDir(), "state.json")
		s := healthStore(t, path)
		c := DefaultHealthConfig()
		now := t0
		for i := 0; i < 30; i++ {
			if i == restartAt {
				s = healthStore(t, path)
			}
			bad := i >= 6 && i < 22
			setNode(t, s, "n1", func(n *Node) {
				n.Health, n.LastChecked = "up", now
				if bad {
					n.Health = "down"
				}
			})
			setTelemetry(s, "n1", now, 1)
			if _, err := s.EvaluateHealth(now, c); err != nil {
				t.Fatal(err)
			}
			now = now.Add(10 * time.Second)
		}
		return s.HealthSnapshot("n1", 0)[0].Transitions
	}
	control := run(-1)
	if len(control) < 3 {
		t.Fatalf("scenario too weak: %+v", control)
	}
	for _, at := range []int{3, 8, 14, 23} {
		if got := run(at); !reflect.DeepEqual(got, control) {
			t.Fatalf("restart at sample %d changed the history\n got %+v\nwant %+v", at, got, control)
		}
	}
}

func TestEngineDoesNotInventUpOrDownAfterABCCOutage(t *testing.T) {
	s := healthStore(t, filepath.Join(t.TempDir(), "state.json"))
	c := DefaultHealthConfig()
	now := t0
	for i := 0; i < 8; i++ {
		setNode(t, s, "n1", func(n *Node) { n.Health, n.LastChecked = "up", now })
		setTelemetry(s, "n1", now, 1)
		s.EvaluateHealth(now, c)
		now = now.Add(10 * time.Second)
	}
	// BCC is down for six hours. The first sample afterwards finds the node
	// unreachable and telemetry stale; that is one observation, not a verdict.
	now = now.Add(6 * time.Hour)
	setNode(t, s, "n1", func(n *Node) { n.Health, n.LastChecked = "down", now })
	s.EvaluateHealth(now, c)
	if l := layerOf(s, "n1", LayerL1); l != HealthUp {
		t.Fatalf("one sample after an outage moved L1 to %s", l)
	}
}

func TestEngineAuditsEveryTransitionAndServesHistory(t *testing.T) {
	dir := t.TempDir()
	store := healthStore(t, filepath.Join(dir, "state.json"))
	app, err := NewServer(store, "admin")
	if err != nil {
		t.Fatal(err)
	}
	now := t0
	for i := 0; i < 8; i++ {
		setNode(t, store, "n1", func(n *Node) { n.Health, n.LastChecked = "up", now })
		setTelemetry(store, "n1", now, 1)
		app.evaluateHealthAt(now)
		now = now.Add(10 * time.Second)
	}
	for i := 0; i < 10; i++ {
		setNode(t, store, "n1", func(n *Node) { n.Health, n.LastChecked = "down", now })
		setTelemetry(store, "n1", now, 1)
		app.evaluateHealthAt(now)
		now = now.Add(10 * time.Second)
	}
	entries, err := app.audit.List(0)
	if err != nil {
		t.Fatal(err)
	}
	var transitions []AuditEntry
	for _, e := range entries {
		if e.Action == "health.transition" {
			transitions = append(transitions, e)
		}
	}
	hist := store.HealthSnapshot("n1", 0)[0].Transitions
	if len(transitions) != len(hist) || len(hist) < 3 {
		t.Fatalf("%d audit entries for %d transitions", len(transitions), len(hist))
	}
	last := transitions[len(transitions)-1]
	if last.Target != "n1/L1" || last.Details["to"] != HealthDown || last.Details["reason"] == "" || last.Details["evidence"] == "" || last.Outcome != "failure" {
		t.Fatalf("audit entry %+v", last)
	}
	if err := app.audit.Verify(); err != nil {
		t.Fatal(err)
	}

	// The API shows layers, NOT_ASSESSED layers and the history; it is admin-only and read-only.
	get := func(path, token string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		app.Handler().ServeHTTP(rr, authReq(http.MethodGet, path, token, nil))
		return rr
	}
	if rr := get("/api/health", "wrong"); rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d", rr.Code)
	}
	rr := get("/api/health?node=n1&all=1", "admin")
	var out struct {
		Nodes []HealthView `json:"nodes"`
	}
	if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &out) != nil || len(out.Nodes) != 1 {
		t.Fatalf("health api: %d %s", rr.Code, rr.Body.String())
	}
	v := out.Nodes[0]
	if v.Overall != HealthDown || len(v.Transitions) != len(hist) || len(v.Layers) != 8 {
		t.Fatalf("view %+v", v)
	}
	if rr := httptest.NewRecorder(); true {
		app.Handler().ServeHTTP(rr, authReq(http.MethodPost, "/api/health", "admin", map[string]any{}))
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("POST /api/health: %d", rr.Code)
		}
	}
}

func TestEngineIsRaceSafe(t *testing.T) {
	s := healthStore(t, filepath.Join(t.TempDir(), "state.json"))
	c := DefaultHealthConfig()
	setNode(t, s, "n1", func(n *Node) {})
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				now := t0.Add(time.Duration(i) * 10 * time.Second)
				switch g % 3 {
				case 0:
					s.EvaluateHealth(now, c)
				case 1:
					s.HealthSnapshot("", 5)
				default:
					s.SetHealth("n1", "up", 2, now)
				}
			}
		}(g)
	}
	wg.Wait()
}
