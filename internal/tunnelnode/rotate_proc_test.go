package tunnelnode

import (
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// procHost runs the real `baft run` as the service: restart kills the
// previous process and starts a new one on the live config.
type procHost struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	config string
	log    string
	exited chan struct{}
	gen    int
}

func (h *procHost) Systemctl(ctx context.Context, args ...string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch args[0] {
	case "restart":
		h.stopLocked()
		return "", h.startLocked()
	case "stop":
		h.stopLocked()
	case "is-active":
		if h.cmd == nil {
			return "inactive", nil
		}
		select {
		case <-h.exited:
			return "activating", nil
		default:
			return "active", nil
		}
	case "is-enabled":
		return "enabled", nil
	case "show":
		return "0", nil
	}
	return "", nil
}

// startLocked starts the service; like Restart=on-failure with
// RestartSec=1s, a process that exits on its own is started again.
func (h *procHost) startLocked() error {
	f, _ := os.OpenFile(h.log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	c := exec.Command(baftBin, "run", "--file", h.config)
	c.Stdout, c.Stderr = f, f
	if err := c.Start(); err != nil {
		return err
	}
	h.gen++
	gen := h.gen
	h.cmd, h.exited = c, make(chan struct{})
	ch := h.exited
	go func() {
		c.Wait()
		f.Close()
		close(ch)
		time.Sleep(time.Second)
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.gen == gen && h.cmd == c {
			_ = h.startLocked()
		}
	}()
	return nil
}

func (h *procHost) stopLocked() {
	if h.cmd != nil {
		h.gen++
		h.cmd.Process.Kill()
		<-h.exited
		h.cmd = nil
	}
}

func (h *procHost) Run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// echo serves a TCP echo target for the tunnel route.
func echo(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	return l.Addr().(*net.TCPAddr).Port
}

// through sends data IR route -> EX -> target and back, retrying while the
// dialer reconnects after a restart.
func through(t *testing.T, route, when string) {
	t.Helper()
	msg := []byte("baft rotation " + when)
	var last error
	for i := 0; i < 40; i++ {
		c, err := net.DialTimeout("tcp", route, 2*time.Second)
		if err == nil {
			c.SetDeadline(time.Now().Add(3 * time.Second))
			buf := make([]byte, len(msg))
			if _, err = c.Write(msg); err == nil {
				_, err = io.ReadFull(c, buf)
			}
			c.Close()
			if err == nil && string(buf) == string(msg) {
				return
			}
		}
		last = err
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("no traffic through the tunnel %s: %v", when, last)
}

// TestRotationWithRealServices rotates the certificate of a real, running
// baft EX/IR pair (real `baft run` processes, a systemd-like restart policy,
// real traffic), then rolls a second rotation back after ACTIVATE.
func TestRotationWithRealServices(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}
	mk := func(name string) (*Manager, *procHost) {
		dir := t.TempDir()
		h := &procHost{config: filepath.Join(dir, "etc", "baft.yaml"), log: filepath.Join(dir, name+".log")}
		m, err := New(Env{ConfigDir: filepath.Join(dir, "etc"), StateDir: filepath.Join(dir, "state"), UnitDir: filepath.Join(dir, "units"),
			Service: "baft", BaftBin: baftBin, PairBin: pairBin, Settle: time.Second, System: h,
			MetricsListen: "127.0.0.1:" + strconv.Itoa(freePort(t))})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			h.mu.Lock()
			h.stopLocked()
			h.mu.Unlock()
			if t.Failed() {
				b, _ := os.ReadFile(h.log)
				t.Logf("== %s log:\n%s", name, b)
			}
		})
		return m, h
	}
	ex, _ := mk("ex")
	ir, _ := mk("ir")
	ctx := context.Background()
	port := freePort(t)
	route := "127.0.0.1:" + strconv.Itoa(freePort(t))
	target := "127.0.0.1:" + strconv.Itoa(echo(t))
	code, err := ex.PrepareEX(ctx, "t1", ExParams{PublicAddress: "127.0.0.1", Port: port, Target: target, RouteID: "service-main"})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := ir.PrepareIR(ctx, "t1", code, IRParams{RouteListen: route, RouteID: "service-main"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ex.CommitEX(ctx, "t1", reply); err != nil {
		t.Fatal(err)
	}
	if _, err := ir.CommitIR(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	for _, m := range []*Manager{ex, ir} {
		if _, err := m.Finalize(ctx, "t1"); err != nil {
			t.Fatal(err)
		}
	}
	through(t, route, "before the rotation")
	step := func(name string, f func() (string, error)) {
		t.Helper()
		out, err := f()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		t.Logf("%s: %s", name, out)
	}
	plan, err := ex.RotatePrepareEX(ctx, "rot-1", "t1", 1)
	if err != nil {
		t.Fatal(err)
	}
	caDER, _ := DecodeDER(plan.CADER)
	certDER, _ := DecodeDER(plan.CertDER)
	step("trust", func() (string, error) { return ir.RotateTrustIR(ctx, "rot-1", "t1", 1, caDER, plan.CASHA256) })
	step("verify-ir", func() (string, error) { return ir.RotateVerifyIR(ctx, "rot-1", certDER, plan.CertSHA256) })
	step("verify-ex", func() (string, error) { return ex.RotateVerifyEX(ctx, "rot-1") })
	step("activate", func() (string, error) { return ex.RotateActivateEX(ctx, "rot-1") })
	step("confirm", func() (string, error) { return ir.RotateConfirmIR(ctx, "rot-1", plan.CertSHA256) })
	step("retire-ir", func() (string, error) { return ir.RotateRetireIR(ctx, "rot-1") })
	through(t, route, "after CONFIRM")
	step("retire-ex", func() (string, error) { return ex.RotateRetireEX(ctx, "rot-1") })
	through(t, route, "after RETIRE_OLD")

	// A second rotation rolled back after ACTIVATE: EX first, then IR.
	plan2, err := ex.RotatePrepareEX(ctx, "rot-2", "t1", 2)
	if err != nil {
		t.Fatal(err)
	}
	ca2, _ := DecodeDER(plan2.CADER)
	cert2, _ := DecodeDER(plan2.CertDER)
	step("trust-2", func() (string, error) { return ir.RotateTrustIR(ctx, "rot-2", "t1", 2, ca2, plan2.CASHA256) })
	step("verify-ir-2", func() (string, error) { return ir.RotateVerifyIR(ctx, "rot-2", cert2, plan2.CertSHA256) })
	step("verify-ex-2", func() (string, error) { return ex.RotateVerifyEX(ctx, "rot-2") })
	step("activate-2", func() (string, error) { return ex.RotateActivateEX(ctx, "rot-2") })
	through(t, route, "with the second certificate active")
	if _, err := ir.RotateRollback(ctx, "rot-2", false); err == nil {
		t.Fatal("the IR dropped the new CA while the EX serves the new certificate")
	}
	step("rollback-ex-2", func() (string, error) { return ex.RotateRollback(ctx, "rot-2", false) })
	step("rollback-ir-2", func() (string, error) { return ir.RotateRollback(ctx, "rot-2", false) })
	through(t, route, "after the rollback")
}
