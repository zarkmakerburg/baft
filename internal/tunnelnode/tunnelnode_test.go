package tunnelnode

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var baftBin, pairBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "tunnelnode-bin")
	if err != nil {
		panic(err)
	}
	baftBin, pairBin = filepath.Join(dir, "baft"), filepath.Join(dir, "baft-pair")
	for bin, pkg := range map[string]string{baftBin: "../../cmd/baft", pairBin: "../../cmd/baft-pair"} {
		if out, err := exec.Command("go", "build", "-o", bin, pkg).CombinedOutput(); err != nil {
			panic(string(out))
		}
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// fakeHost runs the real baft/baft-pair binaries but a pretend systemd.
type fakeHost struct {
	mu          sync.Mutex
	active      bool
	enabled     bool
	failRestart bool
	calls       []string
}

func (f *fakeHost) Systemctl(_ context.Context, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.Join(args, " "))
	switch args[0] {
	case "restart":
		f.active = !f.failRestart
	case "stop":
		f.active = false
	case "enable":
		f.enabled = true
	case "disable":
		f.enabled = false
	case "is-active":
		switch {
		case f.active:
			return "active", nil
		case f.failRestart:
			return "failed", nil
		}
		return "inactive", nil
	case "is-enabled":
		if f.enabled {
			return "enabled", nil
		}
		return "disabled", nil
	case "show":
		return "0", nil
	}
	return "", nil
}

func (f *fakeHost) Run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (f *fakeHost) called(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

type node struct {
	*Manager
	host *fakeHost
	dir  string
}

func newNode(t *testing.T) *node {
	t.Helper()
	dir := t.TempDir()
	h := &fakeHost{}
	m, err := New(Env{
		ConfigDir: filepath.Join(dir, "etc"), StateDir: filepath.Join(dir, "state"), UnitDir: filepath.Join(dir, "units"),
		Service: "baft", BaftBin: baftBin, PairBin: pairBin, Settle: 30 * time.Millisecond, System: h,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &node{Manager: m, host: h, dir: dir}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// listen keeps a TCP listener up for the health check to find.
func listen(t *testing.T, port int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
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
			c.Close()
		}
	}()
}

type pair struct {
	ex, ir   *node
	exPort   int
	irListen string
	exp      ExParams
	irp      IRParams
}

func newPair(t *testing.T) *pair {
	t.Helper()
	p := &pair{ex: newNode(t), ir: newNode(t), exPort: freePort(t)}
	p.irListen = "127.0.0.1:" + strconv.Itoa(freePort(t))
	p.exp = ExParams{PublicAddress: "127.0.0.1", Port: p.exPort, Target: "127.0.0.1:2443", RouteID: "service-main"}
	p.irp = IRParams{RouteListen: p.irListen, RouteID: "service-main"}
	return p
}

// build runs a whole change through commit and returns the secrets used.
func (p *pair) build(t *testing.T, id string) (code, reply string) {
	t.Helper()
	ctx := context.Background()
	var err error
	snap := func(n *node) (string, int) {
		b, _ := os.ReadFile(n.liveConfig())
		return string(b), len(n.host.calls)
	}
	exCfg0, exCalls0 := snap(p.ex)
	irCfg0, irCalls0 := snap(p.ir)
	if code, err = p.ex.PrepareEX(ctx, id, p.exp); err != nil {
		t.Fatalf("PrepareEX: %v", err)
	}
	if !strings.HasPrefix(code, "BAFTPAIR1:") {
		t.Fatalf("pairing code %q", code)
	}
	if reply, err = p.ir.PrepareIR(ctx, id, code, p.irp); err != nil {
		t.Fatalf("PrepareIR: %v", err)
	}
	if !strings.HasPrefix(reply, "BAFTREPLY1:") {
		t.Fatalf("reply code %q", reply)
	}
	// Prepare touches nothing live.
	for n, before := range map[*node][2]any{p.ex: {exCfg0, exCalls0}, p.ir: {irCfg0, irCalls0}} {
		cfg, calls := snap(n)
		if cfg != before[0] {
			t.Fatal("prepare changed the live config")
		}
		for _, c := range n.host.calls[before[1].(int):calls] {
			if strings.HasPrefix(c, "restart") || strings.HasPrefix(c, "enable") || strings.HasPrefix(c, "stop") {
				t.Fatalf("prepare touched the service: %s", c)
			}
		}
	}
	if out, err := p.ex.CommitEX(ctx, id, reply); err != nil {
		t.Fatalf("CommitEX: %v", err)
	} else if !strings.Contains(out, "active") {
		t.Fatalf("CommitEX: %q", out)
	}
	if _, err := p.ir.CommitIR(ctx, id); err != nil {
		t.Fatalf("CommitIR: %v", err)
	}
	return code, reply
}

func TestFullChangeBuildsBothEnds(t *testing.T) {
	p := newPair(t)
	listen(t, p.exPort)
	listen(t, mustPort(p.irListen))
	code, reply := p.build(t, "t1")
	ctx := context.Background()
	for name, n := range map[string]*node{"ex": p.ex, "ir": p.ir} {
		if _, err := n.Health(ctx, "t1"); err != nil {
			t.Fatalf("%s health: %v", name, err)
		}
		if st, _ := os.Stat(n.liveConfig()); st == nil || st.Mode().Perm() != 0o640 {
			t.Fatalf("%s live config missing or wrong mode", name)
		}
		unit, err := os.ReadFile(n.unitPath())
		if err != nil || !strings.Contains(string(unit), "ExecStart="+baftBin+" run --file "+n.liveConfig()) {
			t.Fatalf("%s unit: %v\n%s", name, err, unit)
		}
		if !n.host.enabled || !n.host.active {
			t.Fatalf("%s service not enabled/active", name)
		}
	}
	exCfg, _ := os.ReadFile(p.ex.liveConfig())
	irCfg, _ := os.ReadFile(p.ir.liveConfig())
	if !strings.Contains(string(exCfg), `"listener"`) || !strings.Contains(string(irCfg), `"dialer"`) || !strings.Contains(string(irCfg), p.irListen) {
		t.Fatalf("configs do not look like a pair:\n%s\n%s", exCfg, irCfg)
	}
	for _, n := range []*node{p.ex, p.ir} {
		if _, err := n.Finalize(ctx, "t1"); err != nil {
			t.Fatal(err)
		}
		// One-time secrets and backups are gone; the change stays on record.
		walkNoSecrets(t, n.StateDir, code, reply)
		if n.activeID() != "" {
			t.Fatal("change still active after finalize")
		}
		if _, err := n.Rollback(ctx, "t1"); err == nil {
			t.Fatal("rollback of a finalized change was allowed")
		}
	}
}

func walkNoSecrets(t *testing.T, root string, secrets ...string) {
	t.Helper()
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		b, _ := os.ReadFile(path)
		for _, s := range secrets {
			if strings.Contains(string(b), s) {
				t.Errorf("%s still holds a pairing secret", path)
			}
		}
		if base := filepath.Base(path); base == "psk" || base == "pending.json" || base == "pairing.pending.json" {
			t.Errorf("%s survived finalize", path)
		}
		return nil
	})
}

func mustPort(hostport string) int {
	_, p, _ := net.SplitHostPort(hostport)
	n, _ := strconv.Atoi(p)
	return n
}

func TestRollbackRestoresThePreviousTunnel(t *testing.T) {
	p := newPair(t)
	p.build(t, "first")
	ctx := context.Background()
	for _, n := range []*node{p.ex, p.ir} {
		if _, err := n.Finalize(ctx, "first"); err != nil {
			t.Fatal(err)
		}
	}
	exBefore, _ := os.ReadFile(p.ex.liveConfig())
	irBefore, _ := os.ReadFile(p.ir.liveConfig())

	p.exp.Port = freePort(t) // a different change
	p.build(t, "second")
	if now, _ := os.ReadFile(p.ex.liveConfig()); string(now) == string(exBefore) {
		t.Fatal("second change did not change the EX config")
	}
	for name, n := range map[string]*node{"ex": p.ex, "ir": p.ir} {
		msg, err := n.Rollback(ctx, "second")
		if err != nil || msg != "rolled back" {
			t.Fatalf("%s rollback: %q %v", name, msg, err)
		}
		if msg, err := n.Rollback(ctx, "second"); err != nil || msg != "already rolled back" {
			t.Fatalf("%s second rollback: %q %v", name, msg, err)
		}
	}
	if now, _ := os.ReadFile(p.ex.liveConfig()); string(now) != string(exBefore) {
		t.Fatal("EX config not restored")
	}
	if now, _ := os.ReadFile(p.ir.liveConfig()); string(now) != string(irBefore) {
		t.Fatal("IR config not restored")
	}
	if !p.ex.host.active {
		t.Fatal("previous service not running again")
	}
	// The earlier change's files are still where the restored config expects them.
	if _, err := os.Stat(filepath.Join(p.ir.stage("first"), "peer-ca.pem")); err != nil {
		t.Fatalf("restored IR config lost its CA: %v", err)
	}
}

func TestFailedCommitRestoresAndLeavesNothingBehind(t *testing.T) {
	p := newPair(t)
	ctx := context.Background()
	code, err := p.ex.PrepareEX(ctx, "f1", p.exp)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := p.ir.PrepareIR(ctx, "f1", code, p.irp)
	if err != nil {
		t.Fatal(err)
	}
	p.ex.host.failRestart = true
	if _, err := p.ex.CommitEX(ctx, "f1", reply); err == nil {
		t.Fatal("commit reported success with the service down")
	}
	if _, err := os.Stat(p.ex.liveConfig()); err == nil {
		t.Fatal("failed first commit left a config behind")
	}
	if _, err := os.Stat(p.ex.unitPath()); err == nil {
		t.Fatal("failed first commit left a unit behind")
	}
	if p.ex.host.enabled || p.ex.host.active {
		t.Fatal("failed first commit left the service enabled or running")
	}
	if tx, _ := p.ex.readTxn("f1"); tx.Phase != PhaseRolledBack {
		t.Fatalf("phase %q", tx.Phase)
	}
	if p.ex.activeID() != "" {
		t.Fatal("node still claims the failed change")
	}
}

func TestWrongReplyKeepsLiveStateUntouched(t *testing.T) {
	p := newPair(t)
	ctx := context.Background()
	code, _ := p.ex.PrepareEX(ctx, "w1", p.exp)
	p.ir.PrepareIR(ctx, "w1", code, p.irp)
	if _, err := p.ex.CommitEX(ctx, "w1", "BAFTREPLY1:AAAAAAAAAAAAAAAAAAAAAAAAAAAA"); err == nil {
		t.Fatal("a forged reply was accepted")
	}
	if _, err := os.Stat(p.ex.liveConfig()); err == nil || p.ex.host.called("restart") {
		t.Fatal("a rejected reply touched the live node")
	}
	if tx, _ := p.ex.readTxn("w1"); tx.Phase != PhasePrepared {
		t.Fatalf("phase %q, want prepared so BCC can roll it back", tx.Phase)
	}
	if msg, err := p.ex.Rollback(ctx, "w1"); err != nil || msg != "rolled back" {
		t.Fatalf("rollback: %q %v", msg, err)
	}
}

func TestOneChangeAtATimeAndIdempotentRollback(t *testing.T) {
	p := newPair(t)
	ctx := context.Background()
	if _, err := p.ex.PrepareEX(ctx, "a1", p.exp); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ex.PrepareEX(ctx, "a2", p.exp); err == nil || !strings.Contains(err.Error(), "in progress") {
		t.Fatalf("second concurrent change: %v", err)
	}
	if _, err := p.ex.PrepareEX(ctx, "a1", p.exp); err == nil {
		t.Fatal("same change prepared twice")
	}
	if msg, err := p.ex.Rollback(ctx, "never-started"); err != nil || msg != "nothing to roll back" {
		t.Fatalf("rollback of unknown: %q %v", msg, err)
	}
	if _, err := p.ex.Rollback(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ex.PrepareEX(ctx, "a3", p.exp); err != nil {
		t.Fatalf("node stayed locked after rollback: %v", err)
	}
}

func TestParametersAreValidated(t *testing.T) {
	n := newNode(t)
	ctx := context.Background()
	good := ExParams{PublicAddress: "203.0.113.7", Port: 8443, Target: "127.0.0.1:2443", RouteID: "r1"}
	bad := map[string]func(*ExParams){
		"hostname target": func(p *ExParams) { p.Target = "example.com:443" },
		"no target port":  func(p *ExParams) { p.Target = "127.0.0.1" },
		"bad address":     func(p *ExParams) { p.PublicAddress = "a b" },
		"port 0":          func(p *ExParams) { p.Port = 0 },
		"bad route":       func(p *ExParams) { p.RouteID = "../x" },
	}
	for name, mutate := range bad {
		p := good
		mutate(&p)
		if _, err := n.PrepareEX(ctx, "v1", p); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := n.PrepareEX(ctx, "../escape", good); err == nil {
		t.Error("path traversal id accepted")
	}
	if _, err := n.PrepareIR(ctx, "v2", "BAFTPAIR1:x", IRParams{RouteListen: "0.0.0.0:1443", RouteID: "r1"}); err == nil {
		t.Error("non-loopback route listen accepted")
	}
	if _, err := os.Stat(filepath.Join(n.StateDir, "tunnels")); err == nil {
		t.Error("an invalid request left state behind")
	}
}

func TestUnitMatchesInstallScript(t *testing.T) {
	sh, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)cat >"/etc/systemd/system/\$BAFT_SERVICE\.service" <<EOF\n(.*?)\nEOF\n`).FindSubmatch(sh)
	if m == nil {
		t.Fatal("unit heredoc not found in install.sh")
	}
	fixed := func(text string, skip func(string) bool) []string {
		var out []string
		for _, l := range strings.Split(text, "\n") {
			l = strings.TrimSpace(l)
			if l == "" || strings.Contains(l, "$") || skip(l) {
				continue
			}
			out = append(out, l)
		}
		sort.Strings(out)
		return out
	}
	dyn := regexp.MustCompile(`^(User|Group|ExecStart|ExecReload|ReadWritePaths|AmbientCapabilities|CapabilityBoundingSet)=`)
	n := newNode(t)
	got := fixed(n.Unit(8443), func(l string) bool { return dyn.MatchString(l) })
	want := fixed(string(m[1]), func(string) bool { return false })
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("unit drifted from install.sh:\n got %q\nwant %q", got, want)
	}
	if !strings.Contains(n.Unit(443), "CAP_NET_BIND_SERVICE") || strings.Contains(n.Unit(8443), "CAP_NET_BIND_SERVICE") {
		t.Fatal("privileged-port capability handling is wrong")
	}
}

func TestGenerationAndObservedStateAreEvidenceNotExitStatus(t *testing.T) {
	p := newPair(t)
	listen(t, p.exPort)
	listen(t, mustPort(p.irListen))
	ctx := context.Background()
	if g := p.ex.readGeneration(); g != 0 {
		t.Fatalf("fresh node generation %d", g)
	}
	p.build(t, "g1")
	for name, n := range map[string]*node{"ex": p.ex, "ir": p.ir} {
		o, err := n.Observe(ctx, "g1")
		if err != nil {
			t.Fatalf("%s observe: %v", name, err)
		}
		if o.Phase != PhaseCommitted || o.Generation != 1 || o.PreviousGeneration != 0 || o.NodeGeneration != 1 || !o.ServiceActive || !o.UnitMatches || len(o.ConfigSHA256) != 64 {
			t.Fatalf("%s observed %+v", name, o)
		}
	}
	ex, _ := p.ex.Observe(ctx, "g1")
	ir, _ := p.ir.Observe(ctx, "g1")
	if ex.ConfigRole != "listener" || ex.Listen != "0.0.0.0:"+strconv.Itoa(p.exPort) || ex.Target != p.exp.Target || ex.RouteID != "service-main" {
		t.Fatalf("EX observed %+v", ex)
	}
	if ir.ConfigRole != "dialer" || ir.RouteListen != p.irListen || ir.PeerAddress != "127.0.0.1:"+strconv.Itoa(p.exPort) {
		t.Fatalf("IR observed %+v", ir)
	}
	// The report follows reality: a service that stopped is reported as not active.
	p.ex.host.mu.Lock()
	p.ex.host.active = false
	p.ex.host.mu.Unlock()
	if o, _ := p.ex.Observe(ctx, "g1"); o.ServiceActive {
		t.Fatal("observed state claims a stopped service is active")
	}
	p.ex.host.mu.Lock()
	p.ex.host.active = true
	p.ex.host.mu.Unlock()
	for _, n := range []*node{p.ex, p.ir} {
		n.Finalize(ctx, "g1")
	}
	// A second change moves the generation on; rolling it back restores it.
	p.exp.Port = freePort(t)
	p.build(t, "g2")
	if o, _ := p.ex.Observe(ctx, "g2"); o.Generation != 2 || o.PreviousGeneration != 1 {
		t.Fatalf("second change %+v", o)
	}
	for _, n := range []*node{p.ex, p.ir} {
		if _, err := n.Rollback(ctx, "g2"); err != nil {
			t.Fatal(err)
		}
		if g := n.readGeneration(); g != 1 {
			t.Fatalf("generation after rollback = %d, want 1", g)
		}
	}
	if _, err := p.ex.Observe(ctx, "never"); err == nil {
		t.Fatal("observing an unknown change succeeded")
	}
}
