package tunnelnode

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// tlsService plays the EX's listener: it serves the certificate it loaded at
// its last (fake) restart, like the real service.
type tlsService struct {
	mu   sync.Mutex
	cert *tls.Certificate
}

func serveTLS(t *testing.T, port int) *tlsService {
	t.Helper()
	s := &tlsService{}
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	cfg := &tls.Config{MinVersion: tls.VersionTLS13, NextProtos: []string{"h2"}, GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.cert == nil {
			return nil, errors.New("service not running")
		}
		return s.cert, nil
	}}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				tc := tls.Server(c, cfg)
				_ = tc.Handshake()
				tc.Close()
			}()
		}
	}()
	return s
}

type rotRig struct {
	*pair
	svc *tlsService
	// irRunning is the trust the IR service loaded at its last restart.
	irMu      sync.Mutex
	irRunning *x509.CertPool
	failEX    int // fail the next n EX restarts
	epoch     int
}

func newRotRig(t *testing.T) *rotRig {
	t.Helper()
	r := &rotRig{pair: newPair(t), epoch: 1}
	r.svc = serveTLS(t, r.exPort)
	listen(t, mustPort(r.irListen))
	r.ex.host.onRestart = func() error {
		if r.failEX > 0 {
			r.failEX--
			return errors.New("injected start failure")
		}
		c, err := tls.LoadX509KeyPair(filepath.Join(r.ex.pkiDir(), "server.pem"), filepath.Join(r.ex.pkiDir(), "server.key"))
		if err != nil {
			return err
		}
		r.svc.mu.Lock()
		r.svc.cert = &c
		r.svc.mu.Unlock()
		return nil
	}
	r.ir.host.onRestart = func() error {
		b, err := os.ReadFile(r.irCAFile())
		if err != nil {
			return err
		}
		cs, err := parseCerts(b)
		if err != nil {
			return err
		}
		r.irMu.Lock()
		r.irRunning = poolOf(cs)
		r.irMu.Unlock()
		return nil
	}
	r.build(t, "t1")
	ctx := context.Background()
	for _, n := range []*node{r.ex, r.ir} {
		if _, err := n.Finalize(ctx, "t1"); err != nil {
			t.Fatal(err)
		}
	}
	r.assertTrusted(t, "after the tunnel was built")
	return r
}

func (r *rotRig) irCAFile() string { return filepath.Join(r.ir.stage("t1"), "peer-ca.pem") }

// assertTrusted is the invariant: the IR can verify the EX, both for the
// running services and for the files a reboot would load.
func (r *rotRig) assertTrusted(t *testing.T, when string) {
	t.Helper()
	// Files on disk (what both sides boot with).
	ca, err := os.ReadFile(r.irCAFile())
	if err != nil {
		t.Fatalf("%s: IR CA file: %v", when, err)
	}
	cs, err := parseCerts(ca)
	if err != nil {
		t.Fatalf("%s: IR CA file: %v", when, err)
	}
	pki := r.ex.pkiDir()
	pair, err := tls.LoadX509KeyPair(filepath.Join(pki, "server.pem"), filepath.Join(pki, "server.key"))
	if err != nil {
		t.Fatalf("%s: the EX's live certificate set is not consistent: %v", when, err)
	}
	leaf, _ := x509.ParseCertificate(pair.Certificate[0])
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: poolOf(cs), DNSName: "127.0.0.1"}); err != nil {
		t.Fatalf("%s: after a reboot the IR would not accept the EX: %v", when, err)
	}
	// Running services.
	r.irMu.Lock()
	running := r.irRunning
	r.irMu.Unlock()
	if running == nil {
		t.Fatalf("%s: IR service never started", when)
	}
	if _, err := probeTLS(context.Background(), net.JoinHostPort("127.0.0.1", strconv.Itoa(r.exPort)), "127.0.0.1", running); err != nil {
		t.Fatalf("%s: the running IR does not accept the running EX: %v", when, err)
	}
}

func (r *rotRig) rid() string { return "rot-" + strconv.Itoa(r.epoch) }

func (r *rotRig) prepare(t *testing.T) RotationPlan {
	t.Helper()
	p, err := r.ex.RotatePrepareEX(context.Background(), r.rid(), "t1", r.epoch)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	return p
}

func der(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (r *rotRig) trust(t *testing.T, p RotationPlan) {
	t.Helper()
	if _, err := r.ir.RotateTrustIR(context.Background(), r.rid(), "t1", r.epoch, der(t, p.CADER), p.CASHA256); err != nil {
		t.Fatalf("trust: %v", err)
	}
}

func (r *rotRig) verify(t *testing.T, p RotationPlan) {
	t.Helper()
	ctx := context.Background()
	if _, err := r.ir.RotateVerifyIR(ctx, r.rid(), der(t, p.CertDER), p.CertSHA256); err != nil {
		t.Fatalf("verify IR: %v", err)
	}
	if _, err := r.ex.RotateVerifyEX(ctx, r.rid()); err != nil {
		t.Fatalf("verify EX: %v", err)
	}
}

func (r *rotRig) activate(t *testing.T) {
	t.Helper()
	if _, err := r.ex.RotateActivateEX(context.Background(), r.rid()); err != nil {
		t.Fatalf("activate: %v", err)
	}
}

func (r *rotRig) confirm(t *testing.T, p RotationPlan) {
	t.Helper()
	if _, err := r.ir.RotateConfirmIR(context.Background(), r.rid(), p.CertSHA256); err != nil {
		t.Fatalf("confirm: %v", err)
	}
}

func (r *rotRig) retire(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if _, err := r.ir.RotateRetireIR(ctx, r.rid()); err != nil {
		t.Fatalf("retire IR: %v", err)
	}
	if _, err := r.ex.RotateRetireEX(ctx, r.rid()); err != nil {
		t.Fatalf("retire EX: %v", err)
	}
}

// rollback runs the safe order: EX first, then IR.
func (r *rotRig) rollback(t *testing.T, exNeverActivated bool) {
	t.Helper()
	ctx := context.Background()
	if _, err := r.ex.RotateRollback(ctx, r.rid(), false); err != nil {
		t.Fatalf("rollback EX: %v", err)
	}
	r.assertTrusted(t, "after the EX rolled back")
	if _, err := r.ir.RotateRollback(ctx, r.rid(), exNeverActivated); err != nil {
		t.Fatalf("rollback IR: %v", err)
	}
	r.assertTrusted(t, "after the IR rolled back")
}

func (r *rotRig) servedLeaf(t *testing.T) string {
	t.Helper()
	r.svc.mu.Lock()
	defer r.svc.mu.Unlock()
	return shaHex(r.svc.cert.Certificate[0])
}

func (r *rotRig) crashAt(n *node, point string) {
	n.Fault = func(p string) error {
		if p == point {
			n.Fault = nil
			return fmt.Errorf("injected crash at %s", p)
		}
		return nil
	}
}

func (r *rotRig) assertBackToOld(t *testing.T, oldLeaf, oldCAFile string) {
	t.Helper()
	if got := r.servedLeaf(t); got != oldLeaf {
		t.Fatalf("EX serves %s, want the old certificate %s", got, oldLeaf)
	}
	b, _ := os.ReadFile(r.irCAFile())
	if shaHex(b) != oldCAFile {
		t.Fatal("IR CA file is not the original one")
	}
	for _, n := range []*node{r.ex, r.ir} {
		if id := n.rotationInProgress(); id != "" {
			t.Fatalf("rotation %s still in progress", id)
		}
	}
	if m, _ := filepath.Glob(r.ex.pkiDir() + ".*"); len(m) != 0 {
		t.Fatalf("leftover certificate sets: %v", m)
	}
}

func (r *rotRig) snapshot(t *testing.T) (leaf, caFile string) {
	b, _ := os.ReadFile(r.irCAFile())
	return r.servedLeaf(t), shaHex(b)
}

func TestRotationFullTransactionKeepsTrustAtEveryStep(t *testing.T) {
	r := newRotRig(t)
	ctx := context.Background()
	oldLeaf, oldCA := r.snapshot(t)
	r.svc.mu.Lock()
	oldCert, _ := x509.ParseCertificate(r.svc.cert.Certificate[0])
	r.svc.mu.Unlock()
	p := r.prepare(t)
	if p.OldCertSHA256 != oldLeaf || p.CertSHA256 == oldLeaf || p.Host != "127.0.0.1" {
		t.Fatalf("plan: %+v (old %s)", p, oldLeaf)
	}
	r.assertTrusted(t, "after PREPARE")
	if r.servedLeaf(t) != oldLeaf {
		t.Fatal("PREPARE changed what the EX serves")
	}
	r.trust(t, p)
	r.assertTrusted(t, "after DISTRIBUTE")
	b, _ := os.ReadFile(r.irCAFile())
	if cs, _ := parseCerts(b); len(cs) != 2 {
		t.Fatalf("IR trusts %d CAs during the window, want old + new", len(cs))
	}
	r.verify(t, p)
	r.assertTrusted(t, "after VERIFY")
	r.activate(t)
	r.assertTrusted(t, "after ACTIVATE")
	if r.servedLeaf(t) != p.CertSHA256 {
		t.Fatal("EX does not serve the new certificate after ACTIVATE")
	}
	r.confirm(t, p)
	r.assertTrusted(t, "after CONFIRM")
	if _, err := r.ir.RotateRetireIR(ctx, r.rid()); err != nil {
		t.Fatal(err)
	}
	r.assertTrusted(t, "after the IR retired the old CA")
	if _, err := r.ex.RotateRetireEX(ctx, r.rid()); err != nil {
		t.Fatal(err)
	}
	r.assertTrusted(t, "after RETIRE_OLD")
	b, _ = os.ReadFile(r.irCAFile())
	cs, _ := parseCerts(b)
	if len(cs) != 1 || shaHex(cs[0].Raw) != p.CASHA256 || shaHex(b) == oldCA {
		t.Fatal("IR does not trust exactly the new CA after RETIRE_OLD")
	}
	if m, _ := filepath.Glob(r.ex.pkiDir() + ".*"); len(m) != 0 {
		t.Fatalf("old key material survived RETIRE: %v", m)
	}
	// The old certificate is no longer accepted by the IR.
	if _, err := oldCert.Verify(x509.VerifyOptions{Roots: poolOf(cs), DNSName: "127.0.0.1"}); err == nil {
		t.Fatal("the IR still accepts the old certificate after RETIRE_OLD")
	}
	for _, n := range []*node{r.ex, r.ir} {
		if n.CertEpoch() != 1 || n.rotationInProgress() != "" {
			t.Fatalf("epoch %d, in progress %q", n.CertEpoch(), n.rotationInProgress())
		}
		if _, err := n.RotateRollback(ctx, r.rid(), false); err == nil {
			t.Fatal("a retired rotation rolled back")
		}
	}
	// Idempotency: every step again returns success without changing anything.
	if _, err := r.ex.RotatePrepareEX(ctx, r.rid(), "t1", 1); err == nil {
		t.Fatal("prepare of a retired rotation was accepted")
	}
	if _, err := r.ir.RotateRetireIR(ctx, r.rid()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ex.RotateRetireEX(ctx, r.rid()); err != nil {
		t.Fatal(err)
	}
	// A second rotation with the same epoch is stale; the next one works.
	if _, err := r.ex.RotatePrepareEX(ctx, "rot-again", "t1", 1); err == nil || !strings.Contains(err.Error(), "epoch") {
		t.Fatalf("stale epoch accepted: %v", err)
	}
	r.epoch = 2
	p2 := r.prepare(t)
	r.trust(t, p2)
	r.verify(t, p2)
	r.activate(t)
	r.confirm(t, p2)
	r.retire(t)
	r.assertTrusted(t, "after a second rotation")
}

func TestRotationStepsAreIdempotent(t *testing.T) {
	r := newRotRig(t)
	ctx := context.Background()
	p := r.prepare(t)
	if p2 := r.prepare(t); p2 != p {
		t.Fatalf("prepare again gave another plan:\n%+v\n%+v", p, p2)
	}
	r.trust(t, p)
	r.trust(t, p)
	b, _ := os.ReadFile(r.irCAFile())
	if cs, _ := parseCerts(b); len(cs) != 2 {
		t.Fatal("trust twice added the CA twice")
	}
	r.verify(t, p)
	r.activate(t)
	r.activate(t)
	r.confirm(t, p)
	r.confirm(t, p)
	r.retire(t)
	r.retire(t)
	r.assertTrusted(t, "after repeated steps")
	_ = ctx
}

// HQ fault point: after PREPARE.
func TestRotationFaultAfterPrepare(t *testing.T) {
	r := newRotRig(t)
	oldLeaf, oldCA := r.snapshot(t)
	r.crashAt(r.ex, "prepare:after-record")
	if _, err := r.ex.RotatePrepareEX(context.Background(), r.rid(), "t1", 1); err == nil {
		t.Fatal("no crash")
	}
	r.assertTrusted(t, "crash inside PREPARE")
	p := r.prepare(t) // resumes
	r.assertTrusted(t, "after a resumed PREPARE")
	r.crashAt(r.ex, "prepare:after-stage")
	r.rollback(t, true)
	r.assertBackToOld(t, oldLeaf, oldCA)
	_ = p
}

// HQ fault point: after DISTRIBUTE (bundle written, the IR not yet restarted).
func TestRotationFaultAfterDistribute(t *testing.T) {
	r := newRotRig(t)
	ctx := context.Background()
	oldLeaf, oldCA := r.snapshot(t)
	p := r.prepare(t)
	r.crashAt(r.ir, "trust:after-write")
	if _, err := r.ir.RotateTrustIR(ctx, r.rid(), "t1", 1, der(t, p.CADER), p.CASHA256); err == nil {
		t.Fatal("no crash")
	}
	r.assertTrusted(t, "crash after writing the bundle")
	// The bundle is on disk but the running IR never loaded it: VERIFY must
	// not pass, so the EX can never activate against a stale trust.
	if _, err := r.ir.RotateVerifyIR(ctx, r.rid(), der(t, p.CertDER), p.CertSHA256); err == nil {
		t.Fatal("verify passed although the IR never loaded the new trust")
	}
	r.trust(t, p) // resumes: restarts and proves
	r.assertTrusted(t, "after a resumed DISTRIBUTE")
	r.rollback(t, true)
	r.assertBackToOld(t, oldLeaf, oldCA)
}

// HQ fault point: after VERIFY.
func TestRotationFaultAfterVerify(t *testing.T) {
	r := newRotRig(t)
	oldLeaf, oldCA := r.snapshot(t)
	p := r.prepare(t)
	r.trust(t, p)
	r.verify(t, p)
	r.assertTrusted(t, "after VERIFY")
	// Disconnect here: BCC rolls back. The IR proves the EX still serves a
	// certificate its old trust accepts before it drops the new CA.
	r.rollback(t, false)
	r.assertBackToOld(t, oldLeaf, oldCA)
}

// HQ fault point: during ACTIVATE (crash before and after the swap, and a
// service that does not start on the new set).
func TestRotationFaultDuringActivate(t *testing.T) {
	for _, point := range []string{"activate:before-swap", "activate:after-swap", "activate:before-restart"} {
		t.Run(point, func(t *testing.T) {
			r := newRotRig(t)
			ctx := context.Background()
			oldLeaf, oldCA := r.snapshot(t)
			p := r.prepare(t)
			r.trust(t, p)
			r.verify(t, p)
			r.crashAt(r.ex, point)
			if _, err := r.ex.RotateActivateEX(ctx, r.rid()); err == nil {
				t.Fatal("no crash")
			}
			r.assertTrusted(t, "crash "+point)
			// Reboot of the EX at this instant: it starts on whatever pki holds.
			if _, err := r.ex.host.Systemctl(ctx, "restart", "baft"); err != nil {
				t.Fatal(err)
			}
			r.assertTrusted(t, "EX reboot after "+point)
			r.activate(t) // resumes
			r.assertTrusted(t, "after a resumed ACTIVATE")
			if r.servedLeaf(t) != p.CertSHA256 {
				t.Fatal("not serving the new certificate")
			}
			r.rollback(t, false)
			r.assertBackToOld(t, oldLeaf, oldCA)
		})
	}
	t.Run("start-failure", func(t *testing.T) {
		r := newRotRig(t)
		ctx := context.Background()
		oldLeaf, oldCA := r.snapshot(t)
		p := r.prepare(t)
		r.trust(t, p)
		r.verify(t, p)
		r.failEX = 1
		_, err := r.ex.RotateActivateEX(ctx, r.rid())
		if err == nil || !strings.Contains(err.Error(), "previous certificate was restored") {
			t.Fatalf("activate with a failing start: %v", err)
		}
		r.assertTrusted(t, "after an automatic restore")
		if r.servedLeaf(t) != oldLeaf {
			t.Fatal("the old certificate is not served after the restore")
		}
		if st, _ := r.ex.RotationState(r.rid()); st.Phase != RotStaged {
			t.Fatalf("phase %s after restore, want staged", st.Phase)
		}
		r.activate(t) // retry works
		r.rollback(t, false)
		r.assertBackToOld(t, oldLeaf, oldCA)
	})
}

// HQ fault point: after the first node activated (the EX), the IR must not
// drop the new CA before the EX rolled back.
func TestRotationFaultAfterFirstActivation(t *testing.T) {
	r := newRotRig(t)
	ctx := context.Background()
	oldLeaf, oldCA := r.snapshot(t)
	p := r.prepare(t)
	r.trust(t, p)
	r.verify(t, p)
	r.activate(t)
	for _, never := range []bool{false, true} {
		if _, err := r.ir.RotateRollback(ctx, r.rid(), never); err == nil || !strings.Contains(err.Error(), "still serves the new certificate") {
			t.Fatalf("IR rollback while the EX serves the new certificate: %v", err)
		}
	}
	r.assertTrusted(t, "after the refused IR rollback")
	r.rollback(t, false)
	r.assertBackToOld(t, oldLeaf, oldCA)
}

// HQ fault point: before CONFIRM (the EX crashed after it restarted on the
// new set, and the IR cannot be reached for a while).
func TestRotationFaultBeforeConfirm(t *testing.T) {
	r := newRotRig(t)
	ctx := context.Background()
	p := r.prepare(t)
	r.trust(t, p)
	r.verify(t, p)
	r.crashAt(r.ex, "activate:after-restart")
	if _, err := r.ex.RotateActivateEX(ctx, r.rid()); err == nil {
		t.Fatal("no crash")
	}
	r.assertTrusted(t, "EX crashed before CONFIRM")
	// The IR is restarted (disconnect/reboot) with its on-disk trust.
	if _, err := r.ir.host.Systemctl(ctx, "restart", "baft"); err != nil {
		t.Fatal(err)
	}
	r.assertTrusted(t, "IR reboot before CONFIRM")
	if _, err := r.ir.RotateConfirmIR(ctx, r.rid(), "0000000000000000000000000000000000000000000000000000000000000000"); err == nil {
		t.Fatal("confirm accepted another certificate")
	}
	r.activate(t)
	r.confirm(t, p)
	r.retire(t)
	r.assertTrusted(t, "completed after a crash before CONFIRM")
}

// HQ fault points: after CONFIRM and before RETIRE.
func TestRotationFaultAroundRetire(t *testing.T) {
	t.Run("after-confirm-ex-gone-back", func(t *testing.T) {
		r := newRotRig(t)
		ctx := context.Background()
		p := r.prepare(t)
		r.trust(t, p)
		r.verify(t, p)
		r.activate(t)
		r.confirm(t, p)
		// The EX went back to the old certificate after CONFIRM: the IR must
		// refuse to retire the old CA.
		if _, err := r.ex.RotateRollback(ctx, r.rid(), false); err != nil {
			t.Fatal(err)
		}
		calls := len(r.ir.host.calls)
		caBefore, _ := os.ReadFile(r.irCAFile())
		if _, err := r.ir.RotateRetireIR(ctx, r.rid()); err == nil {
			t.Fatal("IR retired the old CA while the EX serves the old certificate")
		}
		caAfter, _ := os.ReadFile(r.irCAFile())
		for _, c := range r.ir.host.calls[calls:] {
			if strings.HasPrefix(c, "restart") {
				t.Fatal("the refused retire still restarted the IR on a new-only trust")
			}
		}
		if string(caBefore) != string(caAfter) {
			t.Fatal("the refused retire touched the CA file")
		}
		r.assertTrusted(t, "refused retire")
		if _, err := r.ir.RotateRollback(ctx, r.rid(), false); err != nil {
			t.Fatal(err)
		}
		r.assertTrusted(t, "rolled back after CONFIRM")
	})
	for _, point := range []string{"retire-ir:before-write", "retire-ir:after-write"} {
		t.Run(point, func(t *testing.T) {
			r := newRotRig(t)
			ctx := context.Background()
			p := r.prepare(t)
			r.trust(t, p)
			r.verify(t, p)
			r.activate(t)
			r.confirm(t, p)
			r.crashAt(r.ir, point)
			if _, err := r.ir.RotateRetireIR(ctx, r.rid()); err == nil {
				t.Fatal("no crash")
			}
			r.assertTrusted(t, "crash "+point)
			if _, err := r.ir.host.Systemctl(ctx, "restart", "baft"); err != nil {
				t.Fatal(err)
			}
			r.assertTrusted(t, "IR reboot after "+point)
			// Once retiring started, neither side rolls back.
			for _, n := range []*node{r.ir} {
				if _, err := n.RotateRollback(ctx, r.rid(), false); err == nil {
					t.Fatal("rollback accepted while retiring")
				}
			}
			r.retire(t)
			r.assertTrusted(t, "retired after "+point)
		})
	}
	t.Run("retire-ex:before-delete", func(t *testing.T) {
		r := newRotRig(t)
		ctx := context.Background()
		p := r.prepare(t)
		r.trust(t, p)
		r.verify(t, p)
		r.activate(t)
		r.confirm(t, p)
		if _, err := r.ir.RotateRetireIR(ctx, r.rid()); err != nil {
			t.Fatal(err)
		}
		r.crashAt(r.ex, "retire-ex:before-delete")
		if _, err := r.ex.RotateRetireEX(ctx, r.rid()); err == nil {
			t.Fatal("no crash")
		}
		r.assertTrusted(t, "crash before deleting the old set")
		if _, err := r.ex.RotateRollback(ctx, r.rid(), false); err == nil {
			t.Fatal("EX rolled back while retiring")
		}
		if _, err := r.ex.RotateRetireEX(ctx, r.rid()); err != nil {
			t.Fatal(err)
		}
		r.assertTrusted(t, "after a resumed RETIRE")
	})
}

func TestRotationRefusesWhatItDoesNotOwn(t *testing.T) {
	ctx := context.Background()
	t.Run("live pki changed", func(t *testing.T) {
		r := newRotRig(t)
		p := r.prepare(t)
		r.trust(t, p)
		r.verify(t, p)
		host := filepath.Join(r.ex.pkiDir(), "host")
		if err := os.WriteFile(host, []byte("operator.example\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := r.ex.RotateActivateEX(ctx, r.rid()); err == nil || !strings.Contains(err.Error(), "outside BAFT") {
			t.Fatalf("activate over a changed live set: %v", err)
		}
		r.assertTrusted(t, "refused activation")
	})
	t.Run("IR CA file changed", func(t *testing.T) {
		r := newRotRig(t)
		p := r.prepare(t)
		r.trust(t, p)
		b, _ := os.ReadFile(r.irCAFile())
		if err := os.WriteFile(r.irCAFile(), append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := r.ex.RotateRollback(ctx, r.rid(), false); err != nil {
			t.Fatal(err)
		}
		if _, err := r.ir.RotateRollback(ctx, r.rid(), false); err == nil || !strings.Contains(err.Error(), "outside BAFT") {
			t.Fatalf("IR rollback over a changed CA file: %v", err)
		}
	})
	t.Run("live config changed", func(t *testing.T) {
		r := newRotRig(t)
		b, _ := os.ReadFile(r.ex.liveConfig())
		if err := os.WriteFile(r.ex.liveConfig(), append(b, ' '), 0o640); err != nil {
			t.Fatal(err)
		}
		if _, err := r.ex.RotatePrepareEX(ctx, r.rid(), "t1", 1); err == nil || !strings.Contains(err.Error(), "outside BAFT") {
			t.Fatalf("prepare over a changed config: %v", err)
		}
	})
	t.Run("wrong tunnel or role", func(t *testing.T) {
		r := newRotRig(t)
		if _, err := r.ex.RotatePrepareEX(ctx, r.rid(), "t2", 1); err == nil {
			t.Fatal("prepare for an unknown tunnel")
		}
		p := r.prepare(t)
		if _, err := r.ex.RotateTrustIR(ctx, "rot-x", "t1", 1, der(t, p.CADER), p.CASHA256); err == nil {
			t.Fatal("the EX accepted the IR's step")
		}
		if _, err := r.ir.RotateTrustIR(ctx, r.rid(), "t1", 1, der(t, p.CADER), strings.Repeat("0", 64)); err == nil {
			t.Fatal("trust accepted a CA that does not match its digest")
		}
		if _, err := r.ir.RotateTrustIR(ctx, r.rid(), "t1", 1, der(t, p.CertDER), p.CertSHA256); err == nil {
			t.Fatal("trust accepted a leaf as a CA")
		}
	})
	t.Run("one change at a time", func(t *testing.T) {
		r := newRotRig(t)
		r.prepare(t)
		if _, err := r.ex.RotatePrepareEX(ctx, "rot-other", "t1", 1); err == nil || !strings.Contains(err.Error(), "in progress") {
			t.Fatalf("second rotation: %v", err)
		}
		if _, err := r.ex.PrepareEX(ctx, "t2", r.exp); err == nil || !strings.Contains(err.Error(), "rotation") {
			t.Fatalf("tunnel change during a rotation: %v", err)
		}
		if _, err := r.ex.RotateActivateEX(ctx, r.rid()); err == nil || !strings.Contains(err.Error(), "verified") {
			t.Fatalf("activation without VERIFY: %v", err)
		}
	})
}

func TestCertEpochStrictFailsClosedAgainstRetiredHistory(t *testing.T) {
	m := &Manager{Env: Env{StateDir: t.TempDir()}}
	if got, err := m.certEpochStrict(); err != nil || got != 0 {
		t.Fatalf("fresh epoch = %d, %v; want 0, nil", got, err)
	}
	dir := m.rotDir("r1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	record := []byte(`{"version":1,"id":"r1","tunnel_id":"t1","role":"ex","epoch":3,"phase":"retired"}`)
	if err := os.WriteFile(m.rotPath("r1"), record, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.certEpochStrict(); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing epoch with retired history = %v; want fail-closed", err)
	}
	if err := os.WriteFile(m.rotEpochPath(), []byte("corrupt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.certEpochStrict(); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("corrupt epoch = %v; want fail-closed", err)
	}
	if err := os.WriteFile(m.rotEpochPath(), []byte("2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.certEpochStrict(); err == nil || !strings.Contains(err.Error(), "behind") {
		t.Fatalf("stale epoch = %v; want fail-closed", err)
	}
	if err := os.WriteFile(m.rotEpochPath(), []byte("3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := m.certEpochStrict(); err != nil || got != 3 {
		t.Fatalf("matching epoch = %d, %v; want 3, nil", got, err)
	}
}
