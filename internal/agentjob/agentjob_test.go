package agentjob

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/release"
)

var now = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

func job(action string, params map[string]string) Job {
	return Job{JobID: "job-00000001", NodeID: "ex-1", Action: action, Params: params, IssuedAt: now, ExpiresAt: now.Add(time.Hour)}
}

func setup(t *testing.T) (Verifier, func(Job) release.Envelope) {
	pub, priv, _ := release.GenerateKey()
	v := Verifier{BCCKey: pub, NodeID: "ex-1"}
	return v, func(j Job) release.Envelope {
		env, err := Sign(priv, j)
		if err != nil {
			t.Fatal(err)
		}
		return env
	}
}

func wantErr(t *testing.T, err error, substr string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), substr) {
		t.Fatalf("error = %v, want %q", err, substr)
	}
}

func TestAgentRunsOnlyValidSignedJobs(t *testing.T) {
	v, sign := setup(t)
	got, err := v.Verify(sign(job(ActionUpdateBAFT, map[string]string{"version": "v1.2.0"})), now.Add(time.Minute))
	if err != nil || got.Action != ActionUpdateBAFT || got.Params["version"] != "v1.2.0" {
		t.Fatalf("valid job refused: %+v %v", got, err)
	}
	for _, a := range Actions() {
		if _, ok := paramRules[a]; !ok {
			t.Fatal("Actions and rules disagree")
		}
	}
}

func TestSignRefusesWhatAgentsWouldRefuse(t *testing.T) {
	_, priv, _ := release.GenerateKey()
	for name, j := range map[string]Job{
		"shell action":    job("exec", map[string]string{"cmd": "rm -rf /"}),
		"missing param":   job(ActionUpdateBAFT, nil),
		"extra param":     job(ActionRestart, map[string]string{"x": "y"}),
		"bad version":     job(ActionUpdateBAFT, map[string]string{"version": "latest; curl evil"}),
		"too long":        {JobID: "j", NodeID: "ex-1", Action: ActionHealth, IssuedAt: now, ExpiresAt: now.Add(MaxLifetime + time.Second)},
		"bad node":        {JobID: "j", NodeID: "../etc", Action: ActionHealth, IssuedAt: now, ExpiresAt: now.Add(time.Hour)},
		"inverted window": {JobID: "j", NodeID: "ex-1", Action: ActionHealth, IssuedAt: now, ExpiresAt: now},
	} {
		if _, err := Sign(priv, j); err == nil {
			t.Errorf("%s: signed", name)
		}
	}
}

func TestAgentRefusesForgedMisdirectedStaleAndReplayedJobs(t *testing.T) {
	v, sign := setup(t)
	env := sign(job(ActionRestart, nil))

	other, _, _ := release.GenerateKey()
	_, err := Verifier{BCCKey: other, NodeID: "ex-1"}.Verify(env, now)
	wantErr(t, err, "job signature")

	_, err = Verifier{BCCKey: v.BCCKey, NodeID: "ex-2"}.Verify(env, now)
	wantErr(t, err, "not this node")

	_, err = v.Verify(env, now.Add(2*time.Hour))
	wantErr(t, err, "expired")

	_, err = v.Verify(env, now.Add(-ClockSkew-time.Minute))
	wantErr(t, err, "future")

	seen := v
	seen.Seen = func(id string) bool { return id == "job-00000001" }
	_, err = seen.Verify(env, now)
	wantErr(t, err, "already run")

	// Tampering with the payload breaks the signature.
	payload, _ := base64.StdEncoding.DecodeString(env.Payload)
	env.Payload = base64.StdEncoding.EncodeToString([]byte(strings.Replace(string(payload), "restart", "reload", 1)))
	_, err = v.Verify(env, now)
	wantErr(t, err, "job signature")

	// A release manifest signed by the pinned key is not a job.
	pub, priv, _ := release.GenerateKey()
	confused := release.Sign(release.PayloadTypeManifest, []byte(`{}`), priv)
	_, err = Verifier{BCCKey: pub, NodeID: "ex-1"}.Verify(confused, now)
	wantErr(t, err, "payload type")
}

func TestTunnelRetireActionRequiresExactManagedProof(t *testing.T) {
	base := job(ActionTunnelRetire, map[string]string{
		"tunnel_id":     "tun-1",
		"generation":    "4",
		"config_sha256": strings.Repeat("a", 64),
		"unit_sha256":   strings.Repeat("b", 64),
		"marker_sha256": strings.Repeat("c", 64),
		"instance_id":   "de-main",
	})
	base.SchemaVersion = SchemaVersion
	if err := base.Validate(); err != nil {
		t.Fatalf("valid retire job rejected: %v", err)
	}
	for _, k := range []string{"generation", "config_sha256", "unit_sha256", "marker_sha256"} {
		j := base
		j.Params = map[string]string{}
		for pk, pv := range base.Params {
			j.Params[pk] = pv
		}
		delete(j.Params, k)
		if err := j.Validate(); err == nil {
			t.Errorf("retire job without %s accepted", k)
		}
	}
	for name, mutate := range map[string]func(map[string]string){
		"zero generation":   func(p map[string]string) { p["generation"] = "0" },
		"bad config digest": func(p map[string]string) { p["config_sha256"] = "abc" },
		"bad unit digest":   func(p map[string]string) { p["unit_sha256"] = strings.Repeat("z", 64) },
		"path instance":     func(p map[string]string) { p["instance_id"] = "../other" },
		"extra command":     func(p map[string]string) { p["cmd"] = "stop-all" },
	} {
		j := base
		j.Params = map[string]string{}
		for pk, pv := range base.Params {
			j.Params[pk] = pv
		}
		mutate(j.Params)
		if err := j.Validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestPathProbeActionsAreStrictlyBounded(t *testing.T) {
	valid := []Job{
		job(ActionPathProbeInventory, map[string]string{"discovery_id": "pd-1234"}),
		job(ActionPathProbeListen, map[string]string{"probe_id": "pp-1234", "family": "4", "port": "443", "ttl_seconds": "45"}),
		job(ActionPathProbeRun, map[string]string{"probe_id": "pp-1234", "family": "6", "target": "[2001:db8::9]:443", "payload_bytes": "65536", "attempts": "3", "trickle_bytes": "16384", "trickle_ms": "200"}),
		job(ActionPathProbeStop, map[string]string{"probe_id": "pp-1234"}),
	}
	for _, j := range valid {
		j.SchemaVersion = SchemaVersion
		if err := j.Validate(); err != nil {
			t.Fatalf("%s rejected: %v", j.Action, err)
		}
	}
	bad := []Job{
		job(ActionPathProbeInventory, map[string]string{"discovery_id": "../escape"}),
		job(ActionPathProbeListen, map[string]string{"probe_id": "pp-1", "family": "5", "port": "443", "ttl_seconds": "45"}),
		job(ActionPathProbeRun, map[string]string{"probe_id": "pp-1", "family": "4", "target": "example.com:443", "payload_bytes": "65536", "attempts": "3", "trickle_bytes": "16384", "trickle_ms": "200"}),
		job(ActionPathProbeRun, map[string]string{"probe_id": "pp-1", "family": "4", "target": "203.0.113.5:443", "payload_bytes": "99999999", "attempts": "3", "trickle_bytes": "16384", "trickle_ms": "200"}),
		job(ActionPathProbeStop, map[string]string{"probe_id": "../escape"}),
	}
	for _, j := range bad {
		j.SchemaVersion = SchemaVersion
		if err := j.Validate(); err == nil {
			t.Errorf("malformed %s job accepted: %+v", j.Action, j.Params)
		}
	}
}
