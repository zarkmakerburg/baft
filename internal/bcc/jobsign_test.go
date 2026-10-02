package bcc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/agentjob"
	"github.com/zarkmakerburg/baft/internal/release"
)

func TestAgentsReceiveSignedJobs(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []Node{{ID: "ex-1", Address: "127.0.0.1:1", Role: "foreign"}, {ID: "w-1", Address: "127.0.0.1:2", Role: "worker", PublicKey: "workerpublickey000"}} {
		if _, err := store.UpsertNode(n, "tok-"+n.ID); err != nil {
			t.Fatal(err)
		}
	}
	app, err := NewServer(store, "admin")
	if err != nil {
		t.Fatal(err)
	}
	key, created, err := LoadOrCreateJobKey(filepath.Join(dir, "state.json.job-key"))
	if err != nil || !created {
		t.Fatalf("job key: %v created=%v", err, created)
	}
	app.ConfigureJobSigning(key)
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	app.now = func() time.Time { return now }

	do := func(r *http.Request) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		app.Handler().ServeHTTP(rr, r)
		return rr
	}
	if rr := do(authReq(http.MethodPost, "/api/deploy", "admin", map[string]any{"node_ids": []string{"ex-1"}, "version": "v1.2.0"})); rr.Code != http.StatusAccepted {
		t.Fatalf("deploy = %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(authReq(http.MethodPost, "/api/deploy", "admin", map[string]any{"node_ids": []string{"ex-1"}, "version": "v1.3.0"})); rr.Code != http.StatusAccepted {
		t.Fatalf("second deploy = %d %s", rr.Code, rr.Body.String())
	}
	rr := do(authReq(http.MethodGet, "/api/agent/jobs?node_id=ex-1", "tok-ex-1", nil))
	if rr.Code != 200 {
		t.Fatalf("pull = %d", rr.Code)
	}
	var jobs []AgentJob
	if err := json.Unmarshal(rr.Body.Bytes(), &jobs); err != nil || len(jobs) != 2 {
		t.Fatalf("jobs %s: %v", rr.Body.String(), err)
	}
	pub, err := release.DecodePublic(app.JobPublicKey())
	if err != nil {
		t.Fatal(err)
	}
	v := agentjob.Verifier{BCCKey: pub, NodeID: "ex-1"}
	actions := map[string]agentjob.Job{}
	for _, j := range jobs {
		if j.Signed == nil {
			t.Fatalf("job %s served unsigned", j.ID)
		}
		got, err := v.Verify(*j.Signed, now.Add(time.Minute))
		if err != nil {
			t.Fatalf("agent refuses job %s: %v", j.ID, err)
		}
		if got.JobID != j.ID {
			t.Fatalf("signed job id %s for record %s", got.JobID, j.ID)
		}
		actions[got.Params["version"]] = got
	}
	if actions["v1.2.0"].Action != agentjob.ActionUpdateBAFT || actions["v1.3.0"].Action != agentjob.ActionUpdateBAFT {
		t.Fatalf("unexpected signed actions %+v", actions)
	}
	if _, err := v.Verify(*jobs[0].Signed, now.Add(jobValidity+time.Second)); err == nil {
		t.Fatal("job still runnable after its validity")
	}
	if _, err := (agentjob.Verifier{BCCKey: pub, NodeID: "w-1"}).Verify(*jobs[0].Signed, now); err == nil {
		t.Fatal("another node accepted ex-1's job")
	}
}

func TestDeployNeedsAReleaseTag(t *testing.T) {
	for v, ok := range map[string]bool{"v1.2.0": true, "v1.2.0-rc.1": true, "1.2.0": false, "latest": false, "v1.2": false, "v1.2.0;rm": false} {
		if validVersion(v) != ok {
			t.Errorf("validVersion(%q) = %v", v, !ok)
		}
	}
}

func TestJobKeyIsOwnerOnlyAndStable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job-key")
	k1, created, err := LoadOrCreateJobKey(path)
	if err != nil || !created {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("job key mode %o", st.Mode().Perm())
	}
	k2, created, err := LoadOrCreateJobKey(path)
	if err != nil || created || !k1.Equal(k2) {
		t.Fatal("job key not reused")
	}
	os.Chmod(path, 0o644)
	if _, _, err := LoadOrCreateJobKey(path); err == nil {
		t.Fatal("group-readable job key accepted")
	}
}
