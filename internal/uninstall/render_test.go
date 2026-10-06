package uninstall

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runInstaller renders a unit with install.sh's own shell function. Extracting
// the function to a real temporary file is portable across Linux and macOS;
// /dev/fd process substitution is not reliable in the stripped test env.
func runInstaller(t *testing.T, fn string, env map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	raw, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	start := fn + "() {"
	var body []string
	in := false
	for _, line := range lines {
		if !in {
			if line != start {
				continue
			}
			in = true
		}
		body = append(body, line)
		if in && line == "}" {
			break
		}
	}
	if len(body) == 0 || body[len(body)-1] != "}" {
		t.Fatalf("%s: function not found in install.sh", fn)
	}
	path := filepath.Join(t.TempDir(), fn+".sh")
	if err := os.WriteFile(path, []byte(strings.Join(body, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := `set -euo pipefail; . "$1"; ` + fn
	cmd := exec.Command("bash", "-c", script, "bash", path)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", fn, err, out)
	}
	return string(out)
}

func TestServiceUnitMatchesInstaller(t *testing.T) {
	for _, port := range []string{"8443", "443"} {
		env := map[string]string{"BAFT_USER": "baft", "BAFT_BIN": "/usr/local/bin/baft", "CONFIG": "/etc/baft/baft.yaml", "BAFT_STATE_DIR": "/var/lib/baft", "BAFT_PORT": port}
		want := runInstaller(t, "render_service_unit", env)
		got := renderServiceUnit(serviceParams{User: "baft", Bin: "/usr/local/bin/baft", Config: "/etc/baft/baft.yaml", StateDir: "/var/lib/baft", Caps: port == "443"})
		if got != want {
			t.Fatalf("port %s: Go render differs from install.sh:\n--- go\n%s\n--- sh\n%s", port, got, want)
		}
		p, ok := installerServiceParams(want)
		if !ok || renderServiceUnit(p) != want {
			t.Fatalf("port %s: the installer's unit is not recognized: %+v", port, p)
		}
	}
}

func TestAgentUnitMatchesInstaller(t *testing.T) {
	base := map[string]string{
		"BAFT_AGENT_BIN": "/usr/local/bin/baft-agent", "BAFT_BCC_URL": "https://bcc.example.com", "BAFT_NODE_ID": "ex-1",
		"BAFT_AGENT_DIR": "/etc/baft-agent", "BAFT_AGENT_STATE_DIR": "/var/lib/baft-agent", "BAFT_RELEASE_STATE": "/opt/baft/release-state.json",
		"BAFT_BIN": "/usr/local/bin/baft", "BAFT_SERVICE": "baft", "BAFT_CONFIG_DIR": "/etc/baft", "BAFT_STATE_DIR": "/var/lib/baft",
		"BAFT_USER": "baft", "BAFT_METRICS_LISTEN": "127.0.0.1:9191", "BAFT_AGENT_INTERVAL": "30s", "BAFT_PREFIX": "/opt/baft",
		"BAFT_SYSTEMD_DIR": "/etc/systemd/system",
	}
	variants := map[string]map[string]string{
		"pinned root":   {"BAFT_ROOT_PUB": "NJq0"},
		"no root":       {"BAFT_ROOT_PUB": ""},
		"http + urls":   {"BAFT_ROOT_PUB": "NJq0", "BAFT_BCC_URL": "http://127.0.0.1:8080", "BAFT_AGENT_RELEASE_BASE_URL": "file:///rel", "BAFT_AGENT_REVOCATIONS_URL": "file:///rev.json"},
		"base url only": {"BAFT_ROOT_PUB": "", "BAFT_AGENT_RELEASE_BASE_URL": "https://mirror.example/rel"},
	}
	for name, v := range variants {
		t.Run(name, func(t *testing.T) {
			env := map[string]string{}
			for k, x := range base {
				env[k] = x
			}
			for k, x := range v {
				env[k] = x
			}
			want := runInstaller(t, "render_agent_unit", env)
			p, ok := installerAgentParams(want)
			if !ok {
				t.Fatalf("installer agent unit not parsed:\n%s", want)
			}
			if got := renderAgentUnit(p); got != want {
				t.Fatalf("Go render differs:\n--- go\n%s\n--- sh\n%s", got, want)
			}
			if p.NodeID != "ex-1" || p.AgentDir != "/etc/baft-agent" || p.ReleaseState != "/opt/baft/release-state.json" {
				t.Fatalf("params %+v", p)
			}
			// One changed byte is no longer the installer's.
			changed := strings.Replace(want, "RestartSec=10s", "RestartSec=11s", 1)
			if p2, ok := installerAgentParams(changed); ok && renderAgentUnit(p2) == changed {
				t.Fatal("edited unit still recognized")
			}
		})
	}
}

func TestCopyTreeKeepsModesAndLinks(t *testing.T) {
	src := t.TempDir() + "/src"
	os.MkdirAll(src+"/sub", 0o750)
	os.WriteFile(src+"/sub/a", []byte("a"), 0o640)
	os.WriteFile(src+"/b", []byte("bb"), 0o600)
	os.Symlink("sub/a", src+"/link")
	dst := t.TempDir() + "/dst"
	if err := copyTree(src, dst); err != nil {
		t.Fatal(err)
	}
	a, _ := treeHash(src)
	b, _ := treeHash(dst)
	if a == "" || a != b {
		t.Fatal("tree differs after copy")
	}
	fi, _ := os.Stat(dst + "/sub/a")
	if fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", fi.Mode())
	}
}

func TestBCCUnitFromInstallerIsRecognized(t *testing.T) {
	env := map[string]string{
		"BAFT_BCC_BIN":                 "/usr/local/bin/baft-bcc",
		"BAFT_BCC_LISTEN":              "127.0.0.1:8080",
		"BAFT_BCC_STATE_FILE":          "/var/lib/baft-bcc/bcc-state.json",
		"BAFT_BCC_ADMIN_TOKEN_FILE":    "/etc/baft-bcc/admin-token",
		"BAFT_BCC_ACCESS_FILE":         "/etc/baft-bcc/access.json",
		"BAFT_BCC_JOB_KEY_FILE":        "/etc/baft-bcc/job-key",
		"BAFT_BCC_BACKUP_DIR":          "/var/lib/baft-bcc/backups",
		"BAFT_BCC_STATE_DIR":           "/var/lib/baft-bcc",
		"BAFT_BCC_TLS_CERT":            "",
		"BAFT_BCC_TLS_KEY":             "",
		"BAFT_BCC_ALLOW_INSECURE_HTTP": "0",
	}
	unit := runInstaller(t, "render_bcc_unit", env)
	if !labelled(unit, "bcc") {
		t.Fatalf("installer BCC unit is not labelled for safe uninstall:\n%s", unit)
	}
	p := parseBCC(unit)
	if p == nil {
		t.Fatalf("installer BCC unit is not parseable:\n%s", unit)
	}
	if p.StateFile != "/var/lib/baft-bcc/bcc-state.json" ||
		p.AccessFile != "/etc/baft-bcc/access.json" ||
		p.JobKeyFile != "/etc/baft-bcc/job-key" ||
		p.BackupDir != "/var/lib/baft-bcc/backups" {
		t.Fatalf("wrong BCC paths parsed: %+v", p)
	}
	foundAdmin := false
	for _, path := range p.Operator {
		if path == "/etc/baft-bcc/admin-token" {
			foundAdmin = true
		}
	}
	if !foundAdmin {
		t.Fatalf("admin token must remain operator-protected: %+v", p.Operator)
	}
}

func TestBCCUnitNonLoopbackPinsItsOwnListenIP(t *testing.T) {
	env := map[string]string{
		"BAFT_BCC_BIN":                 "/usr/local/bin/baft-bcc",
		"BAFT_BCC_LISTEN":              "203.0.113.5:8443",
		"BAFT_BCC_STATE_FILE":          "/var/lib/baft-bcc/bcc-state.json",
		"BAFT_BCC_ADMIN_TOKEN_FILE":    "/etc/baft-bcc/admin-token",
		"BAFT_BCC_ACCESS_FILE":         "/etc/baft-bcc/access.json",
		"BAFT_BCC_JOB_KEY_FILE":        "/etc/baft-bcc/job-key",
		"BAFT_BCC_BACKUP_DIR":          "/var/lib/baft-bcc/backups",
		"BAFT_BCC_STATE_DIR":           "/var/lib/baft-bcc",
		"BAFT_BCC_TLS_CERT":            "/etc/baft-bcc/fullchain.pem",
		"BAFT_BCC_TLS_KEY":             "/etc/baft-bcc/privkey.pem",
		"BAFT_BCC_ALLOW_INSECURE_HTTP": "0",
	}
	unit := runInstaller(t, "render_bcc_unit", env)
	if !strings.Contains(unit, "Environment=BAFT_BCC_ALLOWED_LISTEN_IPS=203.0.113.5") {
		t.Fatalf("non-loopback listen IP is not pinned in the unit:\n%s", unit)
	}
	if !strings.Contains(unit, "--tls-cert /etc/baft-bcc/fullchain.pem --tls-key /etc/baft-bcc/privkey.pem") {
		t.Fatalf("TLS paths missing from unit:\n%s", unit)
	}
	p := parseBCC(unit)
	if p == nil {
		t.Fatal("non-loopback installer BCC unit is not parseable")
	}
}
