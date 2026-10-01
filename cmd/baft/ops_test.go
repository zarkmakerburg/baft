package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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
		procRoot: t.TempDir(),
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
	dir := t.TempDir()
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
