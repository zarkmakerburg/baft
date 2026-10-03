package uninstall

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// runInstaller renders a unit with install.sh's own shell function.
func runInstaller(t *testing.T, fn string, env map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	script := `set -euo pipefail; . <(sed -n '/^` + fn + `()/,/^}/p' ../../install.sh); ` + fn
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s: %v", fn, err)
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
