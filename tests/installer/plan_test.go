package installer_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

// planRig is a fake server: every path install.sh looks at lives under one
// temp directory, and systemctl and baft are stubs, so `--plan` can be run
// without root, network or systemd and the state matrix can be asserted.
type planRig struct {
	t    *testing.T
	dir  string
	env  []string
	bins string
}

type planOut struct {
	State   string `json:"state"`
	Changes int    `json:"changes"`
	Steps   []struct {
		Item, Action, Detail string
	} `json:"steps"`
}

func newPlanRig(t *testing.T) *planRig {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	u, err := user.Current()
	if err != nil {
		t.Skip("no current user")
	}
	me := u.Username
	d := t.TempDir()
	r := &planRig{t: t, dir: d, bins: filepath.Join(d, "stubs")}
	for _, p := range []string{"stubs", "opt", "etc", "var", "bin", "sysd", "agent", "agentstate", "bccetc", "bccvar"} {
		if err := os.MkdirAll(filepath.Join(d, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// systemctl: is-active / is-enabled answer from marker files.
	r.write("stubs/systemctl", `#!/bin/sh
case "$1" in
  is-active) [ -f "$STUB_DIR/active.$2" ] && echo active || echo inactive ;;
  is-enabled) [ -f "$STUB_DIR/enabled.$2" ] && echo enabled || echo disabled ;;
esac
exit 0
`, 0o755)
	r.env = append(os.Environ(),
		"PATH="+r.bins+":"+os.Getenv("PATH"),
		"STUB_DIR="+d,
		"BAFT_PREFIX="+filepath.Join(d, "opt"), "BAFT_CONFIG_DIR="+filepath.Join(d, "etc"),
		"BAFT_STATE_DIR="+filepath.Join(d, "var"),
		"BAFT_BIN="+filepath.Join(d, "bin/baft"), "BAFT_PAIR_BIN="+filepath.Join(d, "bin/baft-pair"),
		"BAFT_AGENT_BIN="+filepath.Join(d, "bin/baft-agent"),
		"BAFT_RELEASE_STATE="+filepath.Join(d, "opt/release-state.json"),
		"BAFT_SYSTEMD_DIR="+filepath.Join(d, "sysd"),
		"BAFT_AGENT_DIR="+filepath.Join(d, "agent"), "BAFT_AGENT_STATE_DIR="+filepath.Join(d, "agentstate"),
		"BAFT_BCC_BIN="+filepath.Join(d, "bin/baft-bcc"),
		"BAFT_BCC_CONFIG_DIR="+filepath.Join(d, "bccetc"),
		"BAFT_BCC_STATE_DIR="+filepath.Join(d, "bccvar"),
		"BAFT_BCC_STATE_FILE="+filepath.Join(d, "bccvar/bcc-state.json"),
		"BAFT_BCC_ADMIN_TOKEN_FILE="+filepath.Join(d, "bccetc/admin-token"),
		"BAFT_BCC_ACCESS_FILE="+filepath.Join(d, "bccetc/access.json"),
		"BAFT_BCC_JOB_KEY_FILE="+filepath.Join(d, "bccetc/job-key"),
		"BAFT_BCC_BACKUP_DIR="+filepath.Join(d, "bccvar/backups"),
		"BAFT_BCC_SERVICE=baft-bcc",
		"BAFT_USER="+me, "BAFT_NONINTERACTIVE=1",
	)
	return r
}

func (r *planRig) write(rel, content string, mode os.FileMode) {
	r.t.Helper()
	p := filepath.Join(r.dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		r.t.Fatal(err)
	}
}

func (r *planRig) mark(name string) { r.write(name, "", 0o644) }

// installed lays down a healthy EX install; validOK makes `config validate` pass.
func (r *planRig) installed(validOK bool) {
	rc := "1"
	if validOK {
		rc = "0"
	}
	r.write("bin/baft", "#!/bin/sh\n[ \"$1 $2\" = \"config validate\" ] && exit "+rc+"\nexit 0\n", 0o755)
	r.write("bin/baft-pair", "#!/bin/sh\nexit 0\n", 0o755)
	r.write("sysd/baft.service", "[Unit]\n", 0o644)
	r.write("etc/baft.yaml", "x: 1\n", 0o640)
	r.write("etc/noise-key.json", "{}", 0o600)
	r.write("opt/release-state.json", `{"version": "v0.1.0"}`, 0o600)
	for _, f := range []string{"ca.pem", "server.pem", "server.key"} {
		r.write("etc/pki/"+f, "x", 0o600)
	}
}

func (r *planRig) plan(args ...string) planOut {
	r.t.Helper()
	cmd := exec.Command("bash", append([]string{filepath.Join("..", "..", "install.sh"), "--plan", "--json"}, args...)...)
	cmd.Env = r.env
	out, err := cmd.Output()
	if err != nil {
		r.t.Fatalf("install.sh --plan: %v\n%s", err, out)
	}
	var p planOut
	if err := json.Unmarshal(out, &p); err != nil {
		r.t.Fatalf("not JSON: %v\n%s", err, out)
	}
	return p
}

func (p planOut) action(item string) string {
	for _, s := range p.Steps {
		if s.Item == item {
			return s.Action
		}
	}
	return ""
}

func TestPlanStateMatrix(t *testing.T) {
	cases := []struct {
		name  string
		setup func(r *planRig)
		args  []string
		state string
		want  map[string]string
	}{
		{"fresh", func(r *planRig) {}, []string{"--role", "ex"}, "FRESH_INSTALL",
			map[string]string{"noise key": "create", "outer PKI": "create", "pairing": "create", "systemd unit": "create"}},
		{"healthy", func(r *planRig) {
			r.installed(true)
			r.mark("active.baft.service")
			r.mark("enabled.baft.service")
		}, []string{"--role", "ex"}, "ALREADY_INSTALLED",
			map[string]string{"noise key": "keep", "outer PKI": "keep", "config": "keep", "pairing": "skip", "systemd unit": "keep", "service": "keep"}},
		{"stopped", func(r *planRig) {
			r.installed(true)
			r.mark("enabled.baft.service")
		}, []string{"--role", "ex"}, "REPAIR_REQUIRED",
			map[string]string{"service": "start", "noise key": "keep", "config": "keep"}},
		{"disabled", func(r *planRig) {
			r.installed(true)
			r.mark("active.baft.service")
		}, []string{"--role", "ex"}, "REPAIR_REQUIRED",
			map[string]string{"service enable": "update", "service": "keep"}},
		{"valid config but the binary is missing", func(r *planRig) {
			r.installed(true)
			os.Remove(filepath.Join(r.dir, "bin/baft"))
			r.mark("active.baft.service")
			r.mark("enabled.baft.service")
		}, []string{"--role", "ex"}, "PARTIAL_INSTALL",
			map[string]string{"baft binary": "create", "config": "keep", "pairing": "skip", "noise key": "keep", "outer PKI": "keep", "systemd unit": "keep"}},
		{"valid config but the binary does not run", func(r *planRig) {
			r.installed(true)
			r.write("bin/baft", "#!/bin/sh\nexit 1\n", 0o755)
			r.mark("active.baft.service")
			r.mark("enabled.baft.service")
		}, []string{"--role", "ex"}, "PARTIAL_INSTALL",
			map[string]string{"config": "keep", "pairing": "skip"}},
		{"invalid config", func(r *planRig) { r.installed(false) }, []string{"--role", "ex"}, "BROKEN_INSTALL",
			map[string]string{"install": "refuse"}},
		{"config without key", func(r *planRig) {
			r.installed(true)
			os.Remove(filepath.Join(r.dir, "etc/noise-key.json"))
		}, []string{"--role", "ex"}, "BROKEN_INSTALL", map[string]string{"install": "refuse"}},
		{"ex config without pki", func(r *planRig) {
			r.installed(true)
			os.RemoveAll(filepath.Join(r.dir, "etc/pki"))
		}, []string{"--role", "ex"}, "BROKEN_INSTALL", map[string]string{"install": "refuse"}},
		{"waiting for pairing", func(r *planRig) {
			r.installed(true)
			os.Remove(filepath.Join(r.dir, "etc/baft.yaml"))
			r.mark("var/pairing.ex.json")
		}, []string{"--role", "ex"}, "PARTIAL_INSTALL",
			map[string]string{"noise key": "keep", "outer PKI": "keep", "pairing": "create", "systemd unit": "keep"}},
		{"re-pair", func(r *planRig) {
			r.installed(true)
			r.mark("active.baft.service")
			r.mark("enabled.baft.service")
		}, []string{"--role", "ex", "--re-pair"}, "ALREADY_INSTALLED",
			map[string]string{"config": "replace", "pairing": "create", "noise key": "keep", "outer PKI": "keep"}},
		{"upgrade requested", func(r *planRig) {
			r.installed(true)
			r.mark("active.baft.service")
			r.mark("enabled.baft.service")
		}, []string{"--role", "ex", "--version", "v0.2.0"}, "UPGRADE_AVAILABLE",
			map[string]string{"baft binary": "update", "noise key": "keep", "config": "keep"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newPlanRig(t)
			c.setup(r)
			args := c.args
			if len(args) > 2 && args[2] == "--version" {
				r.env = append(r.env, "BAFT_VERSION="+args[3])
				args = args[:2]
			}
			p := r.plan(args...)
			if p.State != c.state {
				t.Fatalf("state = %s, want %s\n%+v", p.State, c.state, p.Steps)
			}
			for item, act := range c.want {
				if got := p.action(item); got != act {
					t.Errorf("%s: %q, want %q (%+v)", item, got, act, p.Steps)
				}
			}
		})
	}
}

// A healthy install's plan has no change at all, and nothing is ever created,
// replaced or regenerated for a credential.
func TestPlanHealthyHasNoChanges(t *testing.T) {
	r := newPlanRig(t)
	r.installed(true)
	r.mark("active.baft.service")
	r.mark("enabled.baft.service")
	p := r.plan("--role", "ex")
	if p.Changes != 0 {
		t.Fatalf("healthy install plans %d changes: %+v", p.Changes, p.Steps)
	}
}

// --plan is read-only: the fake tree is byte-identical afterwards.
func TestPlanDoesNotWrite(t *testing.T) {
	r := newPlanRig(t)
	r.installed(true)
	snap := func() string {
		var b strings.Builder
		filepath.Walk(r.dir, func(p string, i os.FileInfo, _ error) error {
			if i != nil && !i.IsDir() {
				c, _ := os.ReadFile(p)
				b.WriteString(p + "|" + i.Mode().String() + "|" + string(c) + "\n")
			}
			return nil
		})
		return b.String()
	}
	before := snap()
	r.plan("--role", "ex")
	r.plan("--role", "ir")
	if after := snap(); after != before {
		t.Fatal("--plan changed files")
	}
}

func TestPlanAgentOnly(t *testing.T) {
	r := newPlanRig(t)
	r.env = append(r.env, "BAFT_BCC_JOB_KEY=pubkey", "BAFT_AGENT_TOKEN=tok", "BAFT_NODE_ID=ex-1",
		"BAFT_BCC_URL=https://bcc.example.com")
	p := r.plan("--agent-only")
	if p.State != "FRESH_INSTALL" || p.action("agent token") != "create" || p.action("agent unit") != "create" {
		t.Fatalf("fresh agent: %+v", p)
	}
	// Installed with the same inputs: keep everything.
	r.installed(true)
	r.write("bin/baft-agent", "#!/bin/sh\n", 0o755)
	r.write("agent/token", "tok\n", 0o600)
	r.write("agent/bcc-job.pub", "pubkey\n", 0o600)
	r.write("agent/release-root.pub", "NJq0LmZ503x67pdXSuNGmSOqaiNJDpYf9nDE8Bwa-JI\n", 0o600)
	r.mark("active.baft-agent.service")
	p = r.plan("--agent-only")
	if p.action("agent token") != "keep" || p.action("BCC job key") != "keep" {
		t.Fatalf("same inputs must keep: %+v", p.Steps)
	}
	// A different token is a visible replacement, never silent.
	r.env = append(r.env, "BAFT_AGENT_TOKEN=other")
	p = r.plan("--agent-only")
	if p.action("agent token") != "replace" {
		t.Fatalf("different token: %+v", p.Steps)
	}
}
