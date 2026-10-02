package bcc

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/tunnelnode"
)

func goodLive(t Tunnel, role string) tunnelnode.Live {
	l := tunnelnode.Live{
		ConfigPresent: true, UnitPresent: true, ConfigLoads: true, ConfigSHA256: strings.Repeat("a", 64), UnitSHA256: strings.Repeat("b", 64), MarkerSHA256: strings.Repeat("c", 64),
		MarkerPresent: true, MarkerManagedBy: "baft", MarkerTunnelID: t.ID, MarkerGeneration: 4,
		MarkerConfigMatches: true, MarkerUnitMatches: true, NodeGeneration: 4, ServiceActive: true, RouteID: t.RouteID,
	}
	if role == tunnelnode.RoleIR {
		l.ConfigRole, l.RouteListen, l.PeerAddress = "dialer", t.RouteListen, fmt.Sprintf("%s:%d", t.PublicAddress, t.Port)
	} else {
		l.ConfigRole, l.Listen, l.Target = "listener", fmt.Sprintf("0.0.0.0:%d", t.Port), t.Target
	}
	return l
}

func activeTunnel(t *testing.T, s *Store, now time.Time) Tunnel {
	t.Helper()
	tn := driveToObserve(t, s, now)
	ackObserved(t, s, "ir-1", goodObserved(tn, tunnelnode.RoleIR), now)
	ackObserved(t, s, "ex-1", goodObserved(tn, tunnelnode.RoleEX), now)
	for _, node := range []string{"ir-1", "ex-1"} {
		j := pullAll(t, s, node)
		s.AckJob(node, "tok-"+node, j[0].ID, "succeeded", "finalized")
		s.AdvanceTunnels(now)
	}
	got, _ := s.GetTunnel(tn.ID)
	if got.Phase != TunnelActive {
		t.Fatalf("phase %s", got.Phase)
	}
	return got
}

func TestClassifyLiveStates(t *testing.T) {
	tn := Tunnel{ID: "tun-x", RouteID: "service-main", RouteListen: "127.0.0.1:1443", PublicAddress: "203.0.113.5", Port: 8443, Target: "127.0.0.1:2443", ObservedGen: map[string]int{"ir-1": 4},
		Digests: map[string]NodeDigests{"ir-1": {Config: strings.Repeat("a", 64), Unit: strings.Repeat("b", 64), Marker: strings.Repeat("c", 64)}}}
	cases := map[string]struct {
		mutate func(*tunnelnode.Live)
		want   string
	}{
		"in sync":            {func(l *tunnelnode.Live) {}, DriftInSync},
		"config deleted":     {func(l *tunnelnode.Live) { l.ConfigPresent = false }, DriftMissing},
		"unit deleted":       {func(l *tunnelnode.Live) { l.UnitPresent = false }, DriftMissing},
		"no marker":          {func(l *tunnelnode.Live) { l.MarkerPresent = false }, DriftUnmanaged},
		"foreign marker":     {func(l *tunnelnode.Live) { l.MarkerManagedBy = "other" }, DriftUnmanaged},
		"other tunnel":       {func(l *tunnelnode.Live) { l.MarkerTunnelID = "tun-y" }, DriftDrifted},
		"config edited":      {func(l *tunnelnode.Live) { l.MarkerConfigMatches = false }, DriftDrifted},
		"unit edited":        {func(l *tunnelnode.Live) { l.MarkerUnitMatches = false }, DriftDrifted},
		"service stopped":    {func(l *tunnelnode.Live) { l.ServiceActive = false }, DriftDrifted},
		"generation moved":   {func(l *tunnelnode.Live) { l.MarkerGeneration, l.NodeGeneration = 5, 5 }, DriftDrifted},
		"node counter moved": {func(l *tunnelnode.Live) { l.NodeGeneration = 9 }, DriftDrifted},
		"wrong peer":         {func(l *tunnelnode.Live) { l.PeerAddress = "198.51.100.1:1" }, DriftDrifted},
		"config unloadable":  {func(l *tunnelnode.Live) { l.ConfigLoads = false }, DriftDrifted},
		// Coupled tamper: the marker was rewritten to agree with the edit, so
		// every marker comparison passes; only BCC's own digest can tell.
		"config + marker coupled": {func(l *tunnelnode.Live) {
			l.ConfigSHA256 = strings.Repeat("d", 64)
			l.MarkerSHA256 = strings.Repeat("e", 64)
		}, DriftDrifted},
		"unit + marker coupled": {func(l *tunnelnode.Live) {
			l.UnitSHA256 = strings.Repeat("d", 64)
			l.MarkerSHA256 = strings.Repeat("e", 64)
		}, DriftDrifted},
		"config edited, marker untouched but matching": {func(l *tunnelnode.Live) { l.ConfigSHA256 = strings.Repeat("d", 64) }, DriftDrifted},
		"marker metadata changed":                      {func(l *tunnelnode.Live) { l.MarkerSHA256 = strings.Repeat("e", 64) }, DriftDrifted},
	}
	for name, c := range cases {
		l := goodLive(tn, tunnelnode.RoleIR)
		c.mutate(&l)
		got := classifyLive(tn, "ir-1", tunnelnode.RoleIR, l)
		if got.State != c.want || (c.want != DriftInSync && len(got.Problems) == 0) {
			t.Errorf("%s: %+v, want %s with a reason", name, got, c.want)
		}
	}
}

func TestNoVerifiedBaselineIsNeverInSync(t *testing.T) {
	tn := Tunnel{ID: "tun-x", RouteID: "service-main", RouteListen: "127.0.0.1:1443", PublicAddress: "203.0.113.5", Port: 8443, ObservedGen: map[string]int{"ir-1": 4}}
	got := classifyLive(tn, "ir-1", tunnelnode.RoleIR, goodLive(tn, tunnelnode.RoleIR))
	if got.State != DriftUnknown {
		t.Fatalf("without BCC-held digests: %+v", got)
	}
}

func TestActivationStoresTheDigestsBCCVerified(t *testing.T) {
	s := tunnelStore(t)
	tn := activeTunnel(t, s, time.Now())
	for _, node := range []string{"ir-1", "ex-1"} {
		d := tn.Digests[node]
		if d.Config != strings.Repeat("a", 64) || d.Unit != strings.Repeat("b", 64) || d.Marker != strings.Repeat("c", 64) {
			t.Fatalf("%s digests %+v", node, d)
		}
	}
}

func answerDrift(t *testing.T, s *Store, node string, l *tunnelnode.Live, now time.Time) {
	t.Helper()
	j := pullAll(t, s, node)
	if len(j) != 1 || j[0].Type != JobTunnelInspect {
		t.Fatalf("%s: expected an inspect job, got %+v", node, j)
	}
	if l == nil {
		s.AckJob(node, "tok-"+node, j[0].ID, "failed", "boom")
	} else {
		b, _ := json.Marshal(l)
		s.AckJob(node, "tok-"+node, j[0].ID, "succeeded", string(b))
	}
	s.AdvanceTunnels(now)
}

func TestDriftCheckReportsEachNodeAndTheWorstState(t *testing.T) {
	s := tunnelStore(t)
	now := time.Now()
	tn := activeTunnel(t, s, now)
	if _, err := s.StartDrift(tn.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartDrift(tn.ID, now); err == nil {
		t.Fatal("a second check started while one is running")
	}
	ir, ex := goodLive(tn, tunnelnode.RoleIR), goodLive(tn, tunnelnode.RoleEX)
	ex.MarkerConfigMatches = false
	answerDrift(t, s, "ir-1", &ir, now)
	if got, _ := s.GetTunnel(tn.ID); got.Drift != nil {
		t.Fatal("report published before every node answered")
	}
	answerDrift(t, s, "ex-1", &ex, now)
	got, _ := s.GetTunnel(tn.ID)
	if got.Drift == nil || got.Drift.State != DriftDrifted || got.Drift.Nodes["ir-1"].State != DriftInSync || got.Drift.Nodes["ex-1"].State != DriftDrifted {
		t.Fatalf("report %+v", got.Drift)
	}
	if len(got.DriftJobs) != 0 {
		t.Fatal("check still marked running")
	}
	// Fixed: a new check goes back to IN_SYNC.
	if _, err := s.StartDrift(tn.ID, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	ex.MarkerConfigMatches = true
	answerDrift(t, s, "ir-1", &ir, now.Add(time.Minute))
	answerDrift(t, s, "ex-1", &ex, now.Add(time.Minute))
	if got, _ := s.GetTunnel(tn.ID); got.Drift.State != DriftInSync {
		t.Fatalf("after the fix: %+v", got.Drift)
	}
}

func TestDriftCheckNeverClaimsWhatItDidNotSee(t *testing.T) {
	s := tunnelStore(t)
	now := time.Now()
	tn := activeTunnel(t, s, now)
	s.StartDrift(tn.ID, now)
	ir := goodLive(tn, tunnelnode.RoleIR)
	answerDrift(t, s, "ir-1", &ir, now)
	answerDrift(t, s, "ex-1", nil, now) // failed
	got, _ := s.GetTunnel(tn.ID)
	if got.Drift.State != DriftUnknown || got.Drift.Nodes["ex-1"].State != DriftUnknown {
		t.Fatalf("failed node: %+v", got.Drift)
	}
	// A node that never answers becomes UNKNOWN after the timeout.
	s.StartDrift(tn.ID, now)
	pullAll(t, s, "ir-1")
	s.AdvanceTunnels(now.Add(driftTimeout + time.Second))
	got, _ = s.GetTunnel(tn.ID)
	if got.Drift.State != DriftUnknown || len(got.DriftJobs) != 0 {
		t.Fatalf("timeout: %+v", got.Drift)
	}
}

func TestDriftCheckOnlyForActiveTunnelsAndAutomaticWhenEnabled(t *testing.T) {
	s := tunnelStore(t)
	now := time.Now()
	tn := newTunnel(t, s, now)
	if _, err := s.StartDrift(tn.ID, now); err == nil {
		t.Fatal("a tunnel still being built can be drift-checked")
	}
	if _, err := s.StartDrift("tun-none", now); err == nil {
		t.Fatal("unknown tunnel accepted")
	}
	s2 := tunnelStore(t)
	act := activeTunnel(t, s2, now)
	s2.AdvanceTunnels(now.Add(time.Hour))
	if got, _ := s2.GetTunnel(act.ID); len(got.DriftJobs) != 0 {
		t.Fatal("automatic check ran while disabled")
	}
	s2.DriftEvery = time.Hour
	s2.AdvanceTunnels(now.Add(2 * time.Hour))
	if got, _ := s2.GetTunnel(act.ID); len(got.DriftJobs) != 2 {
		t.Fatalf("automatic check did not start: %+v", got.DriftJobs)
	}
	ir, ex := goodLive(act, tunnelnode.RoleIR), goodLive(act, tunnelnode.RoleEX)
	answerDrift(t, s2, "ir-1", &ir, now.Add(2*time.Hour))
	answerDrift(t, s2, "ex-1", &ex, now.Add(2*time.Hour))
	s2.AdvanceTunnels(now.Add(2*time.Hour + time.Minute))
	if got, _ := s2.GetTunnel(act.ID); len(got.DriftJobs) != 0 || got.Drift.State != DriftInSync {
		t.Fatalf("a fresh result was checked again at once: %+v", got)
	}
}
