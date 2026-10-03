package bcc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// emergencyDir builds an emergency backup by hand: real state and audit, a
// manifest with matching digests. mutate may change a file afterwards; fix
// recomputes the manifest so only the content check can catch it.
func emergencyDir(t *testing.T, mutate func(dir string), fix bool) string {
	t.Helper()
	src := t.TempDir()
	state := filepath.Join(src, "s.db")
	st, err := OpenStore(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertNode(Node{ID: "n1", Address: "192.0.2.9:443", Role: "worker"}, "tok"); err != nil {
		t.Fatal(err)
	}
	a, _ := OpenAuditLog(state + ".audit.jsonl")
	a.Append(AuditEntry{Actor: "admin", Action: "node.upsert", Outcome: "success"})
	a.Append(AuditEntry{Actor: "admin", Action: "node.revoke", Outcome: "success"})
	dir := t.TempDir()
	type file struct {
		Name   string `json:"name"`
		Source string `json:"source"`
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
	}
	names := map[string]string{"s.db": state, "s.db.audit.jsonl": state + ".audit.jsonl"}
	for n, s := range names {
		b, _ := os.ReadFile(s)
		os.WriteFile(filepath.Join(dir, n), b, 0o600)
	}
	write := func() {
		var files []file
		for n, s := range names {
			sum, size, _ := fileSHA256(filepath.Join(dir, n))
			files = append(files, file{n, s, size, sum})
		}
		m, _ := json.Marshal(map[string]any{"kind": "baft-bcc-emergency-backup", "state_file": state, "files": files})
		os.WriteFile(filepath.Join(dir, "manifest.json"), m, 0o600)
	}
	write()
	if mutate != nil {
		mutate(dir)
		if fix {
			write()
		}
	}
	return dir
}

func TestVerifyEmergencyBackup(t *testing.T) {
	r := VerifyEmergencyBackup(emergencyDir(t, nil, false))
	if !r.Verified || r.Nodes != 1 || r.AuditEntries != 2 || r.Files != 2 {
		t.Fatalf("%+v", r)
	}
}

func TestVerifyEmergencyBackupRejects(t *testing.T) {
	cases := map[string]struct {
		mutate func(dir string)
		fix    bool
		want   string
	}{
		"file changed after the manifest": {func(d string) { os.WriteFile(filepath.Join(d, "s.db.audit.jsonl"), []byte("{}\n"), 0o600) }, false, "does not match the manifest"},
		"audit chain broken": {func(d string) {
			p := filepath.Join(d, "s.db.audit.jsonl")
			b, _ := os.ReadFile(p)
			os.WriteFile(p, []byte(strings.Replace(string(b), "node.revoke", "node.upsert", 1)), 0o600)
		}, true, "audit log"},
		"state database corrupt": {func(d string) {
			p := filepath.Join(d, "s.db")
			b, _ := os.ReadFile(p)
			for i := 100; i < len(b) && i < 4000; i++ {
				b[i] ^= 0xff
			}
			os.WriteFile(p, b, 0o600)
		}, true, "state"},
		"state missing": {func(d string) {
			os.Remove(filepath.Join(d, "s.db"))
			m, _ := os.ReadFile(filepath.Join(d, "manifest.json"))
			var man map[string]any
			json.Unmarshal(m, &man)
			var keep []any
			for _, f := range man["files"].([]any) {
				if f.(map[string]any)["name"] != "s.db" {
					keep = append(keep, f)
				}
			}
			man["files"] = keep
			m, _ = json.Marshal(man)
			os.WriteFile(filepath.Join(d, "manifest.json"), m, 0o600)
		}, false, "not in the backup"},
		"path in a name": {func(d string) {
			m, _ := os.ReadFile(filepath.Join(d, "manifest.json"))
			os.WriteFile(filepath.Join(d, "manifest.json"), []byte(strings.Replace(string(m), `"name":"s.db"`, `"name":"../s.db"`, 1)), 0o600)
		}, false, "bad file name"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := VerifyEmergencyBackup(emergencyDir(t, c.mutate, c.fix))
			if r.Verified || !strings.Contains(r.Problem, c.want) {
				t.Fatalf("%+v", r)
			}
		})
	}
}
