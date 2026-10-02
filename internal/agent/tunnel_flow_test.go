package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/bcc"
	"github.com/zarkmakerburg/baft/internal/tunnelnode"
)

var flowBaft, flowPair string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "agent-flow-bin")
	if err != nil {
		panic(err)
	}
	flowBaft, flowPair = filepath.Join(dir, "baft"), filepath.Join(dir, "baft-pair")
	for bin, pkg := range map[string]string{flowBaft: "../../cmd/baft", flowPair: "../../cmd/baft-pair"} {
		if out, err := exec.Command("go", "build", "-o", bin, pkg).CombinedOutput(); err != nil {
			panic(string(out))
		}
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// hostSys runs the real baft/baft-pair binaries and a pretend systemd.
type hostSys struct {
	mu          sync.Mutex
	active      bool
	enabled     bool
	failRestart bool
}

func (h *hostSys) Systemctl(_ context.Context, args ...string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch args[0] {
	case "restart":
		h.active = !h.failRestart
	case "stop":
		h.active = false
	case "enable":
		h.enabled = true
	case "disable":
		h.enabled = false
	case "is-active":
		if h.active {
			return "active", nil
		}
		if h.failRestart {
			return "failed", nil
		}
		return "inactive", nil
	case "is-enabled":
		if h.enabled {
			return "enabled", nil
		}
		return "disabled", nil
	case "show":
		return "0", nil
	}
	return "", nil
}

func (h *hostSys) Run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

type flowNode struct {
	id    string
	token string
	agent *Agent
	sys   *hostSys
	tn    *tunnelnode.Manager
	etc   string
}

type flow struct {
	t       *testing.T
	app     *bcc.Server
	store   *bcc.Store
	h       http.Handler
	ex, ir  *flowNode
	exPort  int
	irRoute string
	dir     string
}

func freeFlowPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func accept(t *testing.T, port int) {
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

func newFlow(t *testing.T) *flow {
	t.Helper()
	dir := t.TempDir()
	store, err := bcc.OpenStore(filepath.Join(dir, "bcc", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	app, err := bcc.NewServer(store, "admin")
	if err != nil {
		t.Fatal(err)
	}
	key, _, err := bcc.LoadOrCreateJobKey(filepath.Join(dir, "bcc", "job-key"))
	if err != nil {
		t.Fatal(err)
	}
	app.ConfigureJobSigning(key)
	f := &flow{t: t, app: app, store: store, h: app.Handler(), dir: dir, exPort: freeFlowPort(t)}
	f.irRoute = "127.0.0.1:" + strconv.Itoa(freeFlowPort(t))
	accept(t, f.exPort)
	accept(t, mustFlowPort(f.irRoute))
	srv := httptest.NewServer(app.Handler())
	t.Cleanup(srv.Close)
	f.ex = f.addNode(t, srv.URL, key.Public().(ed25519.PublicKey), "ex-1", "foreign", "127.0.0.1:1")
	f.ir = f.addNode(t, srv.URL, key.Public().(ed25519.PublicKey), "ir-1", "worker", "127.0.0.1:2")
	return f
}

func mustFlowPort(hp string) int {
	_, p, _ := net.SplitHostPort(hp)
	n, _ := strconv.Atoi(p)
	return n
}

func (f *flow) addNode(t *testing.T, url string, jobKey ed25519.PublicKey, id, role, addr string) *flowNode {
	t.Helper()
	token := "token-" + id
	if _, err := f.store.UpsertNode(bcc.Node{ID: id, Address: addr, Role: role}, token); err != nil {
		t.Fatal(err)
	}
	n := &flowNode{id: id, token: token, sys: &hostSys{}, etc: filepath.Join(f.dir, id, "etc")}
	tn, err := tunnelnode.New(tunnelnode.Env{
		ConfigDir: n.etc, StateDir: filepath.Join(f.dir, id, "state"), UnitDir: filepath.Join(f.dir, id, "units"),
		BaftBin: flowBaft, PairBin: flowPair, Settle: 20 * time.Millisecond, System: n.sys,
	})
	if err != nil {
		t.Fatal(err)
	}
	n.tn = tn
	a, err := New(Config{
		BCCURL: url, NodeID: id, Token: token, BCCJobKey: jobKey, StateDir: filepath.Join(f.dir, id, "agent"),
		Service: "baft", System: n.sys, SettleTime: 10 * time.Millisecond, Tunnel: tn,
		BinDir: filepath.Join(f.dir, id, "bin"), ReleaseState: filepath.Join(f.dir, id, "rs.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	n.agent = a
	return n
}

func (f *flow) api(method, path string, body any) (int, []byte) {
	f.t.Helper()
	var rd *strings.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	} else {
		rd = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Authorization", "Bearer admin")
	rr := httptest.NewRecorder()
	f.h.ServeHTTP(rr, req)
	return rr.Code, rr.Body.Bytes()
}

func (f *flow) create(req bcc.TunnelRequest) bcc.Tunnel {
	f.t.Helper()
	code, body := f.api("POST", "/api/tunnels", req)
	if code != http.StatusAccepted {
		f.t.Fatalf("create tunnel = %d %s", code, body)
	}
	var tn bcc.Tunnel
	json.Unmarshal(body, &tn)
	return tn
}

func (f *flow) tunnel(id string) bcc.Tunnel {
	f.t.Helper()
	_, body := f.api("GET", "/api/tunnels?id="+id, nil)
	var tn bcc.Tunnel
	json.Unmarshal(body, &tn)
	return tn
}

func (f *flow) plan() bcc.TunnelRequest {
	return bcc.TunnelRequest{EXNode: "ex-1", IRNode: "ir-1", PublicAddress: "127.0.0.1", Port: f.exPort, RouteListen: f.irRoute}
}

// run lets both agents work until the tunnel is final (or gives up).
func (f *flow) run(id string, agents ...*flowNode) bcc.Tunnel {
	f.t.Helper()
	if len(agents) == 0 {
		agents = []*flowNode{f.ex, f.ir}
	}
	for i := 0; i < 40; i++ {
		for _, n := range agents {
			if _, err := n.agent.RunOnce(context.Background()); err != nil {
				f.t.Fatalf("%s RunOnce: %v", n.id, err)
			}
		}
		switch tn := f.tunnel(id); tn.Phase {
		case bcc.TunnelActive, bcc.TunnelRolledBack, bcc.TunnelRollbackFailed:
			return tn
		}
	}
	f.t.Fatalf("tunnel %s did not settle: %+v", id, f.tunnel(id))
	return bcc.Tunnel{}
}

func (f *flow) noSecretsInBCC(extra ...string) {
	f.t.Helper()
	_, jobs := f.api("GET", "/api/jobs", nil)
	state, _ := os.ReadFile(f.store.Path())
	audit, _ := os.ReadFile(f.store.Path() + ".audit.jsonl")
	for name, b := range map[string][]byte{"/api/jobs": jobs, "state": state, "audit": audit} {
		for _, s := range append([]string{"BAFTPAIR1:", "BAFTREPLY1:"}, extra...) {
			if strings.Contains(string(b), s) {
				f.t.Errorf("%s still holds a pairing secret (%s)", name, s)
			}
		}
	}
}

func TestBCCBuildsATunnelOnTwoServers(t *testing.T) {
	f := newFlow(t)
	tn := f.create(f.plan())
	if tn.Phase != bcc.TunnelPreparingEX || !strings.HasPrefix(tn.ID, "tun-") {
		t.Fatalf("new tunnel: %+v", tn)
	}
	done := f.run(tn.ID)
	if done.Phase != bcc.TunnelActive || done.Error != "" {
		t.Fatalf("tunnel ended %s: %s", done.Phase, done.Error)
	}
	for _, n := range []*flowNode{f.ex, f.ir} {
		if _, err := os.Stat(filepath.Join(n.etc, "baft.yaml")); err != nil {
			t.Fatalf("%s has no live config: %v", n.id, err)
		}
		if !n.sys.active || !n.sys.enabled {
			t.Fatalf("%s service not running", n.id)
		}
	}
	ex, _ := os.ReadFile(filepath.Join(f.ex.etc, "baft.yaml"))
	ir, _ := os.ReadFile(filepath.Join(f.ir.etc, "baft.yaml"))
	if !strings.Contains(string(ex), `"listener"`) || !strings.Contains(string(ir), `"dialer"`) {
		t.Fatalf("configs are not a pair:\n%s\n%s", ex, ir)
	}
	f.noSecretsInBCC()
	// The deployment keeps its evidence: two health results and two observed
	// states, each proved by the node itself and equal to the plan.
	ev := f.tunnel(tn.ID).Evidence
	var health, observed int
	for _, e := range ev {
		if !e.OK {
			t.Errorf("evidence %s on %s is not OK: %v", e.Step, e.Node, e.Problems)
		}
		switch e.Step {
		case "health":
			health++
		case "observe":
			observed++
			if !strings.Contains(e.Detail, `"generation":1`) || !strings.Contains(e.Detail, `"config_sha256"`) {
				t.Errorf("observed evidence is thin: %s", e.Detail)
			}
		}
	}
	if health != 2 || observed != 2 {
		t.Fatalf("evidence: %d health, %d observe: %+v", health, observed, ev)
	}
	for _, id := range []string{"ex-1", "ir-1"} {
		if n, _ := f.store.GetNode(id); n.AppliedGeneration != 1 {
			t.Errorf("%s applied generation %d, want 1", id, n.AppliedGeneration)
		}
	}
	st := f.store.ListJobs()
	var types []string
	for _, j := range st {
		types = append(types, j.Type)
	}
	if len(types) != 10 {
		t.Fatalf("jobs run: %v", types)
	}
	entries, _ := os.ReadFile(f.store.Path() + ".audit.jsonl")
	for _, want := range []string{"tunnel.create", "tunnel.active"} {
		if !strings.Contains(string(entries), want) {
			t.Errorf("audit log lacks %s", want)
		}
	}
}

func TestFailureOnOneSideRollsBothSidesBack(t *testing.T) {
	f := newFlow(t)
	f.ir.sys.failRestart = true // the IR's service will not start
	tn := f.create(f.plan())
	done := f.run(tn.ID)
	if done.Phase != bcc.TunnelRolledBack || !strings.Contains(done.Error, "ir-1") {
		t.Fatalf("ended %s: %s", done.Phase, done.Error)
	}
	for _, n := range []*flowNode{f.ex, f.ir} {
		if _, err := os.Stat(filepath.Join(n.etc, "baft.yaml")); err == nil {
			t.Fatalf("%s kept a config after the rollback", n.id)
		}
		if n.sys.active || n.sys.enabled {
			t.Fatalf("%s service left running or enabled", n.id)
		}
	}
	f.noSecretsInBCC()
	// Nodes are free again for a new attempt.
	f.ir.sys.failRestart = false
	if again := f.run(f.create(f.plan()).ID); again.Phase != bcc.TunnelActive {
		t.Fatalf("retry ended %s: %s", again.Phase, again.Error)
	}
}

func TestFailedChangeRestoresTheWorkingTunnel(t *testing.T) {
	f := newFlow(t)
	first := f.run(f.create(f.plan()).ID)
	if first.Phase != bcc.TunnelActive {
		t.Fatalf("first: %s %s", first.Phase, first.Error)
	}
	exBefore, _ := os.ReadFile(filepath.Join(f.ex.etc, "baft.yaml"))
	irBefore, _ := os.ReadFile(filepath.Join(f.ir.etc, "baft.yaml"))

	p := f.plan()
	p.Port = freeFlowPort(t)
	accept(t, p.Port)
	f.ex.sys.failRestart = true // the new listener will not start
	second := f.run(f.create(p).ID)
	if second.Phase != bcc.TunnelRolledBack {
		t.Fatalf("second: %s %s", second.Phase, second.Error)
	}
	f.ex.sys.failRestart = false
	exAfter, _ := os.ReadFile(filepath.Join(f.ex.etc, "baft.yaml"))
	irAfter, _ := os.ReadFile(filepath.Join(f.ir.etc, "baft.yaml"))
	if string(exAfter) != string(exBefore) || string(irAfter) != string(irBefore) {
		t.Fatal("the previous tunnel config was not restored")
	}
	if f.tunnel(first.ID).Phase != bcc.TunnelActive {
		t.Fatal("the working tunnel lost its active state")
	}
}

func TestNewTunnelSupersedesTheOldOne(t *testing.T) {
	f := newFlow(t)
	first := f.run(f.create(f.plan()).ID)
	p := f.plan()
	p.Port = freeFlowPort(t)
	accept(t, p.Port)
	second := f.run(f.create(p).ID)
	if second.Phase != bcc.TunnelActive {
		t.Fatalf("second: %s %s", second.Phase, second.Error)
	}
	if got := f.tunnel(first.ID).Phase; got != bcc.TunnelSuperseded {
		t.Fatalf("first tunnel is %s", got)
	}
}

func TestCancelMidwayRollsBackOnlyTouchedNodes(t *testing.T) {
	f := newFlow(t)
	tn := f.create(f.plan())
	// Only the EX agent runs: it prepares and BCC queues the IR's step.
	if _, err := f.ex.agent.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	mid := f.tunnel(tn.ID)
	if mid.Phase != bcc.TunnelPreparingIR {
		t.Fatalf("phase %s", mid.Phase)
	}
	code, body := f.api("POST", "/api/tunnels/cancel", map[string]string{"id": tn.ID, "reason": "changed my mind"})
	if code != 200 {
		t.Fatalf("cancel = %d %s", code, body)
	}
	// The IR never saw the queued prepare; the EX rolls its prepare back.
	done := f.run(tn.ID, f.ex)
	if done.Phase != bcc.TunnelRolledBack || !strings.Contains(done.Error, "changed my mind") {
		t.Fatalf("ended %s: %s", done.Phase, done.Error)
	}
	if _, err := os.Stat(filepath.Join(f.ir.tn.StateDir, "tunnels")); err == nil {
		t.Fatal("the untouched IR was given a change")
	}
	for _, j := range f.store.ListJobs() {
		if j.NodeID == "ir-1" && j.Status != "failed" {
			t.Fatalf("IR job %s is %s", j.ID, j.Status)
		}
	}
	f.noSecretsInBCC()
}

func TestBadPlansAreRefusedBeforeAnythingRuns(t *testing.T) {
	f := newFlow(t)
	bad := map[string]func(*bcc.TunnelRequest){
		"same node":       func(r *bcc.TunnelRequest) { r.IRNode = "ex-1" },
		"EX not foreign":  func(r *bcc.TunnelRequest) { r.EXNode, r.IRNode = "ir-1", "ex-1" },
		"unknown node":    func(r *bcc.TunnelRequest) { r.IRNode = "nope" },
		"hostname target": func(r *bcc.TunnelRequest) { r.Target = "example.com:443" },
		"open route":      func(r *bcc.TunnelRequest) { r.RouteListen = "0.0.0.0:1443" },
		"shell address":   func(r *bcc.TunnelRequest) { r.PublicAddress = "1.2.3.4;id" },
	}
	for name, mutate := range bad {
		req := f.plan()
		mutate(&req)
		if code, _ := f.api("POST", "/api/tunnels", req); code != http.StatusBadRequest {
			t.Errorf("%s: status %d", name, code)
		}
	}
	if len(f.store.ListJobs()) != 0 {
		t.Fatal("a refused plan queued jobs")
	}
	first := f.create(f.plan())
	if code, _ := f.api("POST", "/api/tunnels", f.plan()); code != http.StatusBadRequest {
		t.Fatalf("a second tunnel on busy nodes = %d", code)
	}
	if code, _ := f.api("POST", "/api/tunnels/cancel", map[string]string{"id": "tun-nope"}); code != http.StatusBadRequest {
		t.Fatalf("cancel unknown = %d", code)
	}
	_ = first
}

func TestTunnelEndpointsNeedAdminAndJobsStayHidden(t *testing.T) {
	f := newFlow(t)
	req := httptest.NewRequest("GET", "/api/tunnels", nil)
	rr := httptest.NewRecorder()
	f.h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list = %d", rr.Code)
	}
}

func (f *flow) planOf(req bcc.TunnelRequest) (int, bcc.Plan, []byte) {
	f.t.Helper()
	code, body := f.api("POST", "/api/tunnels/plan", req)
	var pl bcc.Plan
	json.Unmarshal(body, &pl)
	return code, pl, body
}

func TestPlanIsReviewedThenDeployedByItsHash(t *testing.T) {
	f := newFlow(t)
	// Agents have not contacted BCC: the plan says so and cannot be deployed.
	code, pl, _ := f.planOf(f.plan())
	if code != 200 || pl.OK || pl.Hash == "" {
		t.Fatalf("plan before any agent contact: %d ok=%v", code, pl.OK)
	}
	var contact int
	for _, g := range pl.Gates {
		if strings.HasPrefix(g.Name, "agent contact") && g.Status == "FAIL" {
			contact++
		}
	}
	if contact != 2 {
		t.Fatalf("gates: %+v", pl.Gates)
	}
	req := f.plan()
	req.PlanHash = pl.Hash
	if code, body := f.api("POST", "/api/tunnels", req); code != http.StatusConflict || !strings.Contains(string(body), "stale or no longer passes") {
		t.Fatalf("deploying a failing plan = %d %s", code, body)
	}
	if len(f.store.ListJobs()) != 0 {
		t.Fatal("a refused deploy queued jobs")
	}

	// Once both agents have contacted BCC the same request plans OK, and the
	// plan is deterministic.
	for _, n := range []*flowNode{f.ex, f.ir} {
		if _, err := n.agent.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	_, ok1, _ := f.planOf(f.plan())
	_, ok2, _ := f.planOf(f.plan())
	if !ok1.OK || ok1.Hash != ok2.Hash {
		t.Fatalf("plan not OK or not deterministic: %v %s %s", ok1.OK, ok1.Hash, ok2.Hash)
	}
	if ok1.Nodes[0].GenerationTo != 1 || len(ok1.Verify) == 0 || len(ok1.Rollback) == 0 || len(ok1.Steps) == 0 {
		t.Fatalf("plan content: %+v", ok1)
	}
	// A different request has a different hash: a reviewed plan cannot be
	// used to deploy something else.
	other := f.plan()
	other.Port = freeFlowPort(t)
	req = other
	req.PlanHash = ok1.Hash
	if code, _ := f.api("POST", "/api/tunnels", req); code != http.StatusConflict {
		t.Fatalf("a hash from another plan was accepted: %d", code)
	}
	// The reviewed plan deploys, and the result matches it.
	req = f.plan()
	req.PlanHash = ok1.Hash
	code, body := f.api("POST", "/api/tunnels", req)
	if code != http.StatusAccepted {
		t.Fatalf("deploy by hash = %d %s", code, body)
	}
	var tn bcc.Tunnel
	json.Unmarshal(body, &tn)
	if tn.PlanHash != ok1.Hash {
		t.Fatal("the deployment does not record the plan it came from")
	}
	if done := f.run(tn.ID); done.Phase != bcc.TunnelActive {
		t.Fatalf("ended %s: %s", done.Phase, done.Error)
	}
	// The next plan starts from the generation BCC verified.
	_, next, _ := f.planOf(f.plan())
	if next.Nodes[0].GenerationFrom != 1 || next.Nodes[0].GenerationTo != 2 || next.Replaces != tn.ID {
		t.Fatalf("next plan: %+v replaces=%s", next.Nodes[0], next.Replaces)
	}
	entries, _ := os.ReadFile(f.store.Path() + ".audit.jsonl")
	if !strings.Contains(string(entries), "tunnel.plan") || !strings.Contains(string(entries), ok1.Hash) {
		t.Error("planning and the plan hash are not in the audit log")
	}
}
