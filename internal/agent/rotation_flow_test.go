package agent

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/bcc"
	"github.com/zarkmakerburg/baft/internal/config"
)

// exListener serves the certificate the fake EX service loaded at its last
// restart, like the real listener.
type exListener struct {
	mu   sync.Mutex
	cert *tls.Certificate
}

type rotFlow struct {
	*flow
	exl       *exListener
	irMu      sync.Mutex
	irRunning *x509.CertPool
	tunnel    bcc.Tunnel
}

func newRotFlow(t *testing.T) *rotFlow {
	t.Helper()
	rf := &rotFlow{exl: &exListener{}}
	rf.flow = newFlowWith(t, func(t *testing.T, port int) {
		l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { l.Close() })
		cfg := &tls.Config{MinVersion: tls.VersionTLS13, NextProtos: []string{"h2"}, GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			rf.exl.mu.Lock()
			defer rf.exl.mu.Unlock()
			if rf.exl.cert == nil {
				return nil, errors.New("not running")
			}
			return rf.exl.cert, nil
		}}
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				go func() { tc := tls.Server(c, cfg); _ = tc.Handshake(); tc.Close() }()
			}
		}()
	})
	rf.ex.sys.onRestart = func() error {
		c, err := tls.LoadX509KeyPair(filepath.Join(rf.ex.etc, "pki", "server.pem"), filepath.Join(rf.ex.etc, "pki", "server.key"))
		if err != nil {
			return err
		}
		rf.exl.mu.Lock()
		rf.exl.cert = &c
		rf.exl.mu.Unlock()
		return nil
	}
	rf.ir.sys.onRestart = func() error {
		pool, err := rf.poolFile(rf.irCAFile())
		if err != nil {
			return err
		}
		rf.irMu.Lock()
		rf.irRunning = pool
		rf.irMu.Unlock()
		return nil
	}
	tn := rf.create(rf.plan())
	rf.tunnel = rf.run(tn.ID)
	if rf.tunnel.Phase != bcc.TunnelActive {
		t.Fatalf("tunnel %s: %s", rf.tunnel.Phase, rf.tunnel.Error)
	}
	rf.assertTrusted("tunnel built")
	return rf
}

// irCAFile is the CA file the IR's live config pins.
func (rf *rotFlow) irCAFile() string {
	cfg, err := config.LoadFile(filepath.Join(rf.ir.etc, "baft.yaml"))
	if err != nil {
		return ""
	}
	return cfg.TLS.CAFile
}

func (rf *rotFlow) poolFile(path string) (*x509.CertPool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(b) {
		return nil, errors.New("no certificates")
	}
	return pool, nil
}

func (rf *rotFlow) caCount() int {
	b, _ := os.ReadFile(rf.irCAFile())
	return strings.Count(string(b), "BEGIN CERTIFICATE")
}

func (rf *rotFlow) servedLeaf() string {
	rf.exl.mu.Lock()
	defer rf.exl.mu.Unlock()
	if rf.exl.cert == nil {
		return ""
	}
	return sha256Hex(rf.exl.cert.Certificate[0])
}

// assertTrusted: the running IR accepts the running EX, and the files both
// would boot from after a reboot agree too.
func (rf *rotFlow) assertTrusted(when string) {
	rf.t.Helper()
	pool, err := rf.poolFile(rf.irCAFile())
	if err != nil {
		rf.t.Fatalf("%s: IR CA file: %v", when, err)
	}
	pki := filepath.Join(rf.ex.etc, "pki")
	pair, err := tls.LoadX509KeyPair(filepath.Join(pki, "server.pem"), filepath.Join(pki, "server.key"))
	if err != nil {
		rf.t.Fatalf("%s: EX certificate set inconsistent: %v", when, err)
	}
	leaf, _ := x509.ParseCertificate(pair.Certificate[0])
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "127.0.0.1"}); err != nil {
		rf.t.Fatalf("%s: after a reboot the IR would reject the EX: %v", when, err)
	}
	rf.irMu.Lock()
	running := rf.irRunning
	rf.irMu.Unlock()
	d := tls.Dialer{Config: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: running, ServerName: "127.0.0.1"}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := d.DialContext(ctx, "tcp", "127.0.0.1:"+strconv.Itoa(rf.exPort))
	if err != nil {
		rf.t.Fatalf("%s: the running IR rejects the running EX: %v", when, err)
	}
	c.Close()
}

func (rf *rotFlow) start(body map[string]any) bcc.CertRotation {
	rf.t.Helper()
	if body == nil {
		body = map[string]any{}
	}
	body["tunnel_id"] = rf.tunnel.ID
	code, b := rf.api("POST", "/api/tunnels/rotate-cert", body)
	if code != http.StatusAccepted {
		rf.t.Fatalf("start rotation = %d %s", code, b)
	}
	var r bcc.CertRotation
	json.Unmarshal(b, &r)
	return r
}

func (rf *rotFlow) rot(id string) bcc.CertRotation {
	rf.t.Helper()
	_, b := rf.api("GET", "/api/cert-rotations?id="+id, nil)
	var r bcc.CertRotation
	json.Unmarshal(b, &r)
	return r
}

// step lets the given agents run once and checks the invariant after each.
func (rf *rotFlow) step(id string, agents ...*flowNode) bcc.CertRotation {
	rf.t.Helper()
	for _, n := range agents {
		if _, err := n.agent.RunOnce(context.Background()); err != nil {
			rf.t.Fatalf("%s RunOnce: %v", n.id, err)
		}
		rf.assertTrusted(n.id + " ran a step of " + rf.rot(id).Phase)
	}
	return rf.rot(id)
}

func (rf *rotFlow) runRot(id string) bcc.CertRotation {
	rf.t.Helper()
	for i := 0; i < 60; i++ {
		r := rf.step(id, rf.ex, rf.ir)
		switch r.Phase {
		case bcc.CertRotComplete, bcc.CertRotRolledBack, bcc.CertRotRollbackFailed, bcc.CertRotRetireFailed:
			return r
		}
	}
	rf.t.Fatalf("rotation did not settle: %+v", rf.rot(id))
	return bcc.CertRotation{}
}

// runUntil steps both agents until the rotation is in phase.
func (rf *rotFlow) runUntil(id, phase string) {
	rf.t.Helper()
	for i := 0; i < 60; i++ {
		if rf.rot(id).Phase == phase {
			return
		}
		for _, n := range []*flowNode{rf.ex, rf.ir} {
			if rf.rot(id).Phase == phase {
				return
			}
			rf.step(id, n)
		}
	}
	rf.t.Fatalf("rotation never reached %s: %+v", phase, rf.rot(id))
}

func (rf *rotFlow) audit() string {
	b, _ := os.ReadFile(rf.store.Path() + ".audit.jsonl")
	return string(b)
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestBCCRotatesATunnelCertificateEndToEnd(t *testing.T) {
	rf := newRotFlow(t)
	oldLeaf := rf.servedLeaf()
	r := rf.start(nil)
	if r.Phase != bcc.CertRotPreparing || r.Epoch != 1 || !strings.HasPrefix(r.ID, "rot-") {
		t.Fatalf("new rotation: %+v", r)
	}
	// One change at a time: no tunnel change and no second rotation.
	if code, _ := rf.api("POST", "/api/tunnels", rf.plan()); code == http.StatusAccepted {
		t.Fatal("a tunnel change started during a rotation")
	}
	if code, _ := rf.api("POST", "/api/tunnels/rotate-cert", map[string]any{"tunnel_id": rf.tunnel.ID}); code == http.StatusAccepted {
		t.Fatal("a second rotation started")
	}
	sawTwo := false
	for i := 0; i < 60; i++ {
		cur := rf.step(r.ID, rf.ex, rf.ir)
		if rf.caCount() == 2 {
			sawTwo = true
		}
		if cur.Phase == bcc.CertRotComplete {
			break
		}
		if terminal := cur.Phase == bcc.CertRotRolledBack || cur.Phase == bcc.CertRotRollbackFailed || cur.Phase == bcc.CertRotRetireFailed; terminal {
			t.Fatalf("rotation ended %s: %s", cur.Phase, cur.Error)
		}
	}
	done := rf.rot(r.ID)
	if done.Phase != bcc.CertRotComplete || done.Error != "" {
		t.Fatalf("rotation ended %s: %s", done.Phase, done.Error)
	}
	if !sawTwo || rf.caCount() != 1 {
		t.Fatalf("trust window: saw old+new %v, final CA count %d", sawTwo, rf.caCount())
	}
	if rf.servedLeaf() != done.CertSHA256 || done.CertSHA256 == oldLeaf || done.OldCertSHA256 != oldLeaf {
		t.Fatal("the EX does not serve the new certificate")
	}
	if tn := rf.tunnel; true {
		got := rf.flow.tunnel(tn.ID)
		if got.CertEpoch != 1 || got.CertSHA256 != done.CertSHA256 || got.Phase != bcc.TunnelActive {
			t.Fatalf("tunnel record: epoch %d cert %s phase %s", got.CertEpoch, got.CertSHA256, got.Phase)
		}
	}
	// Per-node evidence for every step, each proved by the node itself.
	steps := map[string]string{}
	for _, e := range done.Evidence {
		if !e.OK {
			t.Errorf("evidence %s on %s not OK: %v", e.Step, e.Node, e.Problems)
		}
		steps[e.Step] = e.Node
	}
	want := map[string]string{
		bcc.CertRotPreparing: "ex-1", bcc.CertRotDistributing: "ir-1", bcc.CertRotVerifyingIR: "ir-1", bcc.CertRotVerifyingEX: "ex-1",
		bcc.CertRotActivating: "ex-1", bcc.CertRotConfirming: "ir-1", bcc.CertRotRetiringIR: "ir-1", bcc.CertRotRetiringEX: "ex-1",
	}
	for step, node := range want {
		if steps[step] != node {
			t.Errorf("no evidence for %s from %s: %v", step, node, steps)
		}
	}
	audit := rf.audit()
	for _, a := range []string{"cert.rotation.start", "cert.rotation.activated", "cert.rotation.complete"} {
		if !strings.Contains(audit, `"`+a+`"`) {
			t.Errorf("audit lacks %s", a)
		}
	}
	// No private key ever reached BCC.
	state, _ := os.ReadFile(rf.store.Path())
	_, jobs := rf.api("GET", "/api/jobs", nil)
	for name, b := range map[string][]byte{"state": state, "jobs": jobs, "audit": []byte(audit)} {
		if strings.Contains(string(b), "PRIVATE KEY") {
			t.Fatalf("%s holds a private key", name)
		}
	}
	// The next rotation works and gets the next epoch.
	r2 := rf.start(nil)
	if r2.Epoch != 2 {
		t.Fatalf("second rotation epoch %d", r2.Epoch)
	}
	if d := rf.runRot(r2.ID); d.Phase != bcc.CertRotComplete {
		t.Fatalf("second rotation %s: %s", d.Phase, d.Error)
	}
}

// A disconnect at each step before RETIRE rolls back to the working old
// certificate; at RETIRE the step is retried and the rotation completes.
func TestRotationDisconnectAtEveryStep(t *testing.T) {
	type tc struct {
		phase    string
		complete bool
	}
	for _, c := range []tc{
		{bcc.CertRotPreparing, false},
		{bcc.CertRotDistributing, false},
		{bcc.CertRotVerifyingIR, false},
		{bcc.CertRotVerifyingEX, false},
		{bcc.CertRotActivating, false},
		{bcc.CertRotConfirming, false},
		{bcc.CertRotRetiringIR, true},
		{bcc.CertRotRetiringEX, true},
	} {
		t.Run(c.phase, func(t *testing.T) {
			rf := newRotFlow(t)
			oldLeaf := rf.servedLeaf()
			oldCA, _ := os.ReadFile(rf.irCAFile())
			r := rf.start(nil)
			rf.runUntil(r.ID, c.phase)
			// The node that owns this step is unreachable: its job never
			// leaves the queue. BCC gives up after the step timeout.
			late := time.Now().Add(25 * time.Minute)
			if _, err := rf.store.AdvanceTunnels(late); err != nil {
				t.Fatal(err)
			}
			rf.assertTrusted("timeout at " + c.phase)
			got := rf.rot(r.ID)
			if c.complete {
				if !strings.HasPrefix(got.Phase, "retiring") {
					t.Fatalf("after a retire timeout: %s (%s)", got.Phase, got.Error)
				}
			} else if got.Phase != bcc.CertRotRollingBackEX && got.Phase != bcc.CertRotRollingBackIR && got.Phase != bcc.CertRotRolledBack {
				t.Fatalf("after a timeout at %s: %s (%s)", c.phase, got.Phase, got.Error)
			}
			done := rf.runRot(r.ID)
			if c.complete {
				if done.Phase != bcc.CertRotComplete {
					t.Fatalf("rotation %s: %s", done.Phase, done.Error)
				}
				return
			}
			if done.Phase != bcc.CertRotRolledBack {
				t.Fatalf("rotation %s: %s", done.Phase, done.Error)
			}
			if rf.servedLeaf() != oldLeaf {
				t.Fatal("the old certificate is not served after the rollback")
			}
			if now, _ := os.ReadFile(rf.irCAFile()); string(now) != string(oldCA) {
				t.Fatal("the IR's trust is not the original after the rollback")
			}
			if !strings.Contains(rf.audit(), `"cert.rotation.rolled_back"`) {
				t.Fatal("audit lacks cert.rotation.rolled_back")
			}
			// Nothing is left behind: a new rotation starts and completes.
			if d := rf.runRot(rf.start(nil).ID); d.Phase != bcc.CertRotComplete {
				t.Fatalf("rotation after rollback %s: %s", d.Phase, d.Error)
			}
		})
	}
}

// A crash on the EX in the middle of ACTIVATE (after the swap, before the
// restart) fails the step; BCC rolls the EX back first, then the IR.
func TestRotationCrashDuringActivateRollsBackSafely(t *testing.T) {
	rf := newRotFlow(t)
	oldLeaf := rf.servedLeaf()
	r := rf.start(nil)
	rf.runUntil(r.ID, bcc.CertRotActivating)
	rf.ex.tn.Fault = func(p string) error {
		if p == "activate:after-swap" {
			rf.ex.tn.Fault = nil
			return errors.New("injected crash")
		}
		return nil
	}
	// The EX reboots at the crash: it starts on the swapped (new) set.
	rf.step(r.ID, rf.ex)
	if _, err := rf.ex.sys.Systemctl(context.Background(), "restart", "baft"); err != nil {
		t.Fatal(err)
	}
	rf.assertTrusted("EX rebooted mid-activation")
	done := rf.runRot(r.ID)
	if done.Phase != bcc.CertRotRolledBack || rf.servedLeaf() != oldLeaf || rf.caCount() != 1 {
		t.Fatalf("rotation %s (%s), CAs %d", done.Phase, done.Error, rf.caCount())
	}
}

// BCC restarts in the middle of a rotation: the persisted state resumes.
func TestRotationSurvivesBCCRestart(t *testing.T) {
	rf := newRotFlow(t)
	r := rf.start(nil)
	rf.runUntil(r.ID, bcc.CertRotConfirming)
	store, err := bcc.OpenStore(rf.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	app, err := bcc.NewServer(store, "admin")
	if err != nil {
		t.Fatal(err)
	}
	app.ConfigureJobSigning(rf.jobKey)
	rf.store, rf.app, rf.h = store, app, app.Handler()
	if got := rf.rot(r.ID); got.Phase != bcc.CertRotConfirming {
		t.Fatalf("after restart: %s", got.Phase)
	}
	if d := rf.runRot(r.ID); d.Phase != bcc.CertRotComplete {
		t.Fatalf("rotation %s: %s", d.Phase, d.Error)
	}
}

func TestRotationCancelAndBounds(t *testing.T) {
	rf := newRotFlow(t)
	oldLeaf := rf.servedLeaf()
	// Bad requests change nothing.
	for _, body := range []map[string]any{{"hold_seconds": -1}, {"overlap_hours": 1000}, {"hold_seconds": 7200, "overlap_hours": 1}} {
		body["tunnel_id"] = rf.tunnel.ID
		if code, _ := rf.api("POST", "/api/tunnels/rotate-cert", body); code != http.StatusBadRequest {
			t.Fatalf("bad request %v accepted: %d", body, code)
		}
	}
	if code, _ := rf.api("POST", "/api/tunnels/rotate-cert", map[string]any{"tunnel_id": "tun-nope"}); code != http.StatusBadRequest {
		t.Fatal("rotation of an unknown tunnel")
	}
	// Cancel while holding after CONFIRM: the EX serves the new certificate
	// and the IR trusts both; the rollback goes EX first.
	r := rf.start(map[string]any{"hold_seconds": 3600})
	rf.runUntil(r.ID, bcc.CertRotHolding)
	if rf.servedLeaf() == oldLeaf {
		t.Fatal("not serving the new certificate while holding")
	}
	code, b := rf.api("POST", "/api/cert-rotations/cancel", map[string]any{"id": r.ID, "reason": "test"})
	if code != http.StatusOK {
		t.Fatalf("cancel = %d %s", code, b)
	}
	done := rf.runRot(r.ID)
	if done.Phase != bcc.CertRotRolledBack || rf.servedLeaf() != oldLeaf || rf.caCount() != 1 {
		t.Fatalf("cancel ended %s (%s)", done.Phase, done.Error)
	}
	var order []string
	for _, e := range done.Evidence {
		if strings.HasPrefix(e.Step, "rolling_back") {
			order = append(order, e.Node)
		}
	}
	if strings.Join(order, ",") != "ex-1,ir-1" {
		t.Fatalf("rollback order %v, want EX then IR", order)
	}
	// Once RETIRE started there is nothing to cancel.
	r2 := rf.start(nil)
	rf.runUntil(r2.ID, bcc.CertRotRetiringIR)
	if code, _ := rf.api("POST", "/api/cert-rotations/cancel", map[string]any{"id": r2.ID}); code != http.StatusBadRequest {
		t.Fatal("cancel accepted while retiring")
	}
	if d := rf.runRot(r2.ID); d.Phase != bcc.CertRotComplete {
		t.Fatalf("rotation %s: %s", d.Phase, d.Error)
	}
	// Admin only.
	for _, path := range []string{"/api/tunnels/rotate-cert", "/api/cert-rotations/cancel", "/api/cert-rotations"} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, strings.NewReader(`{"tunnel_id":"`+rf.tunnel.ID+`"}`))
		req.Header.Set("Authorization", "Bearer wrong")
		rf.h.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized && rr.Code != http.StatusForbidden && rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s without admin = %d", path, rr.Code)
		}
	}
}
