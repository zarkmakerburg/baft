package bcc

import (
	"strings"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/tunnelnode"
)

func multiInstanceStore(t *testing.T) *Store {
	t.Helper()
	s := tunnelStore(t)
	for id, role := range map[string]string{"ex-2": "foreign", "ir-2": "worker"} {
		if _, err := s.UpsertNode(Node{ID: id, Address: "203.0.113.8:22", Role: role}, "tok-"+id); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func scopedReq(instance, ex, ir string, port int, route, exMetrics, irMetrics string) TunnelRequest {
	return TunnelRequest{
		InstanceID: instance, EXNode: ex, IRNode: ir, Port: port,
		RouteListen: route, EXMetricsListen: exMetrics, IRMetricsListen: irMetrics,
	}
}

func TestScopedTunnelsCanShareIRWithIndependentResources(t *testing.T) {
	s := multiInstanceStore(t)
	now := time.Now()
	a := scopedReq("ir-main-de", "ex-1", "ir-1", 8443, "127.0.0.1:1443", "127.0.0.1:9201", "127.0.0.1:9301")
	b := scopedReq("ir-main-nl", "ex-2", "ir-1", 8443, "127.0.0.1:1444", "127.0.0.1:9201", "127.0.0.1:9302")
	if _, err := s.CreateTunnel(a, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTunnel(b, now); err != nil {
		t.Fatalf("sibling IR instance was blocked: %v", err)
	}
	if got := len(s.ListTunnels()); got != 2 {
		t.Fatalf("tunnels=%d, want 2", got)
	}
}

func TestScopedTunnelsCanShareEXWithIndependentResources(t *testing.T) {
	s := multiInstanceStore(t)
	now := time.Now()
	a := scopedReq("ir-main-de", "ex-1", "ir-1", 8443, "127.0.0.1:1443", "127.0.0.1:9201", "127.0.0.1:9301")
	b := scopedReq("ir-backup-de", "ex-1", "ir-2", 8444, "127.0.0.1:1443", "127.0.0.1:9202", "127.0.0.1:9301")
	if _, err := s.CreateTunnel(a, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTunnel(b, now); err != nil {
		t.Fatalf("sibling EX instance was blocked: %v", err)
	}
}

func TestScopedTunnelResourceConflictsAreRejected(t *testing.T) {
	tests := []struct {
		name string
		a, b TunnelRequest
		want string
	}{
		{
			name: "same IR route listener",
			a:    scopedReq("ir-main-de", "ex-1", "ir-1", 8443, "127.0.0.1:1443", "127.0.0.1:9201", "127.0.0.1:9301"),
			b:    scopedReq("ir-main-nl", "ex-2", "ir-1", 8443, "127.0.0.1:1443", "127.0.0.1:9201", "127.0.0.1:9302"),
			want: "IR route listener",
		},
		{
			name: "same EX listener port",
			a:    scopedReq("ir-main-de", "ex-1", "ir-1", 8443, "127.0.0.1:1443", "127.0.0.1:9201", "127.0.0.1:9301"),
			b:    scopedReq("ir-backup-de", "ex-1", "ir-2", 8443, "127.0.0.1:1444", "127.0.0.1:9202", "127.0.0.1:9302"),
			want: "EX listener port",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := multiInstanceStore(t)
			now := time.Now()
			if _, err := s.CreateTunnel(tc.a, now); err != nil {
				t.Fatal(err)
			}
			if _, err := s.CreateTunnel(tc.b, now); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("conflict=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestScopedSiblingActivationDoesNotSupersede(t *testing.T) {
	s := multiInstanceStore(t)
	now := time.Now()
	a := Tunnel{ID: "tun-a", InstanceID: "ir-main-de", EXNode: "ex-1", IRNode: "ir-1", Phase: TunnelActive}
	b := Tunnel{ID: "tun-b", InstanceID: "ir-main-nl", EXNode: "ex-2", IRNode: "ir-1", Phase: TunnelFinalizingEX}
	s.mu.Lock()
	if s.st.Tunnels == nil {
		s.st.Tunnels = map[string]Tunnel{}
	}
	s.st.Tunnels[a.ID] = a
	s.st.Tunnels[b.ID] = b
	s.nextAfterFinalizeLocked(&b, now)
	s.st.Tunnels[b.ID] = b
	gotA := s.st.Tunnels[a.ID]
	gotB := s.st.Tunnels[b.ID]
	s.mu.Unlock()
	if gotA.Phase != TunnelActive || gotB.Phase != TunnelActive {
		t.Fatalf("sibling phases: a=%s b=%s", gotA.Phase, gotB.Phase)
	}
}

func TestGenerationIsScopedPerInstance(t *testing.T) {
	n := Node{AppliedGeneration: 3, AppliedGenerations: map[string]int{"de": 4, "tr": 7}}
	if g := expectedGeneration(n, "de"); g.From != 4 || g.To != 5 || g.Bootstrap {
		t.Fatalf("de generation %+v", g)
	}
	if g := expectedGeneration(n, "tr"); g.From != 7 || g.To != 8 || g.Bootstrap {
		t.Fatalf("tr generation %+v", g)
	}
	if g := expectedGeneration(n, "new"); !g.Bootstrap {
		t.Fatalf("new instance is not bootstrap: %+v", g)
	}
	setAppliedGeneration(&n, "de", 5)
	if n.AppliedGenerations["de"] != 5 || n.AppliedGenerations["tr"] != 7 || n.AppliedGeneration != 3 {
		t.Fatalf("generation scopes crossed: %+v", n)
	}
}

func TestInstanceMismatchFailsObserveAndDrift(t *testing.T) {
	tun := Tunnel{
		ID: "tun-a", InstanceID: "ir-main-de", EXNode: "ex-1", IRNode: "ir-1",
		RouteID: "service-main", RouteListen: "127.0.0.1:1443", PublicAddress: "203.0.113.7", Port: 8443,
		ExpectedGen: map[string]GenExpect{"ir-1": {From: 0, To: 1, Bootstrap: true}},
	}
	o := tunnelnode.Observed{
		TunnelID: tun.ID, InstanceID: "wrong", Role: tunnelnode.RoleIR, Phase: tunnelnode.PhaseCommitted,
		Generation: 1, PreviousGeneration: 0, NodeGeneration: 1, ServiceActive: true, UnitMatches: true,
		ConfigSHA256: strings.Repeat("a", 64), UnitSHA256: strings.Repeat("b", 64), MarkerSHA256: strings.Repeat("c", 64),
		Managed: true, MarkerTunnelID: tun.ID, MarkerGeneration: 1, MarkerConfigMatches: true, MarkerUnitMatches: true,
		ConfigRole: "dialer", RouteID: tun.RouteID, RouteListen: tun.RouteListen, PeerAddress: "203.0.113.7:8443",
	}
	if p := verifyObserved(tun, "ir-1", tunnelnode.RoleIR, o); len(p) == 0 || !strings.Contains(strings.Join(p, " "), "instance id") {
		t.Fatalf("observe accepted wrong instance: %v", p)
	}

	l := tunnelnode.Live{
		InstanceID: "wrong", ConfigPresent: true, UnitPresent: true, ConfigLoads: true,
		MarkerPresent: true, MarkerManagedBy: "baft", MarkerTunnelID: tun.ID, MarkerGeneration: 1,
		NodeGeneration: 1, MarkerConfigMatches: true, MarkerUnitMatches: true, ServiceActive: true,
		ConfigRole: "dialer", RouteID: tun.RouteID, RouteListen: tun.RouteListen, PeerAddress: "203.0.113.7:8443",
	}
	got := classifyLive(tun, "ir-1", tunnelnode.RoleIR, l)
	if got.State != DriftDrifted || !strings.Contains(strings.Join(got.Problems, " "), "instance id") {
		t.Fatalf("drift accepted wrong instance: %+v", got)
	}
}

func TestScopedTunnelJobsCarryInstanceIdentity(t *testing.T) {
	s := multiInstanceStore(t)
	now := time.Now()
	req := scopedReq("ir-main-de", "ex-1", "ir-1", 8443, "127.0.0.1:1443", "127.0.0.1:9201", "127.0.0.1:9301")
	tn, err := s.CreateTunnel(req, now)
	if err != nil {
		t.Fatal(err)
	}
	exJobs := pullAll(t, s, "ex-1")
	if len(exJobs) != 1 {
		t.Fatalf("EX jobs: %+v", exJobs)
	}
	if got := exJobs[0].Params["instance_id"]; got != req.InstanceID {
		t.Fatalf("EX instance=%q", got)
	}
	if got := exJobs[0].Params["metrics_listen"]; got != req.EXMetricsListen {
		t.Fatalf("EX metrics=%q", got)
	}
	if err := s.AckJobOutput("ex-1", "tok-ex-1", exJobs[0].ID, "succeeded", "pairing code issued", testPairCode); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdvanceTunnels(now); err != nil {
		t.Fatal(err)
	}
	irJobs := pullAll(t, s, "ir-1")
	if len(irJobs) != 1 || irJobs[0].Type != JobTunnelPrepareIR {
		t.Fatalf("IR jobs: %+v", irJobs)
	}
	if got := irJobs[0].Params["instance_id"]; got != tn.InstanceID {
		t.Fatalf("IR instance=%q", got)
	}
	if got := irJobs[0].Params["metrics_listen"]; got != req.IRMetricsListen {
		t.Fatalf("IR metrics=%q", got)
	}
}

func TestNodeUpsertPreservesInstanceGenerations(t *testing.T) {
	s := multiInstanceStore(t)
	s.mu.Lock()
	n := s.st.Nodes["ir-1"]
	n.AppliedGeneration = 2
	n.AppliedGenerations = map[string]int{"de": 4, "tr": 7}
	s.st.Nodes["ir-1"] = n
	s.mu.Unlock()

	if _, err := s.UpsertNode(Node{ID: "ir-1", Alias: "renamed", Address: "203.0.113.9:22", Role: "worker"}, ""); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetNode("ir-1")
	if got.AppliedGeneration != 2 || got.AppliedGenerations["de"] != 4 || got.AppliedGenerations["tr"] != 7 {
		t.Fatalf("upsert lost generations: %+v", got)
	}
}

func TestScopedCertRotationJobCarriesInstanceIdentity(t *testing.T) {
	s := multiInstanceStore(t)
	now := time.Now()
	s.mu.Lock()
	if s.st.Tunnels == nil {
		s.st.Tunnels = map[string]Tunnel{}
	}
	s.st.Tunnels["tun-a"] = Tunnel{
		ID: "tun-a", InstanceID: "ir-main-de", EXNode: "ex-1", IRNode: "ir-1",
		Phase: TunnelActive,
	}
	s.mu.Unlock()
	rot, err := s.StartCertRotation(CertRotationRequest{TunnelID: "tun-a"}, now, AuditEntry{Timestamp: now.UTC(), Actor: "test", Action: "cert.rotation.start", Outcome: "success"})
	if err != nil {
		t.Fatal(err)
	}
	jobs := pullAll(t, s, "ex-1")
	if len(jobs) != 1 || jobs[0].Type != "tunnel_cert_prepare_ex" {
		t.Fatalf("rotation jobs: %+v", jobs)
	}
	if jobs[0].Params["instance_id"] != "ir-main-de" || rot.InstanceID != "ir-main-de" {
		t.Fatalf("rotation lost instance: rot=%+v job=%+v", rot, jobs[0].Params)
	}
}

func TestScopedTunnelRefusesImplicitLegacyMigration(t *testing.T) {
	s := multiInstanceStore(t)
	now := time.Now()
	legacy, err := s.CreateTunnel(TunnelRequest{EXNode: "ex-1", IRNode: "ir-1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	legacy.Phase = TunnelActive
	s.st.Tunnels[legacy.ID] = legacy
	s.mu.Unlock()
	req := scopedReq("ir-main-de", "ex-1", "ir-1", 8444, "127.0.0.1:1444", "127.0.0.1:9202", "127.0.0.1:9302")
	if _, err := s.CreateTunnel(req, now); err == nil || !strings.Contains(err.Error(), "legacy singleton") {
		t.Fatalf("implicit legacy migration was not rejected: %v", err)
	}
}
