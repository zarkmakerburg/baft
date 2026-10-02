package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const fakeMetrics = `# HELP baft_active_flows Number of active BAFT application flows.
# TYPE baft_active_flows gauge
baft_active_flows 3
baft_recovery_current_epoch 2
baft_recovery_attempts_total 4
baft_recovery_commits_total 4
baft_recovery_failures_total{reason="timeout"} 1
baft_recovery_failures_total{reason="stale"} 2
baft_conservation_invariant_violations 0
`

type fakeHost struct {
	env      opsEnv
	dialed   []string
	down     map[string]bool
	streamed []string
}

func newFakeHost(t *testing.T) *fakeHost {
	h := &fakeHost{down: map[string]bool{}}
	h.env = opsEnv{
		output: func(name string, args ...string) (string, error) {
			switch {
			case name == "systemctl" && args[0] == "is-active":
				return "active", nil
			case name == "systemctl" && args[0] == "show":
				return "0", nil
			}
			return "", errors.New("unexpected command")
		},
		stream: func(name string, args []string, stdout, _ io.Writer) error {
			h.streamed = append([]string{name}, args...)
			fmt.Fprintln(stdout, "log line")
			return nil
		},
		dial: func(addr string, _ time.Duration) error {
			h.dialed = append(h.dialed, addr)
			if h.down[addr] {
				return errors.New("connection refused")
			}
			return nil
		},
		fetch: func(url string) (string, error) {
			if url != "http://127.0.0.1:9191/metrics" {
				return "", fmt.Errorf("unexpected url %s", url)
			}
			return fakeMetrics, nil
		},
		procRoot:   t.TempDir(),
		ownerUID:   func(string) (uint32, error) { return 1000, nil },
		serviceUID: func(string) (uint32, error) { return 1000, nil },
	}
	return h
}

func (h *fakeHost) sysctl(t *testing.T, name, value string) {
	p := filepath.Join(h.env.procRoot, "sys", strings.ReplaceAll(name, ".", "/"))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(value+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeDialerConfig writes an IR config whose Noise key has the given mode.
func writeDialerConfig(t *testing.T, keyMode os.FileMode) (cfgPath, keyPath string) {
	return writeDialerConfigIn(t, t.TempDir(), keyMode)
}

func writeDialerConfigIn(t *testing.T, dir string, keyMode os.FileMode) (cfgPath, keyPath string) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	keyPath = filepath.Join(dir, "noise-key.json")
	if err := os.WriteFile(keyPath, []byte("{}"), keyMode); err != nil {
		t.Fatal(err)
	}
	os.Chmod(keyPath, keyMode)
	cfg := fmt.Sprintf(`schema_version: 1
noise: {key_file: %q, peer_public_key: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}
node: {id: ir-01, role: dialer}
peer: {address: "203.0.113.7:8443", server_name: ex.test, allowed_identity: "urn:baft:node:ex-01"}
tls: {min_version: "1.3", ca_file: /tmp/ca.pem, session_tickets: false}
transport: {primary: h2, h3_enabled: false, shards: 1, profile: secure-fast}
limits: {max_flows: 32, data_memory_mib: 64, receive_initial_kib: 64, receive_max_mib: 16, replay_max_mib: 16}
recovery: {enabled: false, retention_seconds: 30}
routes:
  - {id: service-main, listen: "127.0.0.1:1443", remote_route: service-main, direction: outbound, traffic_class: interactive}
management: {unix_socket: /run/baft/admin.sock, metrics_listen: "127.0.0.1:9191"}
logging: {level: info, payload: false}
`, keyPath)
	cfgPath = filepath.Join(dir, "baft.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath, keyPath
}

func writeReleaseState(t *testing.T, v string) string {
	p := filepath.Join(t.TempDir(), "release-state.json")
	os.WriteFile(p, []byte(`{"schema_version":1,"version":"`+v+`","commit":"879ade5af7cb05f3a64bb04c530567e09e838f85","revocation_sequence":1,"updated_at":"2026-10-01T00:00:00Z"}`), 0o644)
	return p
}

func TestStatusSummarisesNode(t *testing.T) {
	cfg, _ := writeDialerConfig(t, 0o600)
	h := newFakeHost(t)
	var out, errOut bytes.Buffer
	code := runStatus([]string{"--file", cfg, "--release-state", writeReleaseState(t, "v"+version)}, &out, &errOut, h.env)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut.String())
	}
	for _, want := range []string{
		"release v" + version + ", commit 879ade5af7cb",
		"ir-01 (dialer), peer 203.0.113.7:8443",
		"service-main: 127.0.0.1:1443 -> peer route service-main",
		"baft: active, restarts 0",
		"3 active",
		"epoch 2, 4 attempts, 4 commits, 3 failures",
		"0 invariant violations",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestStatusJSONAndInvalidConfig(t *testing.T) {
	h := newFakeHost(t)
	var out, errOut bytes.Buffer
	missing := filepath.Join(t.TempDir(), "nope.yaml")
	if code := runStatus([]string{"--file", missing, "--json"}, &out, &errOut, h.env); code != 1 {
		t.Fatalf("invalid config gave code %d", code)
	}
	var r statusReport
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	if r.ConfigError == "" || r.ServiceState != "active" || r.Release != nil {
		t.Fatalf("unexpected report %+v", r)
	}
}

func doctorResult(t *testing.T, h *fakeHost, args ...string) (int, map[string]check) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := runDoctor(append(args, "--json"), &out, &errOut, h.env)
	var res struct {
		OK     bool    `json:"ok"`
		Checks []check `json:"checks"`
	}
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("not JSON: %v\n%s%s", err, out.String(), errOut.String())
	}
	byName := map[string]check{}
	for _, c := range res.Checks {
		if _, dup := byName[c.Name]; !dup {
			byName[c.Name] = c
		}
	}
	if res.OK != (code == 0) {
		t.Fatalf("ok=%v but exit code %d", res.OK, code)
	}
	return code, byName
}

func TestDoctorHealthyNode(t *testing.T) {
	cfg, _ := writeDialerConfig(t, 0o600)
	h := newFakeHost(t)
	h.sysctl(t, "net.ipv4.tcp_congestion_control", "bbr")
	h.sysctl(t, "net.core.default_qdisc", "fq")
	h.sysctl(t, "net.core.rmem_max", "16777216")
	code, c := doctorResult(t, h, "--file", cfg, "--release-state", writeReleaseState(t, "v"+version))
	if code != 0 {
		t.Fatalf("healthy node failed doctor: %+v", c)
	}
	for _, name := range []string{"config", "private key", "service", "release", "metrics", "peer", "route service-main", "tcp congestion", "qdisc", "net.core.rmem_max"} {
		if c[name].Status != checkOK {
			t.Errorf("%s = %+v, want ok", name, c[name])
		}
	}
	if strings.Join(h.dialed, ",") != "203.0.113.7:8443,127.0.0.1:1443" {
		t.Errorf("dialed %v", h.dialed)
	}
}

func TestDoctorFindsProblems(t *testing.T) {
	cfg, key := writeDialerConfig(t, 0o644)
	h := newFakeHost(t)
	h.down["203.0.113.7:8443"] = true
	h.sysctl(t, "net.ipv4.tcp_congestion_control", "cubic")
	h.sysctl(t, "net.core.wmem_max", "212992")
	code, c := doctorResult(t, h, "--file", cfg, "--release-state", writeReleaseState(t, "v9.9.9"))
	if code != 1 {
		t.Fatal("doctor passed a node with an exposed key and an unreachable peer")
	}
	if c["private key"].Status != checkFail || !strings.Contains(c["private key"].Hint, "chmod 0600 "+key) {
		t.Errorf("key check = %+v", c["private key"])
	}
	if c["peer"].Status != checkFail {
		t.Errorf("peer check = %+v", c["peer"])
	}
	if c["release"].Status != checkWarn {
		t.Errorf("version mismatch check = %+v", c["release"])
	}
	if c["tcp congestion"].Status != checkInfo || !strings.Contains(c["tcp congestion"].Hint, "bbr") {
		t.Errorf("congestion check = %+v", c["tcp congestion"])
	}
	if c["net.core.wmem_max"].Status != checkInfo {
		t.Errorf("wmem check = %+v", c["net.core.wmem_max"])
	}
}

func TestDoctorReportsMissingReleaseAndStoppedService(t *testing.T) {
	cfg, _ := writeDialerConfig(t, 0o600)
	h := newFakeHost(t)
	h.env.output = func(name string, args ...string) (string, error) {
		if args[0] == "is-active" {
			return "failed", errors.New("exit status 3")
		}
		return "", errors.New("no")
	}
	code, c := doctorResult(t, h, "--file", cfg, "--release-state", filepath.Join(t.TempDir(), "none.json"))
	if code != 1 || c["service"].Status != checkFail || c["release"].Status != checkWarn {
		t.Fatalf("code=%d service=%+v release=%+v", code, c["service"], c["release"])
	}
}

func TestLogsRunsJournalctl(t *testing.T) {
	h := newFakeHost(t)
	var out, errOut bytes.Buffer
	if code := runLogs([]string{"--service", "baft-ir", "-n", "20", "-f"}, &out, &errOut, h.env); code != 0 {
		t.Fatalf("code=%d %s", code, errOut.String())
	}
	if got := strings.Join(h.streamed, " "); got != "journalctl -u baft-ir -n 20 --no-pager -o short-iso -f" {
		t.Fatalf("ran %q", got)
	}
	if out.String() != "log line\n" {
		t.Fatalf("output %q", out.String())
	}
}

func TestLoopbackMapsWildcards(t *testing.T) {
	for in, want := range map[string]string{"0.0.0.0:8443": "127.0.0.1:8443", ":8443": "127.0.0.1:8443", "[::]:8443": "[::1]:8443", "10.0.0.1:443": "10.0.0.1:443"} {
		if got := loopback(in); got != want {
			t.Errorf("loopback(%q) = %q, want %q", in, got, want)
		}
	}
}

func doctorJSON(t *testing.T, h *fakeHost, args ...string) summary {
	t.Helper()
	var out, errOut bytes.Buffer
	runDoctor(append(args, "--json"), &out, &errOut, h.env)
	var res struct {
		Summary summary `json:"summary"`
	}
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	return res.Summary
}

func layerStates(s summary) map[string]string {
	m := map[string]string{}
	for _, l := range s.Layers {
		m[l.Layer] = l.State
	}
	return m
}

func TestDoctorVerdictNamesTheFailureDomainAndNeverCallsUnobservedLayersHealthy(t *testing.T) {
	cfg, _ := writeDialerConfig(t, 0o600)
	rel := writeReleaseState(t, "v"+version)

	h := newFakeHost(t)
	s := doctorJSON(t, h, "--file", cfg, "--release-state", rel)
	if s.Verdict != verdictHealthy {
		t.Fatalf("healthy node: %+v", s)
	}
	st := layerStates(s)
	if st["L0"] != "PASS" || st["L1"] != "PASS" || st["L3"] != "PASS" || st["L4"] != "PASS" {
		t.Fatalf("observed layers: %v", st)
	}
	for _, id := range []string{"L2", "L6"} {
		if st[id] != "NOT_ASSESSED" {
			t.Errorf("%s is %s: a layer doctor cannot observe must not read as PASS", id, st[id])
		}
	}

	h = newFakeHost(t)
	h.down["203.0.113.7:8443"] = true
	s = doctorJSON(t, h, "--file", cfg, "--release-state", rel)
	if s.Verdict != verdictFailing || s.LikelyDomain != "network path to the peer" || s.Confidence != "high" || s.NextAction == "" {
		t.Fatalf("peer down: %+v", s)
	}
	if st := layerStates(s); st["L1"] != "FAIL" || st["L0"] != "PASS" {
		t.Fatalf("layers with the peer down: %v", st)
	}
}

func TestDoctorSeveralDomainsPicksTheLowestAndSaysItIsLessSure(t *testing.T) {
	cfg, _ := writeDialerConfig(t, 0o644) // exposed key
	h := newFakeHost(t)
	h.down["203.0.113.7:8443"] = true
	s := doctorJSON(t, h, "--file", cfg, "--release-state", writeReleaseState(t, "v"+version))
	if s.LikelyDomain != "host security" || s.Confidence != "medium" {
		t.Fatalf("two failing domains: %+v", s)
	}
}

func TestDoctorTextHasProblemEvidenceImpactFixAndPreviewRunsNothing(t *testing.T) {
	cfg, key := writeDialerConfig(t, 0o644)
	h := newFakeHost(t)
	h.sysctl(t, "net.core.default_qdisc", "pfifo_fast")
	var out, errOut bytes.Buffer
	runDoctor([]string{"--file", cfg, "--release-state", writeReleaseState(t, "v"+version)}, &out, &errOut, h.env)
	for _, want := range []string{"BAFT doctor: FAILING", "likely failure domain: host security", "problem:", "evidence: ", "impact:", "fix:      chmod 0600 " + key, "L2", "NOT_ASSESSED"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report lacks %q:\n%s", want, out.String())
		}
	}
	calls := len(h.streamed)
	out.Reset()
	runDoctor([]string{"--file", cfg, "--release-state", writeReleaseState(t, "v"+version), "--preview-fixes"}, &out, &errOut, h.env)
	p := out.String()
	for _, want := range []string{"Preview only", "SAFE", "chmod 0600 " + key, "REVIEW", "sysctl -w net.core.default_qdisc=fq"} {
		if !strings.Contains(p, want) {
			t.Errorf("preview lacks %q:\n%s", want, p)
		}
	}
	if st, err := os.Stat(key); err != nil || st.Mode().Perm() != 0o644 {
		t.Fatalf("preview changed the key's mode: %v %v", st, err)
	}
	if len(h.streamed) != calls {
		t.Fatal("preview ran a command")
	}
}

func keyCheck(t *testing.T, h *fakeHost, cfg string) check {
	t.Helper()
	_, c := doctorResult(t, h, "--file", cfg, "--release-state", writeReleaseState(t, "v"+version))
	return c["private key"]
}

func TestKeyChmodIsSafeOnlyWhenTheServiceUserOwnsTheKey(t *testing.T) {
	cfg, key := writeDialerConfig(t, 0o644)

	h := newFakeHost(t) // owner 1000, service 1000
	if c := keyCheck(t, h, cfg); c.FixSafety != fixSafe || c.FixCommand != "chmod 0600 "+key {
		t.Fatalf("owned by the service user: %+v", c)
	}

	// A root-owned key under a service running as uid 1000: chmod 0600 would
	// leave the service unable to read it after a restart.
	h = newFakeHost(t)
	h.env.ownerUID = func(string) (uint32, error) { return 0, nil }
	c := keyCheck(t, h, cfg)
	if c.FixSafety != fixReview || !strings.Contains(c.Hint, "lock the service out") || !strings.Contains(c.Hint, "uid 0") {
		t.Fatalf("non-service-owned key: %+v", c)
	}

	// If ownership or the service user cannot be established, it is not SAFE.
	h = newFakeHost(t)
	h.env.serviceUID = func(string) (uint32, error) { return 0, errors.New("no systemd") }
	if c := keyCheck(t, h, cfg); c.FixSafety != fixReview || !strings.Contains(c.Hint, "could not confirm") {
		t.Fatalf("unknown service user: %+v", c)
	}
	h = newFakeHost(t)
	h.env.ownerUID = nil
	h.env.serviceUID = nil
	if c := keyCheck(t, h, cfg); c.FixSafety != fixReview {
		t.Fatalf("no owner information: %+v", c)
	}
}

func TestFixCommandsQuoteEveryConfigDerivedPath(t *testing.T) {
	// A valid path with spaces, a semicolon, $() and an apostrophe.
	dir := filepath.Join(t.TempDir(), "my keys; $(touch PWNED) it's")
	cfg, key := writeDialerConfigIn(t, dir, 0o644)
	h := newFakeHost(t)
	c := keyCheck(t, h, cfg)
	if c.FixCommand == "" || c.FixCommand == "chmod 0600 "+key {
		t.Fatalf("the path went into the command unquoted: %q", c.FixCommand)
	}
	if !strings.Contains(c.Hint, c.FixCommand) {
		t.Errorf("hint does not carry the quoted command: %q", c.Hint)
	}
	// Run the suggested command for real, in the temp dir: it must change the
	// mode of exactly that file and execute nothing else.
	cmd := exec.Command("sh", "-c", c.FixCommand)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the suggested command failed: %v\n%s", err, out)
	}
	if st, _ := os.Stat(key); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode after the fix: %v", st.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(dir, "PWNED")); err == nil {
		t.Fatal("the suggested command executed text taken from the config path")
	}
	// The preview prints the same quoted command (open the key up again first).
	os.Chmod(key, 0o644)
	var out, errOut bytes.Buffer
	runDoctor([]string{"--file", cfg, "--release-state", writeReleaseState(t, "v"+version), "--preview-fixes"}, &out, &errOut, h.env)
	if !strings.Contains(out.String(), c.FixCommand) {
		t.Fatalf("preview lacks the quoted command:\n%s", out.String())
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/etc/baft/noise-key.json": "/etc/baft/noise-key.json",
		"a b":                      "'a b'",
		"it's":                     `'it'\''s'`,
		"$(x)":                     "'$(x)'",
		"":                         "''",
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}
