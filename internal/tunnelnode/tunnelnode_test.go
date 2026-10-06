package tunnelnode

import (
	"context"
	"math/rand/v2"
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
	// onRestart plays the service starting: it loads what the real one would
	// and an error means it failed to start.
	onRestart func() error
	dead      bool
}

func (f *fakeHost) Systemctl(_ context.Context, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.Join(args, " "))
	switch args[0] {
	case "restart":
		f.active = !f.failRestart
		f.dead = false
		if f.active && f.onRestart != nil {
			if err := f.onRestart(); err != nil {
				f.active, f.dead = false, true
			}
		}
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
		case f.failRestart || f.dead:
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

func TestInstanceManagerScopesHostOwnership(t *testing.T) {
	n := newNode(t)
	a, err := n.Manager.ForInstance("ir-main-de", "127.0.0.1:9201")
	if err != nil {
		t.Fatal(err)
	}
	b, err := n.Manager.ForInstance("ir-backup-de", "127.0.0.1:9202")
	if err != nil {
		t.Fatal(err)
	}
	if a.ConfigDir == b.ConfigDir || a.StateDir == b.StateDir || a.Service == b.Service {
		t.Fatalf("instances are not isolated: a=%+v b=%+v", a.Env, b.Env)
	}
	if a.Service != "baft-ir-main-de" || b.Service != "baft-ir-backup-de" {
		t.Fatalf("unexpected scoped services: %q %q", a.Service, b.Service)
	}
	if !strings.Contains(a.ManagedUnit(8443, "t1", 1), "# baft-instance: ir-main-de") {
		t.Fatal("managed unit does not identify its instance")
	}
	legacy, err := n.Manager.ForInstance("default", "")
	if err != nil || legacy != n.Manager {
		t.Fatalf("legacy manager changed: %p %p %v", legacy, n.Manager, err)
	}
	if _, err := n.Manager.ForInstance("../bad", ""); err == nil {
		t.Fatal("unsafe instance id accepted")
	}
}

// freePort returns a port that is free now and is bound again later by the
// code under test. It is picked below the kernel's ephemeral range (Linux
// default 32768-60999): a port from ":0" comes from that range, so an
// outgoing connection made by any test running in parallel can take it before
// it is bound again ("address already in use").
func freePort(t *testing.T) int {
	t.Helper()
	for i := 0; i < 200; i++ {
		port := 20000 + rand.IntN(12000)
		l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
		if err != nil {
			continue
		}
		l.Close()
		return port
	}
	t.Fatal("no free port below the ephemeral range")
	return 0
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
	m := regexp.MustCompile(`(?s)render_service_unit\(\) \{.*?cat <<EOF\n(.*?)\nEOF\n`).FindSubmatch(sh)
	if m == nil {
		t.Fatal("unit heredoc not found in install.sh")
	}
	fixed := func(text string, skip func(string) bool) []string {
		var out []string
		for _, l := range strings.Split(text, "\n") {
			l = strings.TrimSpace(l)
			if l == "" || strings.Contains(l, "$") || strings.HasPrefix(l, "# baft-") || skip(l) {
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

func readMarker(t *testing.T, n *node) (Marker, bool) {
	t.Helper()
	var mk Marker
	if err := readJSON(n.markerPath(), &mk); err != nil {
		return Marker{}, false
	}
	return mk, true
}

func TestOwnershipMarkersAreWrittenAndRestored(t *testing.T) {
	p := newPair(t)
	listen(t, p.exPort)
	listen(t, mustPort(p.irListen))
	ctx := context.Background()
	if _, ok := readMarker(t, p.ex); ok {
		t.Fatal("marker exists before any change")
	}
	p.build(t, "m1")
	for name, n := range map[string]*node{"ex": p.ex, "ir": p.ir} {
		mk, ok := readMarker(t, n)
		if !ok || mk.ManagedBy != "baft" || mk.TunnelID != "m1" || mk.Generation != 1 {
			t.Fatalf("%s marker %+v ok=%v", name, mk, ok)
		}
		cfg, _ := os.ReadFile(n.liveConfig())
		unit, _ := os.ReadFile(n.unitPath())
		if mk.ConfigSHA256 != shaHex(cfg) || mk.UnitSHA256 != shaHex(unit) {
			t.Fatalf("%s marker hashes do not match the files", name)
		}
		for _, want := range []string{"# baft-managed: true", "# baft-tunnel: m1", "# baft-generation: 1"} {
			if !strings.Contains(string(unit), want) {
				t.Fatalf("%s unit lacks %q", name, want)
			}
		}
		o, err := n.Observe(ctx, "m1")
		if err != nil || !o.Managed || o.MarkerTunnelID != "m1" || o.MarkerGeneration != 1 || !o.MarkerConfigMatches || !o.MarkerUnitMatches {
			t.Fatalf("%s observed %+v err=%v", name, o, err)
		}
		if _, err := n.Finalize(ctx, "m1"); err != nil {
			t.Fatal(err)
		}
	}
	first, _ := readMarker(t, p.ex)

	// A second change replaces the marker; rolling it back restores the first.
	p.exp.Port = freePort(t)
	p.build(t, "m2")
	if mk, _ := readMarker(t, p.ex); mk.TunnelID != "m2" || mk.Generation != 2 {
		t.Fatalf("marker after second change %+v", mk)
	}
	for _, n := range []*node{p.ex, p.ir} {
		if _, err := n.Rollback(ctx, "m2"); err != nil {
			t.Fatal(err)
		}
	}
	if mk, _ := readMarker(t, p.ex); mk != first {
		t.Fatalf("marker after rollback %+v, want %+v", mk, first)
	}
}

func TestRollbackOfAFreshChangeRemovesTheMarker(t *testing.T) {
	p := newPair(t)
	p.build(t, "only")
	for _, n := range []*node{p.ex, p.ir} {
		if _, err := n.Rollback(context.Background(), "only"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(n.markerPath()); !os.IsNotExist(err) {
			t.Fatalf("marker left behind: %v", err)
		}
		if _, err := os.Stat(n.unitPath()); !os.IsNotExist(err) {
			t.Fatalf("unit left behind: %v", err)
		}
	}
}

func TestRollbackRefusesToOverwriteExternalEdits(t *testing.T) {
	p := newPair(t)
	listen(t, p.exPort)
	listen(t, mustPort(p.irListen))
	ctx := context.Background()
	p.build(t, "e1")
	for _, n := range []*node{p.ex, p.ir} {
		n.Finalize(ctx, "e1")
	}
	p.exp.Port = freePort(t)
	p.build(t, "e2")

	cfgBefore, _ := os.ReadFile(p.ex.liveConfig())
	edited := append(append([]byte{}, cfgBefore...), []byte("\n# hand edit\n")...)
	if err := os.WriteFile(p.ex.liveConfig(), edited, 0o640); err != nil {
		t.Fatal(err)
	}
	o, _ := p.ex.Observe(ctx, "e2")
	if o.MarkerConfigMatches {
		t.Fatal("observe does not notice the external edit")
	}
	msg, err := p.ex.Rollback(ctx, "e2")
	if err == nil || !strings.Contains(err.Error(), "rollback refused") {
		t.Fatalf("rollback over an external edit: %q %v", msg, err)
	}
	if now, _ := os.ReadFile(p.ex.liveConfig()); string(now) != string(edited) {
		t.Fatal("refused rollback still changed the config")
	}
	if tx, _ := p.ex.readTxn("e2"); tx.Phase != PhaseCommitted {
		t.Fatalf("phase moved to %s", tx.Phase)
	}

	// Restoring the BAFT-written content lets the rollback proceed.
	if err := os.WriteFile(p.ex.liveConfig(), cfgBefore, 0o640); err != nil {
		t.Fatal(err)
	}
	if msg, err := p.ex.Rollback(ctx, "e2"); err != nil || msg != "rolled back" {
		t.Fatalf("rollback after restore: %q %v", msg, err)
	}
}

func retireWant(l Live) RetireExpectation {
	return RetireExpectation{
		Generation: l.NodeGeneration, ConfigSHA256: l.ConfigSHA256,
		UnitSHA256: l.UnitSHA256, MarkerSHA256: l.MarkerSHA256,
	}
}

func TestRetireFinalizedManagedInstanceIsGuardedAndIdempotent(t *testing.T) {
	p := newPair(t)
	p.build(t, "retire1")
	ctx := context.Background()
	for _, n := range []*node{p.ex, p.ir} {
		if _, err := n.Finalize(ctx, "retire1"); err != nil {
			t.Fatal(err)
		}
	}

	// A sibling's files live in different instance-scoped paths and must not
	// be touched by retiring the historical singleton instance.
	sibling, err := p.ex.Manager.ForInstance("sibling", "127.0.0.1:9299")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sibling.ConfigDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sibling.liveConfig(), []byte("sibling-config\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sibling.UnitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sibling.unitPath(), []byte("sibling-unit\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for name, n := range map[string]*node{"ex": p.ex, "ir": p.ir} {
		before := n.Inspect(ctx)
		if !before.ConfigPresent || !before.UnitPresent || !before.MarkerPresent || !before.ServiceActive {
			t.Fatalf("%s pre-retire state %+v", name, before)
		}
		ev, err := n.Retire(ctx, "retire1", retireWant(before))
		if err != nil {
			t.Fatalf("%s retire: %v", name, err)
		}
		if !ev.Before.ServiceActive || ev.After.ConfigPresent || ev.After.UnitPresent || ev.After.MarkerPresent || ev.After.ServiceActive {
			t.Fatalf("%s evidence %+v", name, ev)
		}
		if n.host.active || n.host.enabled {
			t.Fatalf("%s service still active/enabled", name)
		}
		if st, err := n.readRetireState("retire1"); err != nil || st.Phase != "complete" {
			t.Fatalf("%s retire receipt %+v %v", name, st, err)
		}
		again, err := n.Retire(ctx, "retire1", retireWant(before))
		if err != nil || !again.Already {
			t.Fatalf("%s idempotent retry %+v %v", name, again, err)
		}
	}
	if b, err := os.ReadFile(sibling.liveConfig()); err != nil || string(b) != "sibling-config\n" {
		t.Fatalf("sibling config changed: %q %v", b, err)
	}
	if b, err := os.ReadFile(sibling.unitPath()); err != nil || string(b) != "sibling-unit\n" {
		t.Fatalf("sibling unit changed: %q %v", b, err)
	}
}

func TestRetireRefusesDriftBeforeAnyMutation(t *testing.T) {
	p := newPair(t)
	p.build(t, "retire-drift")
	ctx := context.Background()
	for _, n := range []*node{p.ex, p.ir} {
		if _, err := n.Finalize(ctx, "retire-drift"); err != nil {
			t.Fatal(err)
		}
	}
	n := p.ex
	before := n.Inspect(ctx)
	want := retireWant(before)
	original, _ := os.ReadFile(n.liveConfig())
	edited := append(append([]byte{}, original...), []byte("\n# external edit\n")...)
	if err := os.WriteFile(n.liveConfig(), edited, 0o640); err != nil {
		t.Fatal(err)
	}
	calls := len(n.host.calls)
	if _, err := n.Retire(ctx, "retire-drift", want); err == nil || !strings.Contains(err.Error(), "retire refused") {
		t.Fatalf("external drift was retired: %v", err)
	}
	for _, call := range n.host.calls[calls:] {
		if strings.HasPrefix(call, "stop ") || strings.HasPrefix(call, "disable ") || strings.HasPrefix(call, "daemon-reload") {
			t.Fatalf("refused retire mutated systemd: %v", n.host.calls[calls:])
		}
	}
	if now, _ := os.ReadFile(n.liveConfig()); string(now) != string(edited) {
		t.Fatal("refused retire changed edited config")
	}
	if _, err := os.Stat(n.retirePath("retire-drift")); !os.IsNotExist(err) {
		t.Fatalf("refused retire created receipt: %v", err)
	}

	// Restoring bytes but presenting a stale generation still fails closed.
	if err := os.WriteFile(n.liveConfig(), original, 0o640); err != nil {
		t.Fatal(err)
	}
	wrong := want
	wrong.Generation++
	if _, err := n.Retire(ctx, "retire-drift", wrong); err == nil || !strings.Contains(err.Error(), "generation") {
		t.Fatalf("stale generation accepted: %v", err)
	}
}

func TestRetireRefusesUnfinalizedTunnel(t *testing.T) {
	p := newPair(t)
	p.build(t, "retire-not-final")
	ctx := context.Background()
	before := p.ex.Inspect(ctx)
	want := retireWant(before)
	if _, err := p.ex.Retire(ctx, "retire-not-final", want); err == nil || !strings.Contains(err.Error(), "not finalized") {
		t.Fatalf("unfinalized retire accepted: %v", err)
	}
	if !p.ex.Inspect(ctx).ConfigPresent || !p.ex.host.active {
		t.Fatal("refused unfinalized retire changed the live instance")
	}
	if _, err := os.Stat(p.ex.retirePath("retire-not-final")); !os.IsNotExist(err) {
		t.Fatalf("unfinalized retire created a receipt: %v", err)
	}
}

func TestRetireResumesOnlyAfterDurableStartedReceipt(t *testing.T) {
	p := newPair(t)
	p.build(t, "retire-resume")
	ctx := context.Background()
	if _, err := p.ex.Finalize(ctx, "retire-resume"); err != nil {
		t.Fatal(err)
	}
	before := p.ex.Inspect(ctx)
	want := retireWant(before)
	st := retireState{
		TunnelID: "retire-resume", Instance: p.ex.Instance,
		RetireExpectation: want, Phase: "started", Updated: time.Now().UTC(),
	}
	if err := writeJSON(p.ex.retirePath("retire-resume"), st, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p.ex.liveConfig()); err != nil {
		t.Fatal(err)
	}
	ev, err := p.ex.Retire(ctx, "retire-resume", want)
	if err != nil {
		t.Fatal(err)
	}
	if ev.After.ConfigPresent || ev.After.UnitPresent || ev.After.MarkerPresent || ev.After.ServiceActive {
		t.Fatalf("resume did not finish retire: %+v", ev.After)
	}
	if st, _ := p.ex.readRetireState("retire-resume"); st.Phase != "complete" {
		t.Fatalf("resume receipt phase %q", st.Phase)
	}
}

func TestInspectSeesWhatIsReallyThere(t *testing.T) {
	p := newPair(t)
	listen(t, p.exPort)
	listen(t, mustPort(p.irListen))
	ctx := context.Background()
	if l := p.ex.Inspect(ctx); l.ConfigPresent || l.UnitPresent || l.MarkerPresent {
		t.Fatalf("empty node reports %+v", l)
	}
	p.build(t, "i1")
	l := p.ex.Inspect(ctx)
	if !l.ConfigPresent || !l.UnitPresent || !l.ConfigLoads || !l.MarkerPresent || l.MarkerTunnelID != "i1" || !l.MarkerConfigMatches || !l.MarkerUnitMatches || !l.ServiceActive || l.ConfigRole != "listener" {
		t.Fatalf("fresh node reports %+v", l)
	}
	cfg, _ := os.ReadFile(p.ex.liveConfig())
	unit, _ := os.ReadFile(p.ex.unitPath())
	mb, _ := os.ReadFile(p.ex.markerPath())
	if l.ConfigSHA256 != shaHex(cfg) || l.UnitSHA256 != shaHex(unit) || l.MarkerSHA256 != shaHex(mb) {
		t.Fatal("Inspect does not report the live digests of the files")
	}
	// A coupled tamper: edit the config and rewrite the marker to agree.
	// The marker comparisons pass, but the live digest no longer equals the old one.
	edited := append(append([]byte{}, cfg...), '\n', '#')
	os.WriteFile(p.ex.liveConfig(), edited, 0o640)
	var mk Marker
	readJSON(p.ex.markerPath(), &mk)
	mk.ConfigSHA256 = shaHex(edited)
	writeJSON(p.ex.markerPath(), mk, 0o644)
	if c := p.ex.Inspect(ctx); !c.MarkerConfigMatches || c.ConfigSHA256 == l.ConfigSHA256 || c.MarkerSHA256 == l.MarkerSHA256 {
		t.Fatalf("coupled tamper not visible in the live digests: %+v", c)
	}
	os.WriteFile(p.ex.liveConfig(), cfg, 0o640)
	os.WriteFile(p.ex.markerPath(), mb, 0o644)
	// Hand edit, then deleted files.
	os.WriteFile(p.ex.liveConfig(), append(cfg, '\n', '#'), 0o640)
	if l := p.ex.Inspect(ctx); l.MarkerConfigMatches {
		t.Fatal("hand-edited config still matches the marker")
	}
	os.Remove(p.ex.unitPath())
	if l := p.ex.Inspect(ctx); l.UnitPresent || l.MarkerUnitMatches {
		t.Fatalf("deleted unit reported %+v", l)
	}
	os.Remove(p.ex.markerPath())
	if l := p.ex.Inspect(ctx); l.MarkerPresent {
		t.Fatal("marker reported after deletion")
	}
}
