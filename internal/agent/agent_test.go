package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/agentjob"
	"github.com/zarkmakerburg/baft/internal/bcc"
	"github.com/zarkmakerburg/baft/internal/release"
)

const testCommit = "4545a784cb6c043ac1f97f88c810c422144d7823"

type fakeSystem struct {
	mu     sync.Mutex
	calls  []string
	active []string // successive is-active answers; last repeats
	doctor string
}

func (f *fakeSystem) Systemctl(_ context.Context, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.Join(args, " "))
	if args[0] == "is-active" {
		if len(f.active) == 0 {
			return "active", nil
		}
		s := f.active[0]
		if len(f.active) > 1 {
			f.active = f.active[1:]
		}
		return s, nil
	}
	return "", nil
}

func (f *fakeSystem) Run(_ context.Context, name string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, filepath.Base(name)+" "+strings.Join(args, " "))
	return f.doctor, nil
}

type rig struct {
	t       *testing.T
	bccApp  *bcc.Server
	bccURL  string
	sys     *fakeSystem
	binDir  string
	state   string
	relRoot ed25519.PrivateKey
	relKey  ed25519.PrivateKey
	cert    release.Envelope
	relDir  string // served at /releases/
	agent   *Agent
	jobKey  ed25519.PublicKey
}

func newRig(t *testing.T) *rig {
	t.Helper()
	dir := t.TempDir()
	r := &rig{t: t, sys: &fakeSystem{doctor: "OK all checks"}, binDir: filepath.Join(dir, "bin"), state: filepath.Join(dir, "release-state.json"), relDir: filepath.Join(dir, "releases")}

	store, err := bcc.OpenStore(filepath.Join(dir, "bcc", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertNode(bcc.Node{ID: "ex-1", Address: "127.0.0.1:1", Role: "foreign"}, "agent-token"); err != nil {
		t.Fatal(err)
	}
	if r.bccApp, err = bcc.NewServer(store, "admin"); err != nil {
		t.Fatal(err)
	}
	key, _, err := bcc.LoadOrCreateJobKey(filepath.Join(dir, "bcc", "job-key"))
	if err != nil {
		t.Fatal(err)
	}
	r.bccApp.ConfigureJobSigning(key)
	r.jobKey = key.Public().(ed25519.PublicKey)

	mux := http.NewServeMux()
	mux.Handle("/api/", r.bccApp.Handler())
	mux.Handle("/releases/", http.StripPrefix("/releases/", http.FileServer(http.Dir(r.relDir))))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	r.bccURL = srv.URL

	// Release signing: root certifies a release key; root signs revocations.
	rootPub, rootKey, _ := release.GenerateKey()
	relPub, relKey, _ := release.GenerateKey()
	r.relRoot, r.relKey = rootKey, relKey
	if r.cert, err = release.Certify(rootKey, relPub, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(r.relDir, 0o755)
	rev, _ := release.SignRevocations(rootKey, 1, nil, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
	release.WriteEnvelope(filepath.Join(r.relDir, "revocations.json"), rev)

	os.MkdirAll(r.binDir, 0o755)
	for _, n := range []string{"baft", "baft-pair"} {
		os.WriteFile(filepath.Join(r.binDir, n), []byte("old "+n), 0o755)
	}
	r.agent = r.newAgent(r.jobKey, rootPub, filepath.Join(dir, "agent"))
	return r
}

func (r *rig) newAgent(jobKey, root ed25519.PublicKey, stateDir string) *Agent {
	r.t.Helper()
	a, err := New(Config{
		BCCURL: r.bccURL, NodeID: "ex-1", Token: "agent-token", BCCJobKey: jobKey, StateDir: stateDir,
		Service: "baft", ReleaseRoot: root, ReleaseBaseURL: r.bccURL + "/releases",
		RevocationsURL: r.bccURL + "/releases/revocations.json", ReleaseState: r.state,
		BinDir: r.binDir, Arch: "amd64", SettleTime: 10 * time.Millisecond, System: r.sys,
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return a
}

// publish signs a release and serves it at /releases/<version>/.
func (r *rig) publish(version string) string {
	r.t.Helper()
	dir := filepath.Join(r.relDir, version)
	os.MkdirAll(dir, 0o755)
	for _, n := range []string{"baft-linux-amd64", "baft-pair-linux-amd64", "baft-linux-arm64"} {
		os.WriteFile(filepath.Join(dir, n), []byte(version+" "+n), 0o755)
	}
	if _, err := release.SignDir(release.SignInput{
		Dir: dir, Version: version, Commit: testCommit, CreatedAt: time.Now(),
		Provenance: release.Provenance{Builder: "test", GoVersion: "go"}, Key: r.relKey, Cert: r.cert,
	}); err != nil {
		r.t.Fatal(err)
	}
	return dir
}

func (r *rig) deploy(version string) {
	r.t.Helper()
	body, _ := json.Marshal(map[string]any{"node_ids": []string{"ex-1"}, "version": version})
	req := httptest.NewRequest(http.MethodPost, "/api/deploy", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer admin")
	rr := httptest.NewRecorder()
	r.bccApp.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		r.t.Fatalf("deploy = %d %s", rr.Code, rr.Body.String())
	}
}

func (r *rig) runOne() Result {
	r.t.Helper()
	res, err := r.agent.RunOnce(context.Background())
	if err != nil || len(res) != 1 {
		r.t.Fatalf("RunOnce = %+v, %v", res, err)
	}
	return res[0]
}

func (r *rig) bin(n string) string {
	b, _ := os.ReadFile(filepath.Join(r.binDir, n))
	return string(b)
}

func (r *rig) jobStatus() string {
	req := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
	req.Header.Set("Authorization", "Bearer admin")
	rr := httptest.NewRecorder()
	r.bccApp.Handler().ServeHTTP(rr, req)
	var jobs []bcc.Job
	json.Unmarshal(rr.Body.Bytes(), &jobs)
	if len(jobs) == 0 {
		return ""
	}
	return jobs[0].Status
}

func TestSignedUpdateInstallsVerifiedRelease(t *testing.T) {
	r := newRig(t)
	r.publish("v1.1.0")
	r.deploy("v1.1.0")
	res := r.runOne()
	if res.Status != "succeeded" || res.Action != agentjob.ActionUpdateBAFT {
		t.Fatalf("update result %+v", res)
	}
	if r.bin("baft") != "v1.1.0 baft-linux-amd64" || r.bin("baft-pair") != "v1.1.0 baft-pair-linux-amd64" {
		t.Fatalf("binaries not installed: %q %q", r.bin("baft"), r.bin("baft-pair"))
	}
	if r.bin("baft.prev") != "old baft" {
		t.Fatal("previous binary not kept")
	}
	st, err := release.ReadState(r.state)
	if err != nil || st.Version != "v1.1.0" {
		t.Fatalf("release state %+v %v", st, err)
	}
	if r.jobStatus() != "succeeded" {
		t.Fatalf("BCC job status %q", r.jobStatus())
	}
	if !strings.Contains(strings.Join(r.sys.calls, ";"), "restart baft") {
		t.Fatalf("service not restarted: %v", r.sys.calls)
	}
	// A restarted agent remembers the job it ran.
	again := r.newAgent(r.jobKey, r.agent.cfg.ReleaseRoot, r.agent.cfg.StateDir)
	if !again.isSeen(res.JobID) {
		t.Fatal("seen-job log not persisted")
	}
}

func TestFailedUpdateRollsBack(t *testing.T) {
	r := newRig(t)
	r.publish("v1.1.0")
	r.deploy("v1.1.0")
	r.sys.active = []string{"failed", "active"} // new binary fails, old one comes back
	res := r.runOne()
	if res.Status != "failed" || !strings.Contains(res.Detail, "rolled back") {
		t.Fatalf("result %+v", res)
	}
	if r.bin("baft") != "old baft" || r.bin("baft-pair") != "old baft-pair" {
		t.Fatalf("not rolled back: %q %q", r.bin("baft"), r.bin("baft-pair"))
	}
	if _, err := os.Stat(r.state); !os.IsNotExist(err) {
		t.Fatal("failed update was recorded in the release state")
	}
	if r.jobStatus() != "failed" {
		t.Fatalf("BCC job status %q", r.jobStatus())
	}
}

func TestTamperedOrDowngradedReleaseChangesNothing(t *testing.T) {
	r := newRig(t)
	dir := r.publish("v1.1.0")
	os.WriteFile(filepath.Join(dir, "baft-linux-amd64"), []byte("evil"), 0o755)
	r.deploy("v1.1.0")
	res := r.runOne()
	if res.Status != "failed" || !strings.Contains(res.Detail, "did not verify") {
		t.Fatalf("tampered release: %+v", res)
	}
	if r.bin("baft") != "old baft" || strings.Contains(strings.Join(r.sys.calls, ";"), "restart") {
		t.Fatal("tampered release touched the host")
	}

	// Installed v2.0.0 already: an older signed release is refused.
	release.WriteState(r.state, release.TrustState{SchemaVersion: 1, Version: "v2.0.0", Commit: testCommit, RevocationSequence: 1})
	r.publish("v1.2.0")
	r.deploy("v1.2.0")
	res = r.runOne()
	if res.Status != "failed" || !strings.Contains(res.Detail, "downgrade refused") {
		t.Fatalf("downgrade: %+v", res)
	}
}

func TestJobsFromAnUnpinnedKeyAreRefused(t *testing.T) {
	r := newRig(t)
	other, _, _ := release.GenerateKey()
	r.agent = r.newAgent(other, r.agent.cfg.ReleaseRoot, t.TempDir())
	r.publish("v1.1.0")
	r.deploy("v1.1.0")
	res := r.runOne()
	if res.Status != "refused" || !strings.Contains(res.Detail, "job signature") {
		t.Fatalf("result %+v", res)
	}
	if r.bin("baft") != "old baft" || len(r.sys.calls) != 0 {
		t.Fatalf("refused job touched the host: %v", r.sys.calls)
	}
}

func TestRestartReloadAndHealthActions(t *testing.T) {
	r := newRig(t)
	for _, action := range []string{agentjob.ActionRestart, agentjob.ActionReload, agentjob.ActionHealth} {
		out, err := r.agent.execute(context.Background(), agentjob.Job{Action: action})
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		if out == "" {
			t.Fatalf("%s: empty result", action)
		}
	}
	calls := strings.Join(r.sys.calls, ";")
	for _, want := range []string{"restart baft", "reload baft", "is-active baft", "baft doctor --service baft"} {
		if !strings.Contains(calls, want) {
			t.Errorf("missing %q in %s", want, calls)
		}
	}
	r.sys.active = []string{"failed"}
	if _, err := r.agent.execute(context.Background(), agentjob.Job{Action: agentjob.ActionRestart}); err == nil {
		t.Fatal("restart reported success with the service down")
	}
}
