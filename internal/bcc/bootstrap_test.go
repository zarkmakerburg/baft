package bcc

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zarkmakerburg/baft/internal/sshboot"
)

type bootRig struct {
	app  *Server
	got  sshboot.Request
	runs int
	fail error
	out  string
}

func newBootRig(t *testing.T) *bootRig {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	app, err := NewServer(store, "admin")
	if err != nil {
		t.Fatal(err)
	}
	key, _, err := LoadOrCreateJobKey(filepath.Join(t.TempDir(), "job-key"))
	if err != nil {
		t.Fatal(err)
	}
	app.ConfigureJobSigning(key)
	r := &bootRig{app: app}
	app.ConfigureBootstrap(BootstrapConfig{InstallScript: []byte("#!/bin/bash\n"), PublicURL: "https://bcc.example.com"})
	app.boot.scan = func(_ context.Context, tg sshboot.Target) (string, string, error) {
		return "SHA256:scanned" + tg.Host, "ssh-ed25519", nil
	}
	app.boot.run = func(_ context.Context, req sshboot.Request) (sshboot.Result, error) {
		r.runs++
		r.got = req
		return sshboot.Result{Output: r.out}, r.fail
	}
	return r
}

func (r *bootRig) agentOK(token string) bool {
	_, err := r.app.store.PullJobs("ex-1", token)
	return err == nil
}

func (r *bootRig) post(path string, body any, remote string) *httptest.ResponseRecorder {
	req := authReq(http.MethodPost, path, "admin", body)
	req.RemoteAddr = remote
	rr := httptest.NewRecorder()
	r.app.Handler().ServeHTTP(rr, req)
	return rr
}

var goodBody = map[string]any{
	"node_id": "ex-1", "host": "203.0.113.7", "user": "ubuntu", "password": "hunter2-hunter2",
	"host_key_fingerprint": "SHA256:abc",
}

func TestBootstrapInstallsAgentAndRegistersNode(t *testing.T) {
	r := newBootRig(t)
	r.out = "baft-agent enrolled"
	rr := r.post("/api/bootstrap", goodBody, "127.0.0.1:5555")
	if rr.Code != http.StatusCreated {
		t.Fatalf("bootstrap = %d %s", rr.Code, rr.Body.String())
	}
	g := r.got
	if g.Host != "203.0.113.7" || g.Port != 22 || g.User != "ubuntu" || g.HostKeyFingerprint != "SHA256:abc" ||
		g.BCCURL != "https://bcc.example.com" || g.NodeID != "ex-1" || g.BCCJobKey != r.app.JobPublicKey() || len(g.AgentToken) < 40 {
		t.Fatalf("request to sshboot: %+v", g)
	}
	// The token handed to the server is the one BCC will accept from the agent.
	if !r.agentOK(g.AgentToken) {
		t.Fatal("agent token not registered")
	}
	nodes := r.app.store.ListNodes()
	if len(nodes) != 1 || nodes[0].ID != "ex-1" || nodes[0].Address != "203.0.113.7:22" || nodes[0].Role != "foreign" {
		t.Fatalf("inventory: %+v", nodes)
	}
	if strings.Contains(rr.Body.String(), g.AgentToken) || strings.Contains(rr.Body.String(), "hunter2") {
		t.Fatal("response leaks a secret")
	}
}

func TestBootstrapNeverStoresOrAuditsCredentials(t *testing.T) {
	r := newBootRig(t)
	body := map[string]any{"node_id": "ex-1", "host": "203.0.113.7", "private_key": "-----BEGIN PRIVATE KEY-----SECRETKEYBODY", "passphrase": "pp-secret-value", "host_key_fingerprint": "SHA256:abc"}
	r.fail = errors.New("install failed: exit 3")
	if rr := r.post("/api/bootstrap", body, "127.0.0.1:1"); rr.Code != http.StatusBadGateway {
		t.Fatalf("failed install = %d %s", rr.Code, rr.Body.String())
	}
	entries, _ := r.app.audit.List(0)
	var found bool
	for _, e := range entries {
		if e.Action != "node.bootstrap" {
			continue
		}
		found = true
		if e.Outcome != "failure" || e.Details["auth"] != "private_key" || e.Details["host"] != "203.0.113.7" {
			t.Fatalf("audit entry %+v", e)
		}
	}
	if !found {
		t.Fatal("bootstrap not audited")
	}
	for _, f := range []string{r.app.audit.path} {
		b, _ := os.ReadFile(f)
		for _, secret := range []string{"SECRETKEYBODY", "pp-secret-value", r.got.AgentToken} {
			if strings.Contains(string(b), secret) {
				t.Fatalf("%s contains a secret", f)
			}
		}
	}
	b, _ := os.ReadFile(r.app.store.path)
	if strings.Contains(string(b), "SECRETKEYBODY") || strings.Contains(string(b), r.got.AgentToken) {
		t.Fatal("state file contains a secret")
	}
}

func TestBootstrapRefusesClearTextCredentialsAndMissingPieces(t *testing.T) {
	r := newBootRig(t)
	if rr := r.post("/api/bootstrap", goodBody, "198.51.100.9:4000"); rr.Code != http.StatusBadRequest || r.runs != 0 {
		t.Fatalf("plain HTTP from a remote peer = %d", rr.Code)
	}
	// Unauthenticated callers are turned away before anything else.
	req := httptest.NewRequest(http.MethodPost, "/api/bootstrap", strings.NewReader("{}"))
	req.RemoteAddr = "127.0.0.1:1"
	rr := httptest.NewRecorder()
	r.app.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("no token = %d", rr.Code)
	}
	// No pinned fingerprint: sshboot refuses; the inventory entry is still created for a retry.
	r.fail = errors.New("a confirmed SHA256 host key fingerprint is required")
	bad := map[string]any{"node_id": "ex-2", "host": "203.0.113.8", "password": "x"}
	if rr := r.post("/api/bootstrap", bad, "127.0.0.1:1"); rr.Code != http.StatusBadGateway {
		t.Fatalf("missing fingerprint = %d", rr.Code)
	}
	// Without job signing the agent could not verify anything.
	r.app.jobKey = nil
	if rr := r.post("/api/bootstrap", goodBody, "127.0.0.1:1"); rr.Code != http.StatusConflict {
		t.Fatalf("no job key = %d", rr.Code)
	}
	// Not configured at all.
	plain := newBootRig(t)
	plain.app.boot = nil
	if rr := plain.post("/api/bootstrap", goodBody, "127.0.0.1:1"); rr.Code != http.StatusNotImplemented {
		t.Fatalf("unconfigured = %d", rr.Code)
	}
}

func TestHostKeyScanReturnsFingerprintForConfirmation(t *testing.T) {
	r := newBootRig(t)
	rr := r.post("/api/bootstrap/hostkey", map[string]any{"host": "203.0.113.7"}, "192.0.2.1:1")
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "SHA256:scanned203.0.113.7") || !strings.Contains(rr.Body.String(), `"port":"22"`) {
		t.Fatalf("hostkey = %d %s", rr.Code, rr.Body.String())
	}
}

func TestRetryIssuesANewAgentToken(t *testing.T) {
	r := newBootRig(t)
	r.post("/api/bootstrap", goodBody, "127.0.0.1:1")
	first := r.got.AgentToken
	r.post("/api/bootstrap", goodBody, "127.0.0.1:1")
	second := r.got.AgentToken
	if first == second {
		t.Fatal("token reused across bootstraps")
	}
	if !r.agentOK(second) {
		t.Fatal("new token rejected")
	}
	if r.agentOK(first) {
		t.Fatal("old token still valid after a re-bootstrap")
	}
}
