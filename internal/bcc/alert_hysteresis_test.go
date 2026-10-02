package bcc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/telemetry"
)

// A rig drives probes, telemetry, the health sampler and the alert engine with
// a controlled clock: one tick = one 10 s sample.
type alertRig struct {
	t       *testing.T
	path    string
	store   *Store
	app     *Server
	hook    *httptest.Server
	mu      sync.Mutex
	got     []Alert
	failing atomic.Bool
	now     time.Time
}

func newAlertRig(t *testing.T) *alertRig {
	t.Helper()
	r := &alertRig{t: t, path: filepath.Join(t.TempDir(), "state.json"), now: t0}
	r.hook = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		defer req.Body.Close()
		if r.failing.Load() {
			http.Error(w, "down", 500)
			return
		}
		var a Alert
		if err := json.NewDecoder(req.Body).Decode(&a); err != nil {
			http.Error(w, "bad json", 400)
			return
		}
		r.mu.Lock()
		r.got = append(r.got, a)
		r.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(r.hook.Close)
	r.open()
	return r
}

// open (re)opens the store and server on the same state file: a BCC restart.
func (r *alertRig) open() {
	r.t.Helper()
	store, err := OpenStore(r.path)
	if err != nil {
		r.t.Fatal(err)
	}
	app, err := NewServer(store, "admin")
	if err != nil {
		r.t.Fatal(err)
	}
	if err := app.ConfigureAlerts(AlertConfig{WebhookURL: r.hook.URL, TelemetryStaleAfter: 3 * time.Minute, HandshakeErrorRateMilliPerMin: 5000, Interval: time.Second}); err != nil {
		r.t.Fatal(err)
	}
	r.store, r.app = store, app
}

type tickSpec struct {
	routeDown bool
	handshake int64 // milli-errors/min
	silent    bool  // no new telemetry this tick
	noTelem   bool  // the node never reports
	probeDown bool
	// staleProbe: the probe result is not refreshed (no new evidence for L1).
	staleProbe bool
}

func (r *alertRig) tick(node string, s tickSpec) {
	r.t.Helper()
	setNode(r.t, r.store, node, func(n *Node) {
		if s.staleProbe {
			return
		}
		n.Health, n.LastChecked, n.LatencyMS = "up", r.now, 3
		if s.probeDown {
			n.Health = "down"
		}
	})
	if !s.silent && !s.noTelem {
		st := "up"
		if s.routeDown {
			st = "down"
		}
		r.store.mu.Lock()
		r.store.st.Telemetry[node] = TelemetryCursor{
			NodeID: node, LastTelemetry: r.now, ActiveSessions: 1, HandshakeErrorRateMilliMin: s.handshake,
			Routes: []telemetry.RouteSnapshot{{RouteID: "r1", Status: st, ProbeKind: "tcp"}},
		}
		r.store.mu.Unlock()
	}
	r.app.evaluateHealthAt(r.now)
	if err := r.app.evaluateAlertsAt(context.Background(), r.now); err != nil {
		r.t.Fatalf("alert evaluation: %v", err)
	}
	r.now = r.now.Add(10 * time.Second)
}

func (r *alertRig) ticks(n int, node string, s tickSpec) {
	for i := 0; i < n; i++ {
		r.tick(node, s)
	}
}

func (r *alertRig) events() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, a := range r.got {
		out = append(out, fmt.Sprintf("%s/%s/%s", a.Type, a.Status, a.Severity))
	}
	return out
}

func (r *alertRig) auditActions() []string {
	entries, err := r.app.audit.List(0)
	if err != nil {
		r.t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Action, "alert.") {
			out = append(out, e.Action+":"+fmt.Sprint(e.Details["type"])+":"+fmt.Sprint(e.Details["severity"]))
		}
	}
	return out
}

var warm = tickSpec{}
var routeDown = tickSpec{routeDown: true}

func TestOneBadSampleDoesNotAlert(t *testing.T) {
	r := newAlertRig(t)
	r.ticks(8, "n1", warm)
	r.tick("n1", routeDown)
	r.ticks(8, "n1", warm)
	r.tick("n1", tickSpec{handshake: 60000})
	r.ticks(8, "n1", warm)
	if ev := r.events(); len(ev) != 0 {
		t.Fatalf("single bad samples alerted: %v", ev)
	}
}

func TestFlappingDoesNotCauseAnAlertStorm(t *testing.T) {
	r := newAlertRig(t)
	r.ticks(8, "n1", warm)
	for i := 0; i < 60; i++ {
		r.tick("n1", tickSpec{routeDown: i%2 == 0, handshake: int64(i%2) * 60000})
	}
	if ev := r.events(); len(ev) != 0 {
		t.Fatalf("flapping produced %d alerts: %v", len(ev), ev)
	}
}

func TestSustainedFailureAlertsOnceEscalatesOnceAndResolvesOnce(t *testing.T) {
	r := newAlertRig(t)
	r.ticks(8, "n1", warm)
	r.tick("n1", routeDown)
	if len(r.events()) != 0 {
		t.Fatal("first bad sample alerted")
	}
	r.tick("n1", routeDown) // second consecutive BAD: confirmed DEGRADED
	if !reflect.DeepEqual(r.events(), []string{"route_down/firing/warning"}) {
		t.Fatalf("after confirmed DEGRADED: %v", r.events())
	}
	r.ticks(2, "n1", routeDown)
	if len(r.events()) != 1 {
		t.Fatalf("a repeat alert while still DEGRADED: %v", r.events())
	}
	r.ticks(1, "n1", routeDown) // fifth BAD, 40 s: confirmed DOWN
	r.ticks(20, "n1", routeDown)
	if !reflect.DeepEqual(r.events(), []string{"route_down/firing/warning", "route_down/firing/critical"}) {
		t.Fatalf("after confirmed DOWN: %v", r.events())
	}
	// The first OK sample is RECOVERING, not recovered: the alert stays open.
	r.tick("n1", warm)
	r.ticks(3, "n1", warm)
	if len(r.events()) != 2 {
		t.Fatalf("resolved before the recovery was sustained: %v", r.events())
	}
	r.ticks(3, "n1", warm)
	want := []string{"route_down/firing/warning", "route_down/firing/critical", "route_down/resolved/critical"}
	if !reflect.DeepEqual(r.events(), want) {
		t.Fatalf("after sustained recovery: %v", r.events())
	}
	r.ticks(20, "n1", warm)
	if len(r.events()) != 3 {
		t.Fatalf("extra events after resolution: %v", r.events())
	}
	// Every transition is in the audit log, in order, with its evidence.
	if got := r.auditActions(); !reflect.DeepEqual(got, []string{"alert.firing:route_down:warning", "alert.escalated:route_down:critical", "alert.resolved:route_down:critical"}) {
		t.Fatalf("audit: %v", got)
	}
	entries, _ := r.app.audit.List(0)
	for _, e := range entries {
		if e.Action == "alert.firing" && !strings.Contains(fmt.Sprint(e.Details["evidence"]), "route probe down: r1") {
			t.Fatalf("raw signal missing as evidence: %v", e.Details)
		}
	}
	if err := r.app.audit.Verify(); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	first := r.got[0]
	r.mu.Unlock()
	if first.Health != HealthDegraded || first.Evidence == "" || first.RouteID != "r1" {
		t.Fatalf("alert lacks its health state and evidence: %+v", first)
	}
}

func TestDegradedRecoveryClosesTheAlertWithoutEverBeingDown(t *testing.T) {
	r := newAlertRig(t)
	r.ticks(8, "n1", warm)
	r.ticks(3, "n1", routeDown)
	r.ticks(2, "n1", warm)
	if len(r.events()) != 1 {
		t.Fatalf("closed too early: %v", r.events())
	}
	r.tick("n1", warm)
	if !reflect.DeepEqual(r.events(), []string{"route_down/firing/warning", "route_down/resolved/warning"}) {
		t.Fatalf("events: %v", r.events())
	}
}

func TestAlertsAreDeterministicAndRestartSafe(t *testing.T) {
	scenario := func(restartAt int) ([]string, []string) {
		r := newAlertRig(t)
		for i := 0; i < 70; i++ {
			if i == restartAt {
				r.open()
			}
			switch {
			case i < 8:
				r.tick("n1", warm)
			case i < 30:
				r.tick("n1", routeDown)
			default:
				r.tick("n1", warm)
			}
		}
		return r.events(), r.auditActions()
	}
	control, controlAudit := scenario(-1)
	if len(control) != 3 {
		t.Fatalf("control scenario: %v", control)
	}
	if again, _ := scenario(-1); !reflect.DeepEqual(again, control) {
		t.Fatalf("not deterministic: %v vs %v", again, control)
	}
	// A restart at any point (before, between and after each transition) must
	// neither duplicate nor drop an alert or an audit entry.
	for _, at := range []int{3, 9, 10, 12, 14, 20, 29, 31, 33, 36, 50} {
		ev, au := scenario(at)
		if !reflect.DeepEqual(ev, control) || !reflect.DeepEqual(au, controlAudit) {
			t.Fatalf("restart at tick %d changed the outcome\n got %v / %v\nwant %v / %v", at, ev, au, control, controlAudit)
		}
	}
}

func TestUnknownNeverBecomesDownAndNeverResolves(t *testing.T) {
	r := newAlertRig(t)
	r.ticks(8, "n1", warm)
	r.ticks(10, "n1", routeDown) // route_down open at critical
	base := len(r.events())
	if base != 2 {
		t.Fatalf("setup: %v", r.events())
	}
	// Telemetry stops. L4 gets no evidence (NONE) and after three minutes is
	// UNKNOWN: the route alert is neither escalated nor resolved. L0 silence is
	// a real signal (a reporting process went quiet) and opens its own alert.
	r.ticks(40, "n1", tickSpec{silent: true})
	var routeEvents []string
	for _, e := range r.events() {
		if strings.HasPrefix(e, "route_down") {
			routeEvents = append(routeEvents, e)
		}
	}
	if !reflect.DeepEqual(routeEvents, []string{"route_down/firing/warning", "route_down/firing/critical"}) {
		t.Fatalf("UNKNOWN changed the route alert: %v", routeEvents)
	}
	if got := layerOf(r.store, "n1", LayerL4); got != HealthUnknown {
		t.Fatalf("L4 is %s, want UNKNOWN", got)
	}
	r.store.mu.Lock()
	_, stillOpen := r.app.activeAlerts["route_down:n1:r1"]
	r.store.mu.Unlock()
	if !stillOpen {
		t.Fatal("the route alert was closed without a sustained recovery")
	}
	hasStale := false
	for _, e := range r.events() {
		hasStale = hasStale || strings.HasPrefix(e, "telemetry_stale/firing")
	}
	if !hasStale {
		t.Fatalf("a process that stopped reporting did not alert: %v", r.events())
	}
}

func TestNoEvidenceAndNotAssessedNeverAlert(t *testing.T) {
	r := newAlertRig(t)
	// Reachable by probe, but it has never reported anything.
	r.ticks(100, "n1", tickSpec{noTelem: true})
	if ev := r.events(); len(ev) != 0 {
		t.Fatalf("no telemetry at all produced alerts: %v", ev)
	}
	// Telemetry with no sessions and no route result is "no evidence".
	for i := 0; i < 60; i++ {
		setNode(t, r.store, "n2", func(n *Node) { n.Health, n.LastChecked = "up", r.now })
		r.store.mu.Lock()
		r.store.st.Telemetry["n2"] = TelemetryCursor{NodeID: "n2", LastTelemetry: r.now}
		r.store.mu.Unlock()
		r.app.evaluateHealthAt(r.now)
		if err := r.app.evaluateAlertsAt(context.Background(), r.now); err != nil {
			t.Fatal(err)
		}
		r.now = r.now.Add(10 * time.Second)
	}
	if ev := r.events(); len(ev) != 0 {
		t.Fatalf("a quiet but reporting node produced alerts: %v", ev)
	}
	for _, src := range alertSources {
		if src.layer == LayerL3 || src.layer == LayerL5 || src.layer == LayerL6 {
			t.Fatalf("%s is wired to a NOT_ASSESSED layer", src.kind)
		}
	}
}

func TestHandshakeAlertFollowsL2Hysteresis(t *testing.T) {
	r := newAlertRig(t)
	r.ticks(8, "n1", warm)
	r.ticks(6, "n1", tickSpec{handshake: 60000})
	ev := r.events()
	if len(ev) == 0 || ev[0] != "handshake_error_rate/firing/warning" {
		t.Fatalf("events: %v", ev)
	}
	r.ticks(6, "n1", tickSpec{handshake: 0})
	if last := r.events()[len(r.events())-1]; !strings.HasPrefix(last, "handshake_error_rate/resolved") {
		t.Fatalf("events: %v", r.events())
	}
}

func TestWebhookFailureIsRetriedWithoutLosingOrDuplicating(t *testing.T) {
	r := newAlertRig(t)
	r.ticks(8, "n1", warm)
	r.failing.Store(true)
	r.tick("n1", routeDown)
	setNode(t, r.store, "n1", func(n *Node) { n.Health, n.LastChecked = "up", r.now })
	r.store.mu.Lock()
	r.store.st.Telemetry["n1"] = TelemetryCursor{NodeID: "n1", LastTelemetry: r.now, ActiveSessions: 1, Routes: []telemetry.RouteSnapshot{{RouteID: "r1", Status: "down"}}}
	r.store.mu.Unlock()
	r.app.evaluateHealthAt(r.now)
	if err := r.app.evaluateAlertsAt(context.Background(), r.now); err == nil {
		t.Fatal("a failing webhook was not reported")
	}
	r.now = r.now.Add(10 * time.Second)
	if len(r.events()) != 0 {
		t.Fatalf("delivered while failing: %v", r.events())
	}
	r.failing.Store(false)
	r.tick("n1", routeDown)
	r.tick("n1", routeDown)
	if !reflect.DeepEqual(r.events(), []string{"route_down/firing/warning"}) {
		t.Fatalf("after the webhook came back: %v", r.events())
	}
}

func TestAlertEngineIsRaceSafe(t *testing.T) {
	r := newAlertRig(t)
	r.ticks(8, "n1", warm)
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				at := t0.Add(time.Hour + time.Duration(i)*10*time.Second)
				switch g % 3 {
				case 0:
					r.app.evaluateHealthAt(at)
				case 1:
					_ = r.app.evaluateAlertsAt(context.Background(), at)
				default:
					r.store.HealthSnapshot("", 3)
				}
			}
		}(g)
	}
	wg.Wait()
}

// ---- node_unreachable (L1) and correlation ----

var unreachable = tickSpec{probeDown: true}

func (r *alertRig) kinds(prefix string) []string {
	var out []string
	for _, e := range r.events() {
		if strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return out
}

func (r *alertRig) active(key string) (Alert, bool) {
	r.app.alertMu.Lock()
	defer r.app.alertMu.Unlock()
	a, ok := r.app.activeAlerts[key]
	return a, ok
}

func TestNodeUnreachableNeedsConfirmedL1DownNotARawProbeFailure(t *testing.T) {
	r := newAlertRig(t)
	r.ticks(8, "n1", warm)
	r.tick("n1", unreachable) // one failed probe
	r.ticks(8, "n1", warm)
	for i := 0; i < 40; i++ { // flapping probe
		r.tick("n1", tickSpec{probeDown: i%2 == 0})
	}
	// Confirmed DEGRADED only (dashboard and history, not an alert): four bad
	// samples are DEGRADED, five over thirty seconds would be DOWN.
	r.ticks(8, "n1", warm)
	r.ticks(3, "n1", unreachable)
	if got := layerOf(r.store, "n1", LayerL1); got != HealthDegraded {
		t.Fatalf("setup: L1 is %s, want DEGRADED", got)
	}
	r.ticks(4, "n1", warm)
	if ev := r.events(); len(ev) != 0 {
		t.Fatalf("raw failures or DEGRADED alerted: %v", ev)
	}
}

func TestNodeUnreachableOpensOnceKeepsOneKeyAndResolvesAfterSustainedRecovery(t *testing.T) {
	r := newAlertRig(t)
	r.ticks(8, "n1", warm)
	r.ticks(4, "n1", unreachable)
	if len(r.events()) != 0 {
		t.Fatalf("alerted before L1 was confirmed DOWN: %v", r.events())
	}
	r.tick("n1", unreachable) // fifth BAD over 40 s: confirmed DOWN
	if !reflect.DeepEqual(r.events(), []string{"node_unreachable/firing/critical"}) {
		t.Fatalf("at confirmed DOWN: %v", r.events())
	}
	r.ticks(30, "n1", unreachable)
	if len(r.events()) != 1 {
		t.Fatalf("duplicate alerts for one node: %v", r.events())
	}
	r.app.alertMu.Lock()
	n := 0
	for k := range r.app.activeAlerts {
		if strings.HasPrefix(k, "node_unreachable:") {
			n++
		}
	}
	r.app.alertMu.Unlock()
	if _, ok := r.active("node_unreachable:n1"); !ok || n != 1 {
		t.Fatalf("expected exactly one node_unreachable:n1, have %d", n)
	}

	// RECOVERING keeps it open; sustained recovery (UP) resolves it, once.
	r.ticks(4, "n1", warm)
	if got := layerOf(r.store, "n1", LayerL1); got != HealthRecovering {
		t.Fatalf("L1 is %s, want RECOVERING", got)
	}
	if len(r.events()) != 1 {
		t.Fatalf("resolved while RECOVERING: %v", r.events())
	}
	r.ticks(3, "n1", warm)
	if !reflect.DeepEqual(r.events(), []string{"node_unreachable/firing/critical", "node_unreachable/resolved/critical"}) {
		t.Fatalf("after recovery: %v", r.events())
	}
	r.ticks(20, "n1", warm)
	if len(r.events()) != 2 {
		t.Fatalf("extra events: %v", r.events())
	}
}

func TestNodeUnreachableCarriesTheRequiredEvidence(t *testing.T) {
	r := newAlertRig(t)
	r.ticks(8, "n1", warm)
	lastOK := r.now.Add(-10 * time.Second) // the last warm tick
	r.ticks(5, "n1", unreachable)
	r.mu.Lock()
	a := r.got[0]
	r.mu.Unlock()
	f := a.EvidenceFields
	if a.Type != kindUnreachable || a.Severity != "critical" || a.Health != HealthDown {
		t.Fatalf("alert: %+v", a)
	}
	for _, k := range []string{"node_id", "address", "previous_state", "current_state", "failure_duration", "last_successful_reachability", "transition_reason", "event_id"} {
		if f[k] == "" {
			t.Errorf("evidence lacks %s: %v", k, f)
		}
	}
	if f["node_id"] != "n1" || f["address"] != "127.0.0.1:1" || f["previous_state"] != HealthDegraded || f["current_state"] != HealthDown {
		t.Errorf("evidence: %v", f)
	}
	if f["last_successful_reachability"] != lastOK.UTC().Format(time.RFC3339) {
		t.Errorf("last successful reachability %s, want %s", f["last_successful_reachability"], lastOK.UTC().Format(time.RFC3339))
	}
	if !strings.Contains(f["transition_reason"], "BAD samples") || !strings.Contains(a.Evidence, "L1 DEGRADED -> DOWN") {
		t.Errorf("reason/evidence: %q / %q", f["transition_reason"], a.Evidence)
	}
	// The event id ties the alert to the health transition in the audit log.
	entries, _ := r.app.audit.List(0)
	found := false
	for _, e := range entries {
		if e.Action == "health.transition" && e.Details["event_id"] == f["event_id"] && e.Details["to"] == HealthDown && e.Details["layer"] == LayerL1 {
			found = true
		}
		if e.Action == "alert.firing" && e.Details["event_id"] != f["event_id"] && e.Details["type"] == kindUnreachable {
			t.Errorf("alert audit entry lacks the event id: %v", e.Details)
		}
	}
	if !found {
		t.Fatalf("no health.transition audit entry with event id %s", f["event_id"])
	}
}

func TestUnknownReachabilityIsNotAFailureAndDoesNotResolve(t *testing.T) {
	// Never probed: nothing to alert about.
	r := newAlertRig(t)
	r.ticks(60, "n1", tickSpec{staleProbe: true})
	if ev := r.events(); len(ev) != 0 {
		t.Fatalf("UNKNOWN reachability alerted: %v", ev)
	}
	// An open alert is held, not resolved, when evidence disappears.
	r = newAlertRig(t)
	r.ticks(8, "n1", warm)
	r.ticks(8, "n1", unreachable)
	r.ticks(40, "n1", tickSpec{staleProbe: true})
	if got := layerOf(r.store, "n1", LayerL1); got != HealthUnknown {
		t.Fatalf("L1 is %s, want UNKNOWN", got)
	}
	if _, ok := r.active("node_unreachable:n1"); !ok {
		t.Fatal("UNKNOWN resolved the alert")
	}
	if got := r.kinds("node_unreachable"); !reflect.DeepEqual(got, []string{"node_unreachable/firing/critical"}) {
		t.Fatalf("UNKNOWN produced events: %v", got)
	}
}

func TestSimultaneousL0AndL1FailureIsOneIncidentNotAFlood(t *testing.T) {
	r := newAlertRig(t)
	r.ticks(8, "n1", warm)
	// The node vanishes: the probe fails and telemetry stops.
	r.ticks(60, "n1", tickSpec{probeDown: true, silent: true})
	if got := r.events(); !reflect.DeepEqual(got, []string{"node_unreachable/firing/critical"}) {
		t.Fatalf("notifications for one incident: %v", got)
	}
	// The layer evidence is kept: telemetry_stale is recorded, marked correlated,
	// and audited, just not notified.
	a, ok := r.active("telemetry_stale:n1")
	if !ok || !a.Unnotified || a.CorrelatedWith != "node_unreachable:n1" {
		t.Fatalf("telemetry_stale not recorded as correlated: %+v ok=%v", a, ok)
	}
	root, _ := r.active("node_unreachable:n1")
	if !reflect.DeepEqual(root.CorrelatedAlerts, []string{"telemetry_stale:n1"}) {
		t.Fatalf("the root alert does not list what it covers: %v", root.CorrelatedAlerts)
	}
	if au := r.auditActions(); !reflect.DeepEqual(au, []string{"alert.firing:node_unreachable:critical", "alert.correlated:telemetry_stale:warning", "alert.escalated:telemetry_stale:critical"}) {
		t.Fatalf("audit: %v", au)
	}
	// Everything recovers: the root is resolved and announced; the covered alert
	// was never announced, so it closes silently.
	r.ticks(12, "n1", warm)
	if got := r.events(); !reflect.DeepEqual(got, []string{"node_unreachable/firing/critical", "node_unreachable/resolved/critical"}) {
		t.Fatalf("after recovery: %v", got)
	}
	r.app.alertMu.Lock()
	left := len(r.app.activeAlerts)
	r.app.alertMu.Unlock()
	if left != 0 {
		t.Fatalf("%d alerts left open", left)
	}
}

func TestCoveredAlertIsAnnouncedWhenTheIncidentEndsButItIsStillBad(t *testing.T) {
	r := newAlertRig(t)
	r.ticks(8, "n1", warm)
	r.ticks(60, "n1", tickSpec{probeDown: true, silent: true})
	if got := r.events(); len(got) != 1 {
		t.Fatalf("setup: %v", got)
	}
	// The probe works again but the node's telemetry is still silent.
	r.ticks(12, "n1", tickSpec{silent: true})
	got := r.events()
	want := []string{"node_unreachable/firing/critical", "node_unreachable/resolved/critical"}
	if !reflect.DeepEqual(got[:2], want) || len(got) != 3 || !strings.HasPrefix(got[2], "telemetry_stale/firing") {
		t.Fatalf("the still-bad covered alert was not announced after the incident: %v", got)
	}
	if a, ok := r.active("telemetry_stale:n1"); !ok || a.Unnotified || a.CorrelatedWith != "" {
		t.Fatalf("alert state after announcement: %+v", a)
	}
}

func TestEarlierL0AlertIsNotEscalatedLoudlyOnceTheNodeIsUnreachable(t *testing.T) {
	r := newAlertRig(t)
	r.ticks(8, "n1", warm)
	// Telemetry goes quiet and, a moment later, the node stops answering
	// probes. The L0 warning (silent tick 19) is announced first; the probe
	// failure is confirmed DOWN at silent tick 21; L0 would escalate at tick 22.
	r.ticks(16, "n1", tickSpec{silent: true})
	r.ticks(40, "n1", tickSpec{silent: true, probeDown: true})
	if got := r.kinds("telemetry_stale"); !reflect.DeepEqual(got, []string{"telemetry_stale/firing/warning"}) {
		t.Fatalf("telemetry_stale notifications: %v (the correlated escalation must not be sent)", got)
	}
	if got := r.kinds("node_unreachable"); !reflect.DeepEqual(got, []string{"node_unreachable/firing/critical"}) {
		t.Fatalf("node_unreachable: %v", got)
	}
	a, ok := r.active("telemetry_stale:n1")
	if !ok || a.Severity != "critical" {
		t.Fatalf("the escalation was not recorded: %+v ok=%v", a, ok)
	}
}

func TestUnreachableIncidentIsDeterministicAndRestartSafe(t *testing.T) {
	scenario := func(restartAt int) ([]string, []string) {
		r := newAlertRig(t)
		for i := 0; i < 120; i++ {
			if i == restartAt {
				r.open()
			}
			switch {
			case i < 8:
				r.tick("n1", warm)
			case i < 60:
				r.tick("n1", tickSpec{probeDown: true, silent: true})
			case i < 70:
				r.tick("n1", tickSpec{silent: true})
			default:
				r.tick("n1", warm)
			}
		}
		return r.events(), r.auditActions()
	}
	control, controlAudit := scenario(-1)
	if len(control) < 3 {
		t.Fatalf("control: %v", control)
	}
	if again, _ := scenario(-1); !reflect.DeepEqual(again, control) {
		t.Fatalf("not deterministic: %v vs %v", again, control)
	}
	for _, at := range []int{5, 10, 13, 14, 25, 45, 59, 61, 66, 72, 80, 110} {
		ev, au := scenario(at)
		if !reflect.DeepEqual(ev, control) || !reflect.DeepEqual(au, controlAudit) {
			t.Fatalf("restart at tick %d changed the outcome\n got %v / %v\nwant %v / %v", at, ev, au, control, controlAudit)
		}
	}
}
