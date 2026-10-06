package installer_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanBCCOnlyFresh(t *testing.T) {
	r := newPlanRig(t)
	p := r.plan("--bcc-only")
	if p.State != "FRESH_INSTALL" {
		t.Fatalf("state=%s want FRESH_INSTALL: %+v", p.State, p.Steps)
	}
	for item, want := range map[string]string{
		"BCC binary":           "create",
		"BCC admin token":      "create",
		"BCC access":           "create",
		"BCC job key":          "create",
		"BCC bootstrap script": "create",
		"BCC SSH enrollment":   "skip",
		"BCC systemd unit":     "create",
		"BCC service":          "start",
	} {
		if got := p.action(item); got != want {
			t.Errorf("%s=%q want %q: %+v", item, got, want, p.Steps)
		}
	}
}

func TestPlanBCCOnlyDoesNotWrite(t *testing.T) {
	r := newPlanRig(t)
	before, err := exec.Command("sh", "-c", "find "+r.dir+" -type f -print | sort | xargs -r sha256sum").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	r.plan("--bcc-only")
	after, err := exec.Command("sh", "-c", "find "+r.dir+" -type f -print | sort | xargs -r sha256sum").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("--bcc-only --plan mutated the rig\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestBCCOnlyRejectsConflictingModes(t *testing.T) {
	r := newPlanRig(t)
	script := filepath.Join("..", "..", "install.sh")
	for _, args := range [][]string{
		{"--plan", "--bcc-only", "--role", "ex"},
		{"--plan", "--bcc-only", "--agent-only"},
	} {
		cmd := exec.Command("bash", append([]string{script}, args...)...)
		cmd.Env = r.env
		out, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(out), "--bcc-only") {
			t.Fatalf("args %v must fail clearly: err=%v\n%s", args, err, out)
		}
	}
}

func TestPlanBCCOnlyKeepsExistingCredentials(t *testing.T) {
	r := newPlanRig(t)
	r.write("bin/baft-bcc", "#!/bin/sh\nexit 0\n", 0o755)
	r.write("bccetc/admin-token", "admin\n", 0o600)
	r.write("bccetc/access.json", "{}\n", 0o600)
	r.write("bccetc/job-key", "key\n", 0o600)
	r.write("bccvar/bcc-state.json", "state\n", 0o600)
	r.write("sysd/baft-bcc.service", "# baft-managed: true\n# baft-component: bcc\n[Unit]\n", 0o644)
	r.mark("active.baft-bcc.service")
	r.mark("enabled.baft-bcc.service")

	p := r.plan("--bcc-only")
	for _, item := range []string{"BCC admin token", "BCC access", "BCC job key"} {
		if got := p.action(item); got != "keep" {
			t.Errorf("%s=%q want keep: %+v", item, got, p.Steps)
		}
	}
}

func TestBCCOnlyPublicListenFailsClosed(t *testing.T) {
	r := newPlanRig(t)
	script := filepath.Join("..", "..", "install.sh")
	cases := [][]string{
		{"--plan", "--bcc-only", "--bcc-listen", "0.0.0.0:8080"},
		{"--plan", "--bcc-only", "--bcc-tls-cert", "/tmp/cert-only.pem"},
		{"--plan", "--bcc-only", "--bcc-public-url", "http://bcc.example.test"},
		{"--plan", "--bcc-only", "--bcc-listen", "bcc.example.test:443", "--bcc-tls-cert", "/tmp/cert.pem", "--bcc-tls-key", "/tmp/key.pem"},
	}
	for _, args := range cases {
		cmd := exec.Command("bash", append([]string{script}, args...)...)
		cmd.Env = r.env
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("args %v unexpectedly succeeded:\n%s", args, out)
		}
		got := string(out)
		if !strings.Contains(got, "BCC") && !strings.Contains(got, "bcc") && !strings.Contains(got, "TLS") && !strings.Contains(got, "https") {
			t.Fatalf("args %v did not fail clearly:\n%s", args, out)
		}
	}
}

func TestBCCOnlyNonLoopbackTLSPlan(t *testing.T) {
	r := newPlanRig(t)
	p := r.plan(
		"--bcc-only",
		"--bcc-listen", "203.0.113.5:8443",
		"--bcc-tls-cert", "/etc/baft-bcc/fullchain.pem",
		"--bcc-tls-key", "/etc/baft-bcc/privkey.pem",
		"--bcc-public-url", "https://bcc.example.test",
	)
	if p.State != "FRESH_INSTALL" {
		t.Fatalf("state=%s want FRESH_INSTALL: %+v", p.State, p.Steps)
	}
	if got := p.action("BCC service"); got != "start" {
		t.Fatalf("BCC service=%q want start: %+v", got, p.Steps)
	}
	if got := p.action("BCC SSH enrollment"); got != "enable" {
		t.Fatalf("BCC SSH enrollment=%q want enable: %+v", got, p.Steps)
	}
	if got := p.action("BCC bootstrap script"); got != "create" {
		t.Fatalf("BCC bootstrap script=%q want create: %+v", got, p.Steps)
	}
}
