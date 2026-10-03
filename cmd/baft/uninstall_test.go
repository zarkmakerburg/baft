package main

import (
	"bufio"
	"context"
	"debug/buildinfo"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/zarkmakerburg/baft/internal/uninstall"
)

type fakeSystemd struct {
	mu      sync.Mutex
	active  map[string]string
	enabled map[string]string
	calls   []string
}

func (f *fakeSystemd) Systemctl(ctx context.Context, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.HasPrefix(args[0], "is-") {
		f.calls = append(f.calls, strings.Join(args, " "))
	}
	u := ""
	if len(args) > 1 {
		u = args[1]
	}
	switch args[0] {
	case "is-active":
		if s := f.active[u]; s != "" {
			return s, nil
		}
		return "inactive", nil
	case "is-enabled":
		if s := f.enabled[u]; s != "" {
			return s, nil
		}
		return "disabled", nil
	case "stop":
		f.active[u] = "inactive"
	case "start":
		f.active[u] = "active"
	case "disable":
		f.enabled[u] = "disabled"
	case "enable":
		f.enabled[u] = "enabled"
	}
	return "", nil
}

func (f *fakeSystemd) Run(ctx context.Context, name string, args ...string) (string, error) {
	return `{"verified":true}`, nil
}

type uninstallTree struct {
	root string
	sys  *fakeSystemd
}

func (u *uninstallTree) p(rel string) string { return filepath.Join(u.root, rel) }

// newUninstallTree is a host with an installer-made IR on a fake tree; every
// uninstall path in this package points at it.
func newUninstallTree(t *testing.T) *uninstallTree {
	t.Helper()
	u := &uninstallTree{root: t.TempDir(), sys: &fakeSystemd{active: map[string]string{"baft.service": "active"}, enabled: map[string]string{"baft.service": "enabled"}}}
	for _, d := range []string{"units", "bin", "var", "opt", "journal", "backups"} {
		os.MkdirAll(u.p(d), 0o755)
	}
	writeDialerConfigIn(t, u.p("etc"), 0o600)
	self, _ := os.Executable()
	bi, err := buildinfo.ReadFile(self)
	if err != nil {
		t.Skip("no build info")
	}
	b, _ := os.ReadFile(self)
	for _, n := range []string{"baft", "baft-pair"} {
		os.WriteFile(u.p("bin/"+n), b, 0o755)
	}
	os.WriteFile(u.p("units/baft.service"), []byte(uninstall.InstallerServiceUnit("baft", u.p("bin/baft"), u.p("etc/baft.yaml"), u.p("var"), false)), 0o644)
	oldSys, oldMain, oldRoot, oldHook := uninstallSystem, uninstallMainPkg, uninstallRoot, uninstallEnvHook
	uninstallSystem = u.sys
	uninstallMainPkg = map[string]string{"baft": bi.Path, "baft-pair": bi.Path, "baft-agent": bi.Path, "baft-bcc": bi.Path}
	uninstallRoot = false
	uninstallEnvHook = func(e *uninstall.Env) {
		e.UnitDir, e.BinDir, e.Prefix, e.ConfigDir, e.StateDir = u.p("units"), u.p("bin"), u.p("opt"), u.p("etc"), u.p("var")
		e.AgentDir, e.AgentStateDir, e.JournalDir, e.BackupDir = u.p("agent"), u.p("agentstate"), u.p("journal"), u.p("backups")
		e.RefUnitDirs = nil
	}
	t.Cleanup(func() {
		uninstallSystem, uninstallMainPkg, uninstallRoot, uninstallEnvHook = oldSys, oldMain, oldRoot, oldHook
	})
	return u
}

func (u *uninstallTree) snapshot() string {
	var b strings.Builder
	filepath.Walk(u.root, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !strings.Contains(p, "/journal") {
			c := ""
			if fi.Mode().IsRegular() {
				x, _ := os.ReadFile(p)
				c = string(x)
			}
			b.WriteString(p + "|" + fi.Mode().String() + "|" + c + "\n")
		}
		return nil
	})
	return b.String()
}

func runUn(args []string, input string, interactive bool) (int, string, string) {
	var out, errb strings.Builder
	code := uninstallMain(context.Background(), args, uninstallIO{in: bufio.NewReader(strings.NewReader(input)), out: &out, errw: &errb, interactive: interactive})
	return code, out.String(), errb.String()
}

func TestUninstallUsage(t *testing.T) {
	newUninstallTree(t)
	for _, args := range [][]string{{"--bogus"}, {"extra"}, {"--json"}, {"--agent", "--delete-bcc-state"}, {"--no-backup"}, {"--resume", "--restore"}} {
		if code, _, _ := runUn(args, "", false); code != 2 {
			t.Errorf("%v: exit %d", args, code)
		}
	}
}

func TestUninstallPreviewChangesNothing(t *testing.T) {
	u := newUninstallTree(t)
	before := u.snapshot()
	code, out, _ := runUn([]string{"--preview"}, "", false)
	if code != 0 || !strings.Contains(out, "BAFT Uninstall Preview") || !strings.Contains(out, "nothing was changed") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	code, out, _ = runUn([]string{"--preview", "--json", "--full"}, "", false)
	var j map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &j) != nil || j["default"] != "KEEP DATA" {
		t.Fatalf("json exit %d: %s", code, out)
	}
	if u.snapshot() != before || len(u.sys.calls) != 0 {
		t.Fatalf("preview changed something: %v", u.sys.calls)
	}
}

func TestUninstallNeedsExplicitConsent(t *testing.T) {
	u := newUninstallTree(t)
	before := u.snapshot()
	code, out, _ := runUn([]string{"--services"}, "", false)
	if code != 4 || !strings.Contains(out, "--stop-active-tunnels and --yes") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if code, _, _ := runUn([]string{"--services", "--yes"}, "", false); code != 4 {
		t.Fatalf("active tunnel without consent: exit %d", code)
	}
	if u.snapshot() != before || len(u.sys.calls) != 0 {
		t.Fatal("changed without consent")
	}
	code, out, errOut := runUn([]string{"--services", "--yes", "--stop-active-tunnels"}, "", false)
	if code != 0 || !strings.Contains(out, "Uninstall finished and verified") {
		t.Fatalf("exit %d:\n%s\n%s", code, out, errOut)
	}
	if _, err := os.Stat(u.p("units/baft.service")); err == nil {
		t.Fatal("unit kept")
	}
	if _, err := os.Stat(u.p("etc/baft.yaml")); err != nil {
		t.Fatal("config removed without --delete-tunnel-configs")
	}
}

func TestUninstallBlockedByConflict(t *testing.T) {
	u := newUninstallTree(t)
	unit, _ := os.ReadFile(u.p("units/baft.service"))
	os.WriteFile(u.p("units/baft.service"), append([]byte("# baft-managed: true\n# baft-tunnel: t1\n"), unit...), 0o644)
	before := u.snapshot()
	code, out, _ := runUn([]string{"--full", "--yes", "--stop-active-tunnels"}, "", false)
	if code != 3 || !strings.Contains(out, "OWNERSHIP_CONFLICT") || !strings.Contains(out, "Nothing was changed") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if u.snapshot() != before {
		t.Fatal("changed while blocked")
	}
}

func TestUninstallInteractiveConfirmations(t *testing.T) {
	u := newUninstallTree(t)
	before := u.snapshot()
	// Data: no; tunnels: no -> nothing happens.
	code, out, _ := runUn([]string{"--full"}, "n\nn\n", true)
	if code != 4 || !strings.Contains(out, "Delete tunnel configs?") || !strings.Contains(out, "Not confirmed. Nothing was changed.") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	// Anything but the exact word is no.
	if code, _, _ := runUn([]string{"--full"}, "n\ny\nyes\n", true); code != 4 {
		t.Fatalf("loose final confirmation accepted: %d", code)
	}
	if u.snapshot() != before {
		t.Fatal("changed without confirmation")
	}
	// Delete tunnel configs: yes; stop tunnels: yes; type uninstall.
	code, out, errOut := runUn([]string{"--full"}, "y\ny\nuninstall\n", true)
	if code != 0 {
		t.Fatalf("exit %d:\n%s\n%s", code, out, errOut)
	}
	if _, err := os.Stat(u.p("etc/baft.yaml")); err == nil {
		t.Fatal("chosen config kept")
	}
}

func TestUninstallPendingRunAndRestore(t *testing.T) {
	u := newUninstallTree(t)
	dir := u.p("journal/run-20261003T000000.000000000Z-1-0")
	os.MkdirAll(filepath.Join(dir, "quarantine"), 0o700)
	os.WriteFile(filepath.Join(dir, "journal.json"), []byte(`{"version":1,"id":"run-x","status":"applying","services":[{"unit":"baft.service","kind":"transport","was_active":true,"was_enabled":true,"stopped":true,"disabled":true}],"files":[]}`), 0o600)
	u.sys.active["baft.service"], u.sys.enabled["baft.service"] = "inactive", "disabled"
	code, out, _ := runUn([]string{"--yes", "--stop-active-tunnels"}, "", false)
	if code != 5 || !strings.Contains(out, "--resume") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	code, out, _ = runUn([]string{"--restore"}, "", false)
	if code != 0 || !strings.Contains(out, "Restored") {
		t.Fatalf("restore exit %d:\n%s", code, out)
	}
	if u.sys.active["baft.service"] != "active" || u.sys.enabled["baft.service"] != "enabled" {
		t.Fatalf("service not back: %v %v", u.sys.active, u.sys.enabled)
	}
	if code, out, _ := runUn([]string{"--resume"}, "", false); code != 0 || !strings.Contains(out, "No unfinished uninstall") {
		t.Fatalf("resume after restore: %d %s", code, out)
	}
}

func TestMenuUninstallSubmenu(t *testing.T) {
	u := newUninstallTree(t)
	r := newMenuRig(t)
	before := u.snapshot()
	r.run(t, monoCaps, "13\n6\n\n5\nn\nn\n\n0\n\n0\n")
	o := r.out.String()
	for _, want := range []string{"BAFT Uninstall", "1 Remove BAFT binaries only", "2 Remove Agent only", "3 Remove BCC only",
		"4 Remove BAFT services + binaries", "5 Full uninstall", "6 Preview uninstall", "BAFT Uninstall Preview", "Not confirmed. Nothing was changed."} {
		if !strings.Contains(stripANSI(o), want) {
			t.Errorf("menu lacks %q", want)
		}
	}
	if u.snapshot() != before || len(u.sys.calls) != 0 {
		t.Fatalf("menu changed something: %v", u.sys.calls)
	}
}
