// Package uninstall_test runs the uninstall engine against real BCC state:
// the emergency backup the uninstall takes before deleting BCC state must be
// something BCC's own reader and audit verifier accept.
package uninstall_test

import (
	"context"
	"debug/buildinfo"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zarkmakerburg/baft/internal/bcc"
	"github.com/zarkmakerburg/baft/internal/uninstall"
)

type sys struct {
	active map[string]string
}

func (s *sys) Systemctl(ctx context.Context, args ...string) (string, error) {
	switch args[0] {
	case "is-active":
		if v := s.active[args[1]]; v != "" {
			return v, nil
		}
		return "inactive", nil
	case "is-enabled":
		return "enabled", nil
	case "stop":
		s.active[args[1]] = "inactive"
	case "start":
		s.active[args[1]] = "active"
	}
	return "", nil
}

// Run stands in for `baft-bcc verify-backup --dir D --json`.
func (s *sys) Run(ctx context.Context, name string, args ...string) (string, error) {
	r := bcc.VerifyEmergencyBackup(args[2])
	b, _ := json.Marshal(r)
	return string(b), nil
}

func realBCC(t *testing.T, dir string) string {
	t.Helper()
	state := filepath.Join(dir, "state.db")
	st, err := bcc.OpenStore(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ex-1", "ir-1"} {
		if _, err := st.UpsertNode(bcc.Node{ID: id, Address: "192.0.2.1:443", Role: "worker"}, "token-"+id); err != nil {
			t.Fatal(err)
		}
	}
	a, err := bcc.OpenAuditLog(state + ".audit.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for _, act := range []string{"node.upsert", "node.upsert", "deploy.create"} {
		if _, err := a.Append(bcc.AuditEntry{Actor: "admin", Action: act, Outcome: "success"}); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(state+".access.json", []byte(`{"version":1}`), 0o600)
	os.WriteFile(state+".job-key", []byte("job-key"), 0o600)
	return state
}

func TestEmergencyBackupIsVerifiedByBCC(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"units", "bin", "bcc", "backups", "journal"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	state := realBCC(t, filepath.Join(root, "bcc"))
	self, _ := os.Executable()
	bi, err := buildinfo.ReadFile(self)
	if err != nil {
		t.Skip("no build info")
	}
	b, _ := os.ReadFile(self)
	os.WriteFile(filepath.Join(root, "bin", "baft-bcc"), b, 0o755)
	unit := "# baft-managed: true\n# baft-component: bcc\n[Service]\nExecStart=" + filepath.Join(root, "bin", "baft-bcc") +
		" --admin-token-file /x --state-file " + state + "\n"
	os.WriteFile(filepath.Join(root, "units", "baft-bcc.service"), []byte(unit), 0o644)
	s := &sys{active: map[string]string{"baft-bcc.service": "active"}}
	env := &uninstall.Env{UnitDir: filepath.Join(root, "units"), BinDir: filepath.Join(root, "bin"), Prefix: filepath.Join(root, "opt"),
		ConfigDir: filepath.Join(root, "etc"), StateDir: filepath.Join(root, "var"), AgentDir: filepath.Join(root, "agent"),
		AgentStateDir: filepath.Join(root, "agentstate"), JournalDir: filepath.Join(root, "journal"), BackupDir: filepath.Join(root, "backups"),
		System: s, MainPkg: map[string]string{"baft-bcc": bi.Path}}
	o := uninstall.Options{Scope: uninstall.Scope{BCC: true}, Delete: map[uninstall.Class]bool{uninstall.ClassBCCState: true, uninstall.ClassAudit: true}, Yes: true}
	p := env.BuildPlan(context.Background(), o)
	if p.Backup == nil {
		t.Fatalf("no backup planned; remove=%v", p.Remove)
	}
	res, err := env.Apply(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if res.Backup.VerifiedBy != "sha256+baft-bcc" || !strings.Contains(res.Backup.Summary, "2 node(s)") || !strings.Contains(res.Backup.Summary, "3 entr") {
		t.Fatalf("backup %+v", res.Backup)
	}
	if _, err := os.Stat(state); err == nil {
		t.Fatal("state not deleted")
	}
	// Restoring is copying the files back: BCC opens them as before.
	var man struct {
		Files []struct{ Name, Source string } `json:"files"`
	}
	raw, _ := os.ReadFile(filepath.Join(res.Backup.Dir, "manifest.json"))
	json.Unmarshal(raw, &man)
	for _, f := range man.Files {
		c, _ := os.ReadFile(filepath.Join(res.Backup.Dir, f.Name))
		os.WriteFile(f.Source, c, 0o600)
	}
	st, err := bcc.OpenStore(state)
	if err != nil {
		t.Fatalf("restored state does not open: %v", err)
	}
	_ = st
	if _, err := bcc.OpenAuditLog(state + ".audit.jsonl"); err != nil {
		t.Fatalf("restored audit: %v", err)
	}

	// A tampered copy is caught by BCC's verifier.
	ap := filepath.Join(res.Backup.Dir, "state.db.audit.jsonl")
	c, _ := os.ReadFile(ap)
	os.WriteFile(ap, []byte(strings.Replace(string(c), "deploy.create", "deploy.cancel", 1)), 0o600)
	if r := bcc.VerifyEmergencyBackup(res.Backup.Dir); r.Verified || !strings.Contains(r.Problem, "does not match the manifest") {
		t.Fatalf("tampered file verified: %+v", r)
	}
}
