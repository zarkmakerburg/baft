package bcc

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testPairCode  = "BAFTPAIR1:AAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	testReplyCode = "BAFTREPLY1:BBBBBBBBBBBBBBBBBBBBBBBBBBBB"
)

func tunnelStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	for id, role := range map[string]string{"ex-1": "foreign", "ir-1": "worker"} {
		if _, err := s.UpsertNode(Node{ID: id, Address: "203.0.113.7:22", Role: role}, "tok-"+id); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func newTunnel(t *testing.T, s *Store, now time.Time) Tunnel {
	t.Helper()
	tn, err := s.CreateTunnel(TunnelRequest{EXNode: "ex-1", IRNode: "ir-1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	return tn
}

func pullAll(t *testing.T, s *Store, node string) []Job {
	t.Helper()
	jobs, err := s.PullJobs(node, "tok-"+node)
	if err != nil {
		t.Fatal(err)
	}
	return jobs
}

func TestPlanDefaultsAndFirstStep(t *testing.T) {
	s := tunnelStore(t)
	now := time.Now()
	tn := newTunnel(t, s, now)
	if tn.PublicAddress != "203.0.113.7" || tn.Port != 8443 || tn.Target != "127.0.0.1:2443" || tn.RouteListen != "127.0.0.1:1443" || tn.RouteID != "service-main" {
		t.Fatalf("defaults: %+v", tn)
	}
	jobs := pullAll(t, s, "ex-1")
	if len(jobs) != 1 || jobs[0].Type != JobTunnelPrepareEX || jobs[0].Params["tunnel_id"] != tn.ID {
		t.Fatalf("first step: %+v", jobs)
	}
}

func TestSecretsAreHiddenFromListingsAndWipedAfterUse(t *testing.T) {
	s := tunnelStore(t)
	now := time.Now()
	tn := newTunnel(t, s, now)
	j := pullAll(t, s, "ex-1")[0]
	if err := s.AckJobOutput("ex-1", "tok-ex-1", j.ID, "succeeded", "pairing code issued", testPairCode); err != nil {
		t.Fatal(err)
	}
	// Between the ack and the next step the code sits in the record, but no listing shows it.
	app, _ := NewServer(s, "admin")
	req := authReq(http.MethodGet, "/api/jobs", "admin", nil)
	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, req)
	if strings.Contains(rr.Body.String(), "BAFTPAIR1") {
		t.Fatal("/api/jobs shows a pairing code")
	}
	if _, err := s.AdvanceTunnels(now); err != nil {
		t.Fatal(err)
	}
	ir := pullAll(t, s, "ir-1")
	if len(ir) != 1 || ir[0].Type != JobTunnelPrepareIR || ir[0].Params["code"] != testPairCode {
		t.Fatalf("IR step: %+v", ir)
	}
	if old, _ := s.jobByID(j.ID); old.Output != "" {
		t.Fatal("the EX's output survived being passed on")
	}
	if err := s.AckJobOutput("ir-1", "tok-ir-1", ir[0].ID, "succeeded", "reply code issued", testReplyCode); err != nil {
		t.Fatal(err)
	}
	if done, _ := s.jobByID(ir[0].ID); done.Params["code"] != "" {
		t.Fatal("the pairing code stayed in the finished prepare_ir job")
	}
	s.AdvanceTunnels(now)
	ex := pullAll(t, s, "ex-1")
	if len(ex) != 1 || ex[0].Type != JobTunnelCommitEX || ex[0].Params["reply"] != testReplyCode {
		t.Fatalf("commit step: %+v", ex)
	}
	s.AckJobOutput("ex-1", "tok-ex-1", ex[0].ID, "succeeded", "ok", "")
	if done, _ := s.jobByID(ex[0].ID); done.Params["reply"] != "" {
		t.Fatal("the reply stayed in the finished commit_ex job")
	}
	_ = tn
}

func (s *Store) jobByID(id string) (Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.st.Jobs[id]
	return j, ok
}

func TestAnUnansweredStepTimesOutAndRollsBack(t *testing.T) {
	s := tunnelStore(t)
	now := time.Now()
	tn := newTunnel(t, s, now)
	pullAll(t, s, "ex-1") // dispatched, but the agent never answers

	if ev, _ := s.AdvanceTunnels(now.Add(19 * time.Minute)); len(ev) != 0 {
		t.Fatalf("early event %+v", ev)
	}
	if got, _ := s.GetTunnel(tn.ID); got.Phase != TunnelPreparingEX {
		t.Fatalf("phase %s before the timeout", got.Phase)
	}
	s.AdvanceTunnels(now.Add(21 * time.Minute))
	got, _ := s.GetTunnel(tn.ID)
	if got.Phase != TunnelRollingBack || !strings.Contains(got.Error, "timed out") || len(got.RollbackJobs) != 1 {
		t.Fatalf("after timeout: %+v", got)
	}
	// The node stays silent through the rollback too: BCC reports it instead of waiting forever.
	ev, _ := s.AdvanceTunnels(now.Add(21*time.Minute + tunnelRollbackTimeout + time.Minute))
	got, _ = s.GetTunnel(tn.ID)
	if got.Phase != TunnelRollbackFailed || len(ev) != 1 || ev[0].Action != "tunnel.rollback_failed" {
		t.Fatalf("after rollback timeout: %+v %+v", got, ev)
	}
	// A tunnel stuck like this blocks new changes on its nodes until it is dealt with...
	if _, err := s.CreateTunnel(TunnelRequest{EXNode: "ex-1", IRNode: "ir-1"}, now); err == nil {
		t.Fatal("new tunnel allowed on a node with a failed rollback")
	}
	// ...and an operator can retry the rollback once the node is back.
	if _, err := s.CancelTunnel(tn.ID, "node is back", now.Add(4*time.Hour)); err != nil {
		t.Fatal(err)
	}
	rb := pullAll(t, s, "ex-1")
	var rollback *Job
	for i := range rb {
		if rb[i].Type == JobTunnelRollback && rb[i].Status == "dispatched" {
			rollback = &rb[i]
		}
	}
	if rollback == nil {
		t.Fatalf("no rollback retry queued: %+v", rb)
	}
	s.AckJob("ex-1", "tok-ex-1", rollback.ID, "succeeded", "rolled back")
	ev, _ = s.AdvanceTunnels(now.Add(4 * time.Hour))
	if got, _ = s.GetTunnel(tn.ID); got.Phase != TunnelRolledBack || len(ev) != 1 {
		t.Fatalf("after retry: %+v %+v", got, ev)
	}
}

func TestEmptyPairingOutputFailsClosed(t *testing.T) {
	s := tunnelStore(t)
	now := time.Now()
	tn := newTunnel(t, s, now)
	j := pullAll(t, s, "ex-1")[0]
	s.AckJobOutput("ex-1", "tok-ex-1", j.ID, "succeeded", "ok", "") // success without a code
	s.AdvanceTunnels(now)
	got, _ := s.GetTunnel(tn.ID)
	if got.Phase == TunnelPreparingIR || !strings.Contains(got.Error, "no pairing code") {
		t.Fatalf("went on without a pairing code: %+v", got)
	}
}

func TestHealthIsRetriedThenGivesUp(t *testing.T) {
	s := tunnelStore(t)
	now := time.Now()
	tn := newTunnel(t, s, now)
	step := func(node string, status, out string) {
		t.Helper()
		j := pullAll(t, s, node)
		if len(j) != 1 {
			t.Fatalf("%s: expected one job, got %+v", node, j)
		}
		if err := s.AckJobOutput(node, "tok-"+node, j[0].ID, status, "m", out); err != nil {
			t.Fatal(err)
		}
		s.AdvanceTunnels(now)
	}
	step("ex-1", "succeeded", testPairCode)
	step("ir-1", "succeeded", testReplyCode)
	step("ex-1", "succeeded", "")
	step("ir-1", "succeeded", "")
	for i := 0; i < tunnelHealthAttempts-1; i++ {
		step("ir-1", "failed", "")
		if got, _ := s.GetTunnel(tn.ID); got.Phase != TunnelHealthIR {
			t.Fatalf("gave up after %d failed health checks (%s)", i+1, got.Phase)
		}
	}
	step("ir-1", "failed", "")
	got, _ := s.GetTunnel(tn.ID)
	if got.Phase != TunnelRollingBack || len(got.RollbackJobs) != 2 {
		t.Fatalf("after the last failed health check: %+v", got)
	}
	// IR first, then EX.
	if first := s.st.Jobs[got.RollbackJobs[0]]; first.NodeID != "ir-1" {
		t.Fatalf("rollback starts with %s", first.NodeID)
	}
}

func TestTunnelsSurviveARestartAndBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s.UpsertNode(Node{ID: "ex-1", Address: "203.0.113.7:22", Role: "foreign"}, "tok")
	s.UpsertNode(Node{ID: "ir-1", Address: "198.51.100.2:22", Role: "worker"}, "tok2")
	tn, err := s.CreateTunnel(TunnelRequest{EXNode: "ex-1", IRNode: "ir-1"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	again, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := again.GetTunnel(tn.ID)
	if !ok || got.Phase != TunnelPreparingEX || got.JobID != tn.JobID || len(got.Jobs) != 1 {
		t.Fatalf("tunnel after restart: %+v %v", got, ok)
	}
	if v, err := stateSchemaVersion(path); err != nil || v != len(stateMigrations) {
		t.Fatalf("schema version %d %v", v, err)
	}
}

func TestRetiredEnrollJobsAreFailedNotServed(t *testing.T) {
	s := tunnelStore(t)
	s.mu.Lock()
	s.newJobLocked(Job{Type: jobEnrollPeerLegacy, NodeID: "ex-1"})
	s.mu.Unlock()
	if jobs := pullAll(t, s, "ex-1"); len(jobs) != 0 {
		t.Fatalf("a retired job type was served: %+v", jobs)
	}
	j, _ := s.jobByID("job-00000001")
	if j.Status != "failed" || !strings.Contains(j.Message, "retired") {
		t.Fatalf("retired job: %+v", j)
	}
}
