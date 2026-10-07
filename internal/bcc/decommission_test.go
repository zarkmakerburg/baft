package bcc

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/tunnelnode"
)

func activateTunnel(t *testing.T, s *Store, now time.Time) Tunnel {
	t.Helper()
	tn := driveToObserve(t, s, now)
	ackObserved(t, s, "ir-1", goodObserved(tn, tunnelnode.RoleIR), now)
	ackObserved(t, s, "ex-1", goodObserved(tn, tunnelnode.RoleEX), now)
	for _, node := range []string{"ir-1", "ex-1"} {
		jobs := pullAll(t, s, node)
		if len(jobs) != 1 || jobs[0].Type != JobTunnelFinalize {
			t.Fatalf("%s finalize job: %+v", node, jobs)
		}
		if err := s.AckJob(node, "tok-"+node, jobs[0].ID, "succeeded", "finalized"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AdvanceTunnels(now); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.GetTunnel(tn.ID)
	if got.Phase != TunnelActive {
		t.Fatalf("activation ended in %s", got.Phase)
	}
	return got
}

func retireMessage(tn Tunnel, node string) string {
	d := tn.Digests[node]
	gen := tn.ObservedGen[node]
	before := tunnelnode.Live{
		InstanceID: tn.InstanceID, ConfigPresent: true, UnitPresent: true,
		ConfigSHA256: d.Config, UnitSHA256: d.Unit, MarkerSHA256: d.Marker,
		MarkerPresent: true, MarkerManagedBy: "baft", MarkerTunnelID: tn.ID,
		MarkerGeneration: gen, MarkerConfigMatches: true, MarkerUnitMatches: true,
		NodeGeneration: gen, ServiceActive: true,
	}
	ev := tunnelnode.RetireEvidence{
		TunnelID: tn.ID, Instance: tn.InstanceID, Before: before,
		After: tunnelnode.Live{InstanceID: tn.InstanceID, NodeGeneration: gen},
	}
	b, _ := json.Marshal(ev)
	return string(b)
}

func TestDecommissionPlanIsReadOnlyAndApplyStartsWithIR(t *testing.T) {
	s := tunnelStore(t)
	now := time.Now().UTC()
	tn := activateTunnel(t, s, now)
	beforeJobs := len(s.ListJobs())
	plan, err := s.BuildDecommissionPlan(tn.ID, now)
	if err != nil || !plan.OK || plan.Hash == "" || !plan.Disruptive {
		t.Fatalf("plan %+v err=%v", plan, err)
	}
	if len(s.ListJobs()) != beforeJobs {
		t.Fatal("decommission planning mutated jobs")
	}
	if len(plan.Nodes) != 2 || plan.Nodes[0].Role != tunnelnode.RoleIR || plan.Nodes[1].Role != tunnelnode.RoleEX {
		t.Fatalf("plan node order %+v", plan.Nodes)
	}

	got, _, err := s.StartDecommissionFromPlan(tn.ID, plan.Hash, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != TunnelDecommissioningIR {
		t.Fatalf("phase %s", got.Phase)
	}
	jobs := pullAll(t, s, "ir-1")
	if len(jobs) != 1 || jobs[0].Type != JobTunnelRetire {
		t.Fatalf("IR retire job %+v", jobs)
	}
	if jobs[0].Params["generation"] != "4" ||
		jobs[0].Params["config_sha256"] != strings.Repeat("a", 64) ||
		jobs[0].Params["unit_sha256"] != strings.Repeat("b", 64) ||
		jobs[0].Params["marker_sha256"] != strings.Repeat("c", 64) {
		t.Fatalf("retire guard params %+v", jobs[0].Params)
	}
	if ex := pullAll(t, s, "ex-1"); len(ex) != 0 {
		t.Fatalf("EX retire was queued before IR completed: %+v", ex)
	}
}

func TestDecommissionRequiresCurrentReviewedPlan(t *testing.T) {
	s := tunnelStore(t)
	now := time.Now().UTC()
	tn := activateTunnel(t, s, now)
	plan, err := s.BuildDecommissionPlan(tn.ID, now)
	if err != nil {
		t.Fatal(err)
	}

	s.mu.Lock()
	x := s.st.Tunnels[tn.ID]
	x.Digests["ir-1"] = NodeDigests{
		Config: strings.Repeat("d", 64), Unit: strings.Repeat("b", 64), Marker: strings.Repeat("c", 64),
	}
	s.st.Tunnels[tn.ID] = x
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	s.mu.Unlock()

	_, cur, err := s.StartDecommissionFromPlan(tn.ID, plan.Hash, now)
	var stale ErrStaleDecommissionPlan
	if !errors.As(err, &stale) || cur.Hash == "" || cur.Hash == plan.Hash {
		t.Fatalf("stale plan accepted or not refreshed: cur=%+v err=%v", cur, err)
	}
	if got, _ := s.GetTunnel(tn.ID); got.Phase != TunnelActive {
		t.Fatalf("stale plan mutated phase to %s", got.Phase)
	}
}

func TestDecommissionCompletesIRThenEX(t *testing.T) {
	s := tunnelStore(t)
	now := time.Now().UTC()
	tn := activateTunnel(t, s, now)
	plan, _ := s.BuildDecommissionPlan(tn.ID, now)
	if _, _, err := s.StartDecommissionFromPlan(tn.ID, plan.Hash, now); err != nil {
		t.Fatal(err)
	}

	ir := pullAll(t, s, "ir-1")
	if len(ir) != 1 {
		t.Fatalf("IR jobs %+v", ir)
	}
	if err := s.AckJobOutput("ir-1", "tok-ir-1", ir[0].ID, "succeeded", retireMessage(tn, "ir-1"), ""); err != nil {
		t.Fatal(err)
	}
	if ev, err := s.AdvanceTunnels(now); err != nil || len(ev) != 0 {
		t.Fatalf("IR advance events=%+v err=%v", ev, err)
	}
	mid, _ := s.GetTunnel(tn.ID)
	if mid.Phase != TunnelDecommissioningEX || !mid.DecommissionedNodes["ir-1"] || mid.DecommissionedNodes["ex-1"] {
		t.Fatalf("mid state %+v", mid)
	}

	ex := pullAll(t, s, "ex-1")
	if len(ex) != 1 || ex[0].Type != JobTunnelRetire {
		t.Fatalf("EX retire job %+v", ex)
	}
	if err := s.AckJobOutput("ex-1", "tok-ex-1", ex[0].ID, "succeeded", retireMessage(tn, "ex-1"), ""); err != nil {
		t.Fatal(err)
	}
	events, err := s.AdvanceTunnels(now)
	if err != nil || len(events) != 1 || events[0].Action != "tunnel.decommissioned" {
		t.Fatalf("final events %+v err=%v", events, err)
	}
	done, _ := s.GetTunnel(tn.ID)
	if done.Phase != TunnelDecommissioned || !done.DecommissionedNodes["ir-1"] || !done.DecommissionedNodes["ex-1"] {
		t.Fatalf("final state %+v", done)
	}
}

func TestDecommissionPartialFailureRetriesOnlyPendingEX(t *testing.T) {
	s := tunnelStore(t)
	now := time.Now().UTC()
	tn := activateTunnel(t, s, now)
	plan, _ := s.BuildDecommissionPlan(tn.ID, now)
	s.StartDecommissionFromPlan(tn.ID, plan.Hash, now)

	ir := pullAll(t, s, "ir-1")[0]
	s.AckJobOutput("ir-1", "tok-ir-1", ir.ID, "succeeded", retireMessage(tn, "ir-1"), "")
	s.AdvanceTunnels(now)
	ex := pullAll(t, s, "ex-1")[0]
	s.AckJob("ex-1", "tok-ex-1", ex.ID, "failed", "temporary node error")
	events, _ := s.AdvanceTunnels(now)
	if len(events) != 1 || events[0].Action != "tunnel.decommission_failed" {
		t.Fatalf("failure events %+v", events)
	}
	failed, _ := s.GetTunnel(tn.ID)
	if failed.Phase != TunnelDecommissionFailed || !failed.DecommissionedNodes["ir-1"] {
		t.Fatalf("failed state %+v", failed)
	}

	retryPlan, err := s.BuildDecommissionPlan(tn.ID, now.Add(time.Minute))
	if err != nil || !retryPlan.OK || !retryPlan.Nodes[0].Already {
		t.Fatalf("retry plan %+v err=%v", retryPlan, err)
	}
	retry, _, err := s.StartDecommissionFromPlan(tn.ID, retryPlan.Hash, now.Add(time.Minute))
	if err != nil || retry.Phase != TunnelDecommissioningEX {
		t.Fatalf("retry %+v err=%v", retry, err)
	}
	if irAgain := pullAll(t, s, "ir-1"); len(irAgain) != 0 {
		t.Fatalf("retry touched already retired IR: %+v", irAgain)
	}
	exRetry := pullAll(t, s, "ex-1")
	if len(exRetry) != 1 || exRetry[0].Type != JobTunnelRetire {
		t.Fatalf("retry EX job %+v", exRetry)
	}
	s.AckJobOutput("ex-1", "tok-ex-1", exRetry[0].ID, "succeeded", retireMessage(tn, "ex-1"), "")
	s.AdvanceTunnels(now.Add(time.Minute))
	if done, _ := s.GetTunnel(tn.ID); done.Phase != TunnelDecommissioned {
		t.Fatalf("retry ended in %s", done.Phase)
	}
}

func TestDecommissionStateSurvivesBCCRestart(t *testing.T) {
	s := tunnelStore(t)
	now := time.Now().UTC()
	tn := activateTunnel(t, s, now)
	plan, _ := s.BuildDecommissionPlan(tn.ID, now)
	started, _, err := s.StartDecommissionFromPlan(tn.ID, plan.Hash, now)
	if err != nil {
		t.Fatal(err)
	}
	path := s.path
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reopened.GetTunnel(tn.ID)
	if !ok || got.Phase != TunnelDecommissioningIR || got.JobID != started.JobID || got.DecommissionPlanHash != plan.Hash {
		t.Fatalf("restart state %+v", got)
	}
	jobs := pullAll(t, reopened, "ir-1")
	if len(jobs) != 1 || jobs[0].ID != started.JobID || jobs[0].Type != JobTunnelRetire {
		t.Fatalf("restart lost retire job %+v", jobs)
	}
}

func TestStaleAgentBlocksDecommissionPlan(t *testing.T) {
	s := tunnelStore(t)
	now := time.Now().UTC()
	tn := activateTunnel(t, s, now)
	s.mu.Lock()
	n := s.st.Nodes["ir-1"]
	n.AgentSeen = now.Add(-agentFreshness - time.Second)
	s.st.Nodes["ir-1"] = n
	s.mu.Unlock()
	plan, err := s.BuildDecommissionPlan(tn.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if plan.OK {
		t.Fatalf("stale agent passed decommission plan: %+v", plan.Gates)
	}
	found := false
	for _, g := range plan.Gates {
		if g.Name == "node: ir-1" && g.Status == "FAIL" && strings.Contains(g.Detail, "stale") {
			found = true
		}
	}
	if !found {
		t.Fatalf("stale-agent gate missing: %+v", plan.Gates)
	}
}

func TestKnownDriftBlocksDecommissionPlan(t *testing.T) {
	s := tunnelStore(t)
	now := time.Now().UTC()
	tn := activateTunnel(t, s, now)
	s.mu.Lock()
	x := s.st.Tunnels[tn.ID]
	x.Drift = &DriftReport{State: DriftDrifted, CheckedAt: now, Nodes: map[string]DriftNode{
		"ir-1": {State: DriftDrifted, Problems: []string{"config changed"}},
	}}
	s.st.Tunnels[tn.ID] = x
	s.mu.Unlock()
	plan, err := s.BuildDecommissionPlan(tn.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if plan.OK {
		t.Fatalf("known drift passed plan: %+v", plan.Gates)
	}
}

func TestDecommissionFailedBlocksReplacementInSameManagedSlot(t *testing.T) {
	s := tunnelStore(t)
	now := time.Now().UTC()
	tn := activateTunnel(t, s, now)
	s.mu.Lock()
	x := s.st.Tunnels[tn.ID]
	x.Phase = TunnelDecommissionFailed
	x.DecommissionedNodes = map[string]bool{"ir-1": true}
	s.st.Tunnels[tn.ID] = x
	s.mu.Unlock()
	if _, err := s.CreateTunnel(TunnelRequest{EXNode: "ex-1", IRNode: "ir-1"}, now.Add(time.Minute)); err == nil || !strings.Contains(err.Error(), "incomplete decommission") {
		t.Fatalf("replacement allowed over partial decommission: %v", err)
	}
}

func TestDecommissionRequestAndOutcomeHaveDurableAuditIntents(t *testing.T) {
	s := tunnelStore(t)
	now := time.Now().UTC()
	tn := activateTunnel(t, s, now)
	plan, err := s.BuildDecommissionPlan(tn.ID, now)
	if err != nil || !plan.OK {
		t.Fatalf("plan %+v err=%v", plan, err)
	}
	started, _, err := s.StartDecommissionFromPlanAudited(tn.ID, plan.Hash, now, AuditEntry{Actor: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	pending := s.PendingSecurityAuditIntents()
	if len(pending) != 1 || pending[0].Action != "tunnel.decommission.request" || pending[0].Target != tn.ID {
		t.Fatalf("request audit intent %+v", pending)
	}
	ir := pullAll(t, s, "ir-1")
	if len(ir) != 1 || ir[0].ID != started.JobID {
		t.Fatalf("IR retire jobs %+v", ir)
	}
	if err := s.AckJobOutput("ir-1", "tok-ir-1", ir[0].ID, "succeeded", retireMessage(tn, "ir-1"), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdvanceTunnels(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	ex := pullAll(t, s, "ex-1")
	if len(ex) != 1 {
		t.Fatalf("EX retire jobs %+v", ex)
	}
	if err := s.AckJobOutput("ex-1", "tok-ex-1", ex[0].ID, "succeeded", retireMessage(tn, "ex-1"), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdvanceTunnels(now.Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	pending = s.PendingSecurityAuditIntents()
	var request, outcome bool
	for _, a := range pending {
		request = request || a.Action == "tunnel.decommission.request"
		outcome = outcome || a.Action == "tunnel.decommissioned"
	}
	if !request || !outcome {
		t.Fatalf("durable audit intents missing request=%v outcome=%v: %+v", request, outcome, pending)
	}

	reopened, err := OpenStore(s.path)
	if err != nil {
		t.Fatal(err)
	}
	pending = reopened.PendingSecurityAuditIntents()
	request, outcome = false, false
	for _, a := range pending {
		request = request || a.Action == "tunnel.decommission.request"
		outcome = outcome || a.Action == "tunnel.decommissioned"
	}
	if !request || !outcome {
		t.Fatalf("restart lost audit intents: %+v", pending)
	}
}

func TestDecommissionAPIPlanApplyStatusAndAudit(t *testing.T) {
	s := tunnelStore(t)
	now := time.Now().UTC()
	tn := activateTunnel(t, s, now)
	app, err := NewServer(s, "admin")
	if err != nil {
		t.Fatal(err)
	}

	planRR := httptest.NewRecorder()
	app.Handler().ServeHTTP(planRR, authReq(http.MethodPost, "/api/tunnels/decommission/plan", "admin", map[string]string{"id": tn.ID}))
	if planRR.Code != http.StatusOK {
		t.Fatalf("plan status=%d body=%s", planRR.Code, planRR.Body.String())
	}
	var plan DecommissionPlan
	if err := json.Unmarshal(planRR.Body.Bytes(), &plan); err != nil || !plan.OK || plan.Hash == "" {
		t.Fatalf("plan %+v err=%v", plan, err)
	}

	applyRR := httptest.NewRecorder()
	app.Handler().ServeHTTP(applyRR, authReq(http.MethodPost, "/api/tunnels/decommission", "admin", map[string]string{"id": tn.ID, "plan_hash": plan.Hash}))
	if applyRR.Code != http.StatusAccepted {
		t.Fatalf("apply status=%d body=%s", applyRR.Code, applyRR.Body.String())
	}
	var started Tunnel
	if err := json.Unmarshal(applyRR.Body.Bytes(), &started); err != nil || started.Phase != TunnelDecommissioningIR {
		t.Fatalf("started %+v err=%v", started, err)
	}

	ir := pullAll(t, s, "ir-1")
	if len(ir) != 1 {
		t.Fatalf("IR jobs %+v", ir)
	}
	if err := s.AckJobOutput("ir-1", "tok-ir-1", ir[0].ID, "succeeded", retireMessage(tn, "ir-1"), ""); err != nil {
		t.Fatal(err)
	}
	app.AdvanceTunnels()

	ex := pullAll(t, s, "ex-1")
	if len(ex) != 1 {
		t.Fatalf("EX jobs %+v", ex)
	}
	if err := s.AckJobOutput("ex-1", "tok-ex-1", ex[0].ID, "succeeded", retireMessage(tn, "ex-1"), ""); err != nil {
		t.Fatal(err)
	}
	app.AdvanceTunnels()

	statusRR := httptest.NewRecorder()
	app.Handler().ServeHTTP(statusRR, authReq(http.MethodGet, "/api/tunnels?id="+tn.ID, "admin", nil))
	if statusRR.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", statusRR.Code, statusRR.Body.String())
	}
	var done Tunnel
	if err := json.Unmarshal(statusRR.Body.Bytes(), &done); err != nil || done.Phase != TunnelDecommissioned {
		t.Fatalf("status tunnel %+v err=%v", done, err)
	}

	auditRR := httptest.NewRecorder()
	app.Handler().ServeHTTP(auditRR, authReq(http.MethodGet, "/api/audit", "admin", nil))
	if auditRR.Code != http.StatusOK {
		t.Fatalf("audit status=%d body=%s", auditRR.Code, auditRR.Body.String())
	}
	body := auditRR.Body.String()
	for _, want := range []string{"tunnel.decommission.plan", "tunnel.decommission.request", "tunnel.decommissioned"} {
		if !strings.Contains(body, want) {
			t.Errorf("audit lacks %s: %s", want, body)
		}
	}
}
