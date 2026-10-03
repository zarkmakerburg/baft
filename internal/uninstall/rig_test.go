package uninstall

import (
	"context"
	"debug/buildinfo"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// fakeSys is systemd for tests: units have an active and an enabled state.
type fakeSys struct {
	mu      sync.Mutex
	active  map[string]string
	enabled map[string]string
	calls   []string
	fail    map[string]error
	run     func(name string, args ...string) (string, error)
}

func newFakeSys() *fakeSys {
	return &fakeSys{active: map[string]string{}, enabled: map[string]string{}, fail: map[string]error{}}
}

func (f *fakeSys) Systemctl(ctx context.Context, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := strings.Join(args, " ")
	if !strings.HasPrefix(key, "is-") {
		f.calls = append(f.calls, key)
	}
	if err := f.fail[key]; err != nil {
		return "", err
	}
	unit := ""
	if len(args) > 1 {
		unit = args[1]
	}
	switch args[0] {
	case "is-active":
		if s, ok := f.active[unit]; ok {
			return s, nil
		}
		return "inactive", nil
	case "is-enabled":
		if s, ok := f.enabled[unit]; ok {
			return s, nil
		}
		return "disabled", nil
	case "stop":
		f.active[unit] = "inactive"
	case "start", "restart":
		f.active[unit] = "active"
	case "enable":
		f.enabled[unit] = "enabled"
	case "disable":
		f.enabled[unit] = "disabled"
	}
	return "", nil
}

func (f *fakeSys) Run(ctx context.Context, name string, args ...string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "run "+filepath.Base(name)+" "+strings.Join(args, " "))
	run := f.run
	f.mu.Unlock()
	if run == nil {
		return `{"verified":true,"summary":"fake"}`, nil
	}
	return run(name, args...)
}

func (f *fakeSys) set(unit, active, enabled string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active[unit], f.enabled[unit] = active, enabled
}

func (f *fakeSys) state(unit string) (string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.active[unit]
	if !ok {
		a = "inactive"
	}
	e, ok := f.enabled[unit]
	if !ok {
		e = "disabled"
	}
	return a, e
}

type rig struct {
	t    *testing.T
	root string
	env  *Env
	sys  *fakeSys
}

func (r *rig) p(rel string) string { return filepath.Join(r.root, rel) }

func newRig(t *testing.T) *rig {
	t.Helper()
	root := t.TempDir()
	r := &rig{t: t, root: root, sys: newFakeSys()}
	for _, d := range []string{"units", "bin", "opt", "etc", "var", "agent", "agentstate", "journal", "backups", "skel"} {
		if err := os.MkdirAll(r.p(d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bi, err := buildinfo.ReadFile(self)
	if err != nil {
		t.Skip("test binary has no build info")
	}
	main := map[string]string{}
	for _, n := range []string{"baft", "baft-pair", "baft-agent", "baft-bcc"} {
		main[n] = bi.Path
	}
	r.env = &Env{UnitDir: r.p("units"), BinDir: r.p("bin"), Prefix: r.p("opt"), ConfigDir: r.p("etc"), StateDir: r.p("var"),
		AgentDir: r.p("agent"), AgentStateDir: r.p("agentstate"), JournalDir: r.p("journal"), BackupDir: r.p("backups"),
		SkelDir: r.p("skel"), System: r.sys, MainPkg: main}
	return r
}

func (r *rig) write(rel, content string, mode os.FileMode) string {
	r.t.Helper()
	p := r.p(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		r.t.Fatal(err)
	}
	return p
}

// binaries installs copies of the test binary under BAFT's names; MainPkg
// makes them "BAFT builds".
func (r *rig) binaries(names ...string) {
	r.t.Helper()
	self, _ := os.Executable()
	b, err := os.ReadFile(self)
	if err != nil {
		r.t.Fatal(err)
	}
	if len(names) == 0 {
		names = []string{"baft", "baft-pair", "baft-agent", "baft-bcc"}
	}
	for _, n := range names {
		if err := os.WriteFile(r.p("bin/"+n), b, 0o755); err != nil {
			r.t.Fatal(err)
		}
	}
}

const pemCert = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"
const pemKey = "-----BEGIN PRIVATE KEY-----\nMIIB\n-----END PRIVATE KEY-----\n"

func (r *rig) exConfig(dir string) string {
	return fmt.Sprintf(`schema_version: 1
node:
  id: ex-01
  role: listener
server:
  listen: 0.0.0.0:8443
  server_name: ex.example
  allowed_peer_identities:
    - urn:baft:node:ir-01
tls:
  min_version: "1.3"
  ca_file: %[1]s/pki/ca.pem
  cert_file: %[1]s/pki/server.pem
  key_file: %[1]s/pki/server.key
  session_tickets: false
transport:
  primary: h2
  h3_enabled: false
  shards: 4
  profile: secure-fast
limits:
  max_flows: 256
  data_memory_mib: 256
  receive_initial_kib: 64
  receive_max_mib: 16
  replay_max_mib: 16
recovery:
  enabled: false
  retention_seconds: 30
routes:
  - id: service-main
    direction: inbound
    target: 127.0.0.1:2443
    allowed_peers:
      - urn:baft:node:ir-01
management:
  unix_socket: /run/baft/admin.sock
  metrics_listen: 127.0.0.1:9191
logging:
  level: info
  payload: false
`, r.p(dir))
}

// installerEX lays down what install.sh leaves for an EX: unit, config,
// Noise key, PKI, release state, a stale pairing secret and the socket.
func (r *rig) installerEX(active bool) {
	r.t.Helper()
	r.binaries("baft", "baft-pair")
	r.write("etc/baft.yaml", r.exConfig("etc"), 0o640)
	r.write("etc/noise-key.json", `{"version":1,"private":"cHJpdg","public":"cHVi"}`+"\n", 0o600)
	r.write("etc/pki/ca.pem", pemCert, 0o644)
	r.write("etc/pki/server.pem", pemCert, 0o644)
	r.write("etc/pki/ca.key", pemKey, 0o600)
	r.write("etc/pki/server.key", pemKey, 0o600)
	r.write("opt/release-state.json", `{"schema_version":1,"version":"v0.1.1","commit":"abc"}`+"\n", 0o644)
	r.write("var/pairing.psk", "secret\n", 0o600)
	r.write("units/baft.service", renderServiceUnit(serviceParams{User: "baft", Bin: r.p("bin/baft"), Config: r.p("etc/baft.yaml"), StateDir: r.p("var")}), 0o644)
	if active {
		r.sys.set("baft.service", "active", "enabled")
	} else {
		r.sys.set("baft.service", "inactive", "enabled")
	}
}

func (r *rig) socket(rel string) {
	r.t.Helper()
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: r.p(rel), Net: "unix"})
	if err != nil {
		r.t.Skip("unix sockets unavailable: ", err)
	}
	l.SetUnlinkOnClose(false)
	l.Close()
}

func (r *rig) agent(active bool) {
	r.t.Helper()
	r.binaries("baft-agent")
	r.write("agent/token", "tok\n", 0o600)
	r.write("agent/bcc-job.pub", "jobkey\n", 0o600)
	r.write("agent/release-root.pub", "rootkey\n", 0o600)
	r.write("agentstate/seen-jobs.json", `{"jobs":[]}`, 0o600)
	p := agentParams{AgentBin: r.p("bin/baft-agent"), BCCURL: "https://bcc.example.com", NodeID: "ex-1", AgentDir: r.p("agent"), ReleaseRoot: true,
		AgentStateDir: r.p("agentstate"), ReleaseState: r.p("opt/release-state.json"), BinDir: r.p("bin"), Service: "baft", ConfigDir: r.p("etc"),
		BaftStateDir: r.p("var"), User: "baft", MetricsListen: "127.0.0.1:9191", Interval: "30s", Prefix: r.p("opt"), SystemdDir: r.p("units")}
	r.write("units/baft-agent.service", renderAgentUnit(p), 0o644)
	if active {
		r.sys.set("baft-agent.service", "active", "enabled")
	}
}

// snapshot hashes every file under the rig (except the journal and backups).
func (r *rig) snapshot() map[string]string {
	r.t.Helper()
	out := map[string]string{}
	filepath.Walk(r.root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(r.root, p)
		if strings.HasPrefix(rel, "journal") || strings.HasPrefix(rel, "backups") {
			return nil
		}
		switch {
		case fi.Mode().IsRegular():
			s, _, _ := hashRegular(p)
			out[rel] = s + " " + fi.Mode().String()
		case fi.IsDir():
			out[rel+"/"] = fi.Mode().String()
		default:
			out[rel] = fi.Mode().String()
		}
		return nil
	})
	return out
}

func diffSnap(a, b map[string]string) []string {
	var d []string
	for k, v := range a {
		if b[k] != v {
			d = append(d, k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			d = append(d, "+"+k)
		}
	}
	sort.Strings(d)
	return d
}

func paths(l []*Artifact) []string {
	var out []string
	for _, a := range l {
		out = append(out, a.Path)
	}
	return out
}

func has(l []*Artifact, path string) *Artifact {
	for _, a := range l {
		if a.Path == path {
			return a
		}
	}
	return nil
}

func (r *rig) plan(o Options) *Plan {
	r.t.Helper()
	return r.env.BuildPlan(context.Background(), o)
}

func copyFile(t *testing.T, dst string, src io.Reader) {
	t.Helper()
	f, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := io.Copy(f, src); err != nil {
		t.Fatal(err)
	}
}
