package bcc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newPathProbeStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertNode(Node{ID: "src", Address: "127.0.0.1:22", PathIPv4: "127.0.0.1", Role: "worker"}, "tok-src"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertNode(Node{ID: "dst", Address: "127.0.0.1:22", PathIPv4: "127.0.0.1", PathIPv6: "2001:db8::9", Role: "foreign"}, "tok-dst"); err != nil {
		t.Fatal(err)
	}
	return s
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func startOnePathProbe(t *testing.T, s *Store, port int) PathProbe {
	t.Helper()
	p, err := s.StartPathProbe(PathProbeRequest{
		SourceNode: "src", DestinationNode: "dst", Family: "4", CandidatePorts: []int{port},
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPathProbeFullDataBecomesRecommended(t *testing.T) {
	s := newPathProbeStore(t)
	now := time.Now().UTC()
	p := startOnePathProbe(t, s, 22001)

	if err := s.AckJobOutput("dst", "tok-dst", p.JobID, "succeeded", "listener ready", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdvancePathProbes(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	p, _ = s.GetPathProbe(p.ID)
	run := probeRunEvidence{
		ProbeID: p.ID, Family: "4", Target: "127.0.0.1:22001",
		Bulk: []probeFlowEvidence{
			{Requested: 65536, BytesWritten: 65536, ConnectMS: 7, Connected: true, Handshake: true, ACK: true},
			{Requested: 65536, BytesWritten: 65536, ConnectMS: 5, Connected: true, Handshake: true, ACK: true},
			{Requested: 65536, BytesWritten: 65536, ConnectMS: 6, Connected: true, Handshake: true, ACK: true},
		},
		Trickle: probeFlowEvidence{Requested: 16384, BytesWritten: 16384, ConnectMS: 6, Connected: true, Handshake: true, ACK: true},
	}
	if err := s.AckJobOutput("src", "tok-src", p.JobID, "succeeded", "path probe evidence recorded", mustJSON(t, run)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdvancePathProbes(now.Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	p, _ = s.GetPathProbe(p.ID)
	listen := probeListenEvidence{
		ProbeID: p.ID, Family: "4", Port: 22001,
		Received: []int64{65536, 65536, 65536, 16384},
		Full:     []bool{true, true, true, true},
	}
	if err := s.AckJobOutput("dst", "tok-dst", p.JobID, "succeeded", "path probe evidence recorded", mustJSON(t, listen)); err != nil {
		t.Fatal(err)
	}
	evs, err := s.AdvancePathProbes(now.Add(3 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	p, _ = s.GetPathProbe(p.ID)
	if p.Phase != PathProbeComplete || p.RecommendedPort != 22001 || p.Candidates[0].Class != PathFullData {
		t.Fatalf("unexpected completed probe: %+v", p)
	}
	if len(evs) != 1 || evs[0].Action != "pathprobe.complete" {
		t.Fatalf("unexpected events: %+v", evs)
	}
}

func TestPathProbeDetectsRepeatedByteCeiling(t *testing.T) {
	s := newPathProbeStore(t)
	now := time.Now().UTC()
	p := startOnePathProbe(t, s, 22002)
	if err := s.AckJobOutput("dst", "tok-dst", p.JobID, "succeeded", "listener ready", ""); err != nil {
		t.Fatal(err)
	}
	_, _ = s.AdvancePathProbes(now.Add(time.Second))
	p, _ = s.GetPathProbe(p.ID)
	run := probeRunEvidence{
		ProbeID: p.ID, Family: "4", Target: "127.0.0.1:22002",
		Bulk: []probeFlowEvidence{
			{Requested: 65536, BytesWritten: 65536, ConnectMS: 4, Connected: true, Handshake: true},
			{Requested: 65536, BytesWritten: 65536, ConnectMS: 4, Connected: true, Handshake: true},
			{Requested: 65536, BytesWritten: 65536, ConnectMS: 5, Connected: true, Handshake: true},
		},
		Trickle: probeFlowEvidence{Requested: 16384, BytesWritten: 16384, ConnectMS: 4, Connected: true, Handshake: true},
	}
	if err := s.AckJobOutput("src", "tok-src", p.JobID, "succeeded", "path probe evidence recorded", mustJSON(t, run)); err != nil {
		t.Fatal(err)
	}
	_, _ = s.AdvancePathProbes(now.Add(2 * time.Second))
	p, _ = s.GetPathProbe(p.ID)
	listen := probeListenEvidence{
		ProbeID: p.ID, Family: "4", Port: 22002,
		Received: []int64{8688, 8688, 8688, 8688},
		Full:     []bool{false, false, false, false},
	}
	if err := s.AckJobOutput("dst", "tok-dst", p.JobID, "succeeded", "path probe evidence recorded", mustJSON(t, listen)); err != nil {
		t.Fatal(err)
	}
	_, _ = s.AdvancePathProbes(now.Add(3 * time.Second))
	p, _ = s.GetPathProbe(p.ID)
	c := p.Candidates[0]
	if c.Class != PathByteCeiling || c.ByteCeiling != 8688 || p.RecommendedPort != 0 {
		t.Fatalf("ceiling not classified: %+v", p)
	}
}

func TestPathProbeOccupiedPortIsSkippedWithoutSourceJob(t *testing.T) {
	s := newPathProbeStore(t)
	now := time.Now().UTC()
	p := startOnePathProbe(t, s, 22)
	if err := s.AckJobOutput("dst", "tok-dst", p.JobID, "failed", "path probe listen: bind: address already in use", ""); err != nil {
		t.Fatal(err)
	}
	_, err := s.AdvancePathProbes(now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	p, _ = s.GetPathProbe(p.ID)
	if p.Phase != PathProbeComplete || p.Candidates[0].Class != PathLocalConflict {
		t.Fatalf("occupied port not skipped: %+v", p)
	}
	for _, j := range s.ListJobs() {
		if j.NodeID == "src" {
			t.Fatalf("source probe was queued for occupied destination port: %+v", j)
		}
	}
}

func TestPathProbeUsesOnlyEnrolledNodeAddressesAndPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertNode(Node{ID: "src", Address: "192.0.2.2:22", PathIPv4: "192.0.2.2", Role: "worker"}, "src-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertNode(Node{ID: "dst", Address: "198.51.100.9:22", PathIPv4: "198.51.100.9", PathIPv6: "2001:db8::9", Role: "foreign"}, "dst-token"); err != nil {
		t.Fatal(err)
	}
	p, err := s.StartPathProbe(PathProbeRequest{
		SourceNode: "src", DestinationNode: "dst", Family: "6", CandidatePorts: []int{443},
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if p.DestinationIP != "2001:db8::9" {
		t.Fatalf("destination escaped enrolled IPv6 address: %+v", p)
	}
	s2, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := s2.GetPathProbe(p.ID)
	if !ok || got.DestinationIP != p.DestinationIP || got.JobID == "" {
		t.Fatalf("path probe did not survive BCC restart: %+v ok=%v", got, ok)
	}
	if _, err := s2.StartPathProbe(PathProbeRequest{
		SourceNode: "src", DestinationNode: "not-enrolled", Family: "4", CandidatePorts: []int{443},
	}, time.Now().UTC()); err == nil {
		t.Fatal("probe to an unenrolled destination was accepted")
	}
}

func TestPathProbeRecommendationIsDeterministic(t *testing.T) {
	cs := []PathProbeCandidate{
		{Port: 8443, Class: PathFullData, ConnectRTTMS: 18},
		{Port: 443, Class: PathFullData, ConnectRTTMS: 18},
		{Port: 22, Class: PathConnectOnly, ConnectRTTMS: 1},
	}
	if got := recommendProbePort(cs); got != 443 {
		t.Fatalf("recommendation=%d want=443", got)
	}
}

func TestPathProbeCancelQueuesDestinationCleanup(t *testing.T) {
	s := newPathProbeStore(t)
	now := time.Now().UTC()
	p := startOnePathProbe(t, s, 22003)
	cancelled, err := s.CancelPathProbe(p.ID, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Phase != PathProbeStopping || !cancelled.CancelRequested || cancelled.JobID == "" {
		t.Fatalf("cancel did not queue cleanup: %+v", cancelled)
	}
	j := s.st.Jobs[cancelled.JobID]
	if j.Type != "path_probe_stop" || j.NodeID != "dst" {
		t.Fatalf("cleanup job is not destination stop: %+v", j)
	}
	if err := s.AckJobOutput("dst", "tok-dst", j.ID, "succeeded", "path probe evidence recorded", mustJSON(t, probeListenEvidence{ProbeID: p.ID})); err != nil {
		t.Fatal(err)
	}
	evs, err := s.AdvancePathProbes(now.Add(2 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetPathProbe(p.ID)
	if got.Phase != PathProbeCancelled || got.JobID != "" {
		t.Fatalf("cancel did not finish cleanly: %+v", got)
	}
	if len(evs) != 1 || evs[0].Action != "pathprobe.cancelled" {
		t.Fatalf("unexpected cancel events: %+v", evs)
	}
}

func TestPathProbeAPIRejectsArbitraryTargetAndDashboardExposesControl(t *testing.T) {
	s := newPathProbeStore(t)
	app, err := NewServer(s, "admin")
	if err != nil {
		t.Fatal(err)
	}
	bad := httptest.NewRecorder()
	app.Handler().ServeHTTP(bad, authReq(http.MethodPost, "/api/path-probes", "admin", map[string]any{
		"source_node": "src", "destination_node": "dst", "family": "4",
		"candidate_ports": []int{443}, "target": "203.0.113.250",
	}))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("arbitrary target field accepted: %d %s", bad.Code, bad.Body.String())
	}
	ok := httptest.NewRecorder()
	app.Handler().ServeHTTP(ok, authReq(http.MethodPost, "/api/path-probes", "admin", map[string]any{
		"source_node": "src", "destination_node": "dst", "family": "4",
		"candidate_ports": []int{443, 8443},
	}))
	if ok.Code != http.StatusAccepted {
		t.Fatalf("path probe start=%d %s", ok.Code, ok.Body.String())
	}
	var p PathProbe
	if err := json.Unmarshal(ok.Body.Bytes(), &p); err != nil || p.DestinationIP != "127.0.0.1" {
		t.Fatalf("unexpected API probe: %+v err=%v", p, err)
	}
	for _, want := range []string{"/api/path-probes", "Automatic Path Discovery", "ppRun", "loadPathProbes", "/api/path-graph", "loadPathGraph"} {
		if !strings.Contains(dashboardHTML, want) {
			t.Errorf("dashboard lacks %q", want)
		}
	}
}

func TestNodePathAddressesAreValidatedAndPreserved(t *testing.T) {
	s := newPathProbeStore(t)
	n, ok := s.GetNode("dst")
	if !ok || n.PathIPv6 != "2001:db8::9" {
		t.Fatalf("missing path address: %+v", n)
	}
	if _, err := s.UpsertNode(Node{ID: "dst", Alias: "renamed", Address: n.Address, Role: "foreign"}, ""); err != nil {
		t.Fatal(err)
	}
	n, _ = s.GetNode("dst")
	if n.PathIPv4 != "127.0.0.1" || n.PathIPv6 != "2001:db8::9" {
		t.Fatalf("partial node update erased path addresses: %+v", n)
	}
	if _, err := s.UpsertNode(Node{ID: "bad", Address: "127.0.0.1:22", PathIPv6: "192.0.2.4", Role: "worker"}, "tok"); err == nil {
		t.Fatal("IPv4 accepted as path_ipv6")
	}
}
