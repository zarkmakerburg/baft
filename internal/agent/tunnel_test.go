package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/agentjob"
	"github.com/zarkmakerburg/baft/internal/tunnelnode"
)

const fakeCode = "BAFTPAIR1:AAAAAAAAAAAAAAAAAAAAAAAAAA"

func prepareEXJob() agentjob.Job {
	return agentjob.Job{Action: agentjob.ActionTunnelPrepareEX, Params: map[string]string{
		"tunnel_id": "t1", "public_address": "203.0.113.7", "port": "8443", "target": "127.0.0.1:2443",
		"route_id": "service-main", "record_shaping": "false",
	}}
}

func TestTunnelPrepareReturnsTheSecretOnlyAsOutput(t *testing.T) {
	r := newRig(t)
	dir := t.TempDir()
	tn, err := tunnelnode.New(tunnelnode.Env{
		ConfigDir: filepath.Join(dir, "etc"), StateDir: filepath.Join(dir, "state"), UnitDir: filepath.Join(dir, "units"),
		BaftBin: "baft", PairBin: "baft-pair", Settle: 10 * time.Millisecond, System: r.sys,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.agent.cfg.Tunnel = tn
	r.sys.doctor = fakeCode // what the fake baft-pair prints

	detail, output, err := r.agent.executeFull(context.Background(), prepareEXJob())
	if err != nil {
		t.Fatal(err)
	}
	if output != fakeCode {
		t.Fatalf("output %q", output)
	}
	if strings.Contains(detail, "BAFTPAIR1") {
		t.Fatalf("the pairing code leaked into the loggable detail: %q", detail)
	}
	// Non-tunnel actions return no secret output.
	if _, out, err := r.agent.executeFull(context.Background(), agentjob.Job{Action: agentjob.ActionHealth}); err != nil || out != "" {
		t.Fatalf("health: %q %v", out, err)
	}
}

func TestTunnelJobsAreRefusedWhenNotEnabled(t *testing.T) {
	r := newRig(t)
	_, _, err := r.agent.executeFull(context.Background(), prepareEXJob())
	if err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("got %v", err)
	}
}

func TestTunnelActionsAreSignedAndStrictlyShaped(t *testing.T) {
	for _, action := range []string{
		agentjob.ActionTunnelPrepareEX, agentjob.ActionTunnelPrepareIR, agentjob.ActionTunnelCommitEX,
		agentjob.ActionTunnelCommitIR, agentjob.ActionTunnelHealth, agentjob.ActionTunnelFinalize, agentjob.ActionTunnelRollback,
	} {
		found := false
		for _, a := range agentjob.Actions() {
			found = found || a == action
		}
		if !found {
			t.Errorf("%s is not on the allowlist", action)
		}
	}
	now := time.Now()
	bad := prepareEXJob()
	bad.SchemaVersion, bad.JobID, bad.NodeID, bad.IssuedAt, bad.ExpiresAt = 1, "j1", "ex-1", now, now.Add(time.Hour)
	if err := bad.Validate(); err != nil {
		t.Fatalf("valid job rejected: %v", err)
	}
	for name, mutate := range map[string]func(map[string]string){
		"hostname target":   func(p map[string]string) { p["target"] = "example.com:443" },
		"shell in address":  func(p map[string]string) { p["public_address"] = "1.2.3.4;id" },
		"path-like tunnel":  func(p map[string]string) { p["tunnel_id"] = "../x" },
		"port not a number": func(p map[string]string) { p["port"] = "80a" },
		"extra parameter":   func(p map[string]string) { p["cmd"] = "id" },
		"missing parameter": func(p map[string]string) { delete(p, "route_id") },
		"non-bool shaping":  func(p map[string]string) { p["record_shaping"] = "yes" },
	} {
		j := bad
		j.Params = map[string]string{}
		for k, v := range bad.Params {
			j.Params[k] = v
		}
		mutate(j.Params)
		if err := j.Validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	ir := agentjob.Job{SchemaVersion: 1, JobID: "j2", NodeID: "ir-1", Action: agentjob.ActionTunnelPrepareIR, IssuedAt: now, ExpiresAt: now.Add(time.Hour),
		Params: map[string]string{"tunnel_id": "t1", "code": fakeCode, "route_listen": "0.0.0.0:1443", "route_id": "r"}}
	if ir.Validate() == nil {
		t.Error("non-loopback route listen accepted")
	}
	ir.Params["route_listen"] = "127.0.0.1:1443"
	if err := ir.Validate(); err != nil {
		t.Errorf("valid IR job rejected: %v", err)
	}
}
