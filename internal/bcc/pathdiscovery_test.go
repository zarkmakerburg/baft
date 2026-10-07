package bcc

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func newGenericDiscoveryStore(t *testing.T, ids ...string) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		ip := fmt.Sprintf("192.0.2.%d", i+1)
		if _, err := s.UpsertNode(Node{ID: id, Address: ip + ":22", PathIPv4: ip, Role: "worker"}, "tok-"+id); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestPathDiscoveryAutomaticallyPlansGenericDirectedEdges(t *testing.T) {
	s := newGenericDiscoveryStore(t, "A", "B", "C")
	now := time.Now().UTC()
	d, err := s.StartPathDiscovery([]int{49152}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Inventory) != 3 {
		t.Fatalf("inventory jobs=%d want 3", len(d.Inventory))
	}
	for i, id := range []string{"A", "B", "C"} {
		inv := d.Inventory[id]
		out := fmt.Sprintf(`{"ipv4":["192.0.2.%d"]}`, i+1)
		if err := s.AckJobOutput(id, "tok-"+id, inv.JobID, "succeeded", "inventory complete", out); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AdvancePathDiscoveries(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	ds := s.ListPathDiscoveries()
	if len(ds) != 1 || ds[0].Phase != PathDiscoveryProbing {
		t.Fatalf("unexpected discovery state: %+v", ds)
	}
	if got := len(ds[0].ProbeIDs); got != 6 {
		t.Fatalf("planned edges=%d want 6", got)
	}

	active, queued := 0, 0
	for _, p := range s.ListPathProbes() {
		if p.SourceNode == p.DestinationNode {
			t.Fatalf("self edge was planned: %+v", p)
		}
		if p.Family != "4" || len(p.Candidates) != 1 || p.Candidates[0].Port != 49152 {
			t.Fatalf("unexpected generic probe: %+v", p)
		}
		if p.Phase == PathProbeQueued {
			queued++
		} else {
			active++
		}
	}
	if active != 4 || queued != 2 {
		t.Fatalf("active=%d queued=%d want 4/2", active, queued)
	}
}

func putGraphProbe(s *Store, id, src, dst, family, ip string, port int, class string, rtt int64, at time.Time) {
	s.st.PathProbes[id] = PathProbe{
		ID: id, SourceNode: src, DestinationNode: dst, Family: family, DestinationIP: ip,
		Phase: PathProbeComplete, RecommendedPort: port, CreatedAt: at, UpdatedAt: at,
		Candidates: []PathProbeCandidate{{Port: port, Class: class, ConnectRTTMS: rtt}},
	}
}

func recommendation(g PathGraph, src, dst string) (PathRecommendation, bool) {
	for _, r := range g.Recommendations {
		if r.SourceNode == src && r.DestinationNode == dst {
			return r, true
		}
	}
	return PathRecommendation{}, false
}

func TestPathGraphFindsGenericMultiHopWithoutTopologyNames(t *testing.T) {
	s := newGenericDiscoveryStore(t, "A", "B", "C")
	now := time.Now().UTC()
	s.mu.Lock()
	putGraphProbe(s, "ab", "A", "B", "4", "192.0.2.2", 443, PathFullData, 10, now)
	putGraphProbe(s, "bc", "B", "C", "6", "2001:db8::3", 8443, PathFullData, 12, now)
	putGraphProbe(s, "ac-bad", "A", "C", "4", "192.0.2.3", 443, PathByteCeiling, 2, now)
	s.mu.Unlock()

	g := s.PathGraph(now.Add(time.Minute))
	r, ok := recommendation(g, "A", "C")
	if !ok {
		t.Fatal("A->C recommendation missing")
	}
	if r.Direct || len(r.Hops) != 2 || r.Hops[0].DestinationNode != "B" || r.Hops[1].DestinationNode != "C" {
		t.Fatalf("wrong generic multi-hop recommendation: %+v", r)
	}
}

func TestPathGraphIgnoresStaleEvidenceAndPrefersHealthyDirect(t *testing.T) {
	s := newGenericDiscoveryStore(t, "A", "B", "C")
	now := time.Now().UTC()
	s.mu.Lock()
	putGraphProbe(s, "ab", "A", "B", "4", "192.0.2.2", 443, PathFullData, 5, now)
	putGraphProbe(s, "bc", "B", "C", "4", "192.0.2.3", 443, PathFullData, 5, now)
	putGraphProbe(s, "ac-stale", "A", "C", "6", "2001:db8::3", 443, PathFullData, 1, now.Add(-pathDiscoveryFreshFor-time.Minute))
	s.mu.Unlock()

	g := s.PathGraph(now)
	r, ok := recommendation(g, "A", "C")
	if !ok || r.Direct {
		t.Fatalf("stale direct edge should not win: %+v ok=%v", r, ok)
	}

	s.mu.Lock()
	putGraphProbe(s, "ac-fresh", "A", "C", "4", "192.0.2.3", 8443, PathFullData, 30, now)
	s.mu.Unlock()
	g = s.PathGraph(now)
	r, ok = recommendation(g, "A", "C")
	if !ok || !r.Direct || len(r.Hops) != 1 {
		t.Fatalf("fresh healthy direct route should win: %+v ok=%v", r, ok)
	}
}

func TestAutomaticDiscoveryStartsAndRefreshesWhenNodeSetChanges(t *testing.T) {
	s := newGenericDiscoveryStore(t, "A", "B")
	app, err := NewServer(s, "admin")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	app.now = func() time.Time { return now }
	app.AdvancePathDiscoveries()
	ds := s.ListPathDiscoveries()
	if len(ds) != 1 || ds[0].Phase != PathDiscoveryInventory {
		t.Fatalf("automatic discovery was not started: %+v", ds)
	}
	app.AdvancePathDiscoveries()
	if got := len(s.ListPathDiscoveries()); got != 1 {
		t.Fatalf("duplicate active discovery started: %d", got)
	}

	s.mu.Lock()
	d := s.st.PathDiscoveries[ds[0].ID]
	d.Phase = PathDiscoveryComplete
	d.UpdatedAt = now
	s.st.PathDiscoveries[d.ID] = d
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	s.mu.Unlock()

	if _, err := s.UpsertNode(Node{ID: "C", Address: "192.0.2.3:22", PathIPv4: "192.0.2.3", Role: "worker"}, "tok-C"); err != nil {
		t.Fatal(err)
	}
	app.AdvancePathDiscoveries()
	if got := len(s.ListPathDiscoveries()); got != 2 {
		t.Fatalf("node-set change did not trigger fresh discovery: %d", got)
	}
}

func TestPathDiscoveryExcludesRevokedNodes(t *testing.T) {
	s := newGenericDiscoveryStore(t, "A", "B", "C")
	now := time.Now().UTC()
	if _, err := s.RevokeNode("B", "test", now); err != nil {
		t.Fatal(err)
	}
	d, err := s.StartPathDiscovery([]int{49152}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Inventory) != 2 {
		t.Fatalf("inventory=%d want 2 active nodes: %+v", len(d.Inventory), d.Inventory)
	}
	if _, ok := d.Inventory["B"]; ok {
		t.Fatal("revoked node included in automatic discovery")
	}
}

func TestPathGraphNeverRoutesThroughRevokedNode(t *testing.T) {
	s := newGenericDiscoveryStore(t, "A", "B", "C")
	now := time.Now().UTC()
	s.mu.Lock()
	putGraphProbe(s, "ab-r", "A", "B", "4", "192.0.2.2", 443, PathFullData, 5, now)
	putGraphProbe(s, "bc-r", "B", "C", "4", "192.0.2.3", 443, PathFullData, 5, now)
	s.mu.Unlock()
	if _, err := s.RevokeNode("B", "test", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	g := s.PathGraph(now.Add(2 * time.Second))
	if _, ok := recommendation(g, "A", "C"); ok {
		t.Fatal("route through revoked B was recommended")
	}
}

func TestPathDiscoveryInventoryTimeoutFailsClosed(t *testing.T) {
	s := newGenericDiscoveryStore(t, "A", "B")
	now := time.Now().UTC()
	d, err := s.StartPathDiscovery([]int{49152}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AdvancePathDiscoveries(now.Add(pathProbeJobTimeout + time.Second)); err != nil {
		t.Fatal(err)
	}
	got := s.ListPathDiscoveries()
	if len(got) != 1 || got[0].ID != d.ID || got[0].Phase != PathDiscoveryFailed {
		t.Fatalf("timed-out inventory did not fail closed: %+v", got)
	}
	for _, inv := range got[0].Inventory {
		j, ok := s.jobByID(inv.JobID)
		if !ok || j.Status != "failed" {
			t.Fatalf("inventory job did not time out: %+v ok=%v", j, ok)
		}
	}
}
