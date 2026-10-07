package agent

import (
	"context"
	"encoding/json"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/zarkmakerburg/baft/internal/agentjob"
)

func freeProbePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return p
}

func TestPathProbeCarriesIntegrityCheckedData(t *testing.T) {
	a := &Agent{probeListeners: map[string]*pathProbeListener{}}
	port := freeProbePort(t)
	id := "probe-test-1"
	if _, err := a.pathProbeListen(agentjob.Job{Action: agentjob.ActionPathProbeListen, Params: map[string]string{
		"probe_id": id, "family": "4", "port": strconv.Itoa(port), "ttl_seconds": "20",
	}}); err != nil {
		t.Fatal(err)
	}
	runJSON, err := a.pathProbeRun(context.Background(), agentjob.Job{Action: agentjob.ActionPathProbeRun, Params: map[string]string{
		"probe_id": id, "family": "4", "target": net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		"payload_bytes": "4096", "attempts": "2", "trickle_bytes": "2048", "trickle_ms": "10",
	}})
	if err != nil {
		t.Fatal(err)
	}
	var run pathProbeRunEvidence
	if err := json.Unmarshal([]byte(runJSON), &run); err != nil {
		t.Fatal(err)
	}
	if len(run.Bulk) != 2 || !run.Bulk[0].ACK || !run.Bulk[1].ACK || !run.Trickle.ACK {
		t.Fatalf("unexpected run evidence: %+v", run)
	}
	stopJSON := a.pathProbeStop(agentjob.Job{Params: map[string]string{"probe_id": id}})
	var listen pathProbeListenEvidence
	if err := json.Unmarshal([]byte(stopJSON), &listen); err != nil {
		t.Fatal(err)
	}
	if len(listen.Received) != 3 {
		t.Fatalf("receiver saw %d flows, want 3: %+v", len(listen.Received), listen)
	}
	for i, ok := range listen.Full {
		if !ok {
			t.Fatalf("receiver flow %d was not complete: %+v", i, listen)
		}
	}
}

func TestPathProbeListenerNeverStealsOccupiedPort(t *testing.T) {
	ln, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	a := &Agent{probeListeners: map[string]*pathProbeListener{}}
	_, err = a.pathProbeListen(agentjob.Job{Action: agentjob.ActionPathProbeListen, Params: map[string]string{
		"probe_id": "probe-conflict", "family": "4", "port": strconv.Itoa(port), "ttl_seconds": "20",
	}})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "address already in use") {
		t.Fatalf("occupied port not rejected safely: %v", err)
	}
}
