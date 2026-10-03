package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyBackupCommand(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runVerifyBackup(nil, &out, &errb); code != 2 {
		t.Fatalf("no --dir: exit %d", code)
	}
	dir := t.TempDir()
	out.Reset()
	if code := runVerifyBackup([]string{"--dir", dir, "--json"}, &out, &errb); code != 1 {
		t.Fatalf("empty dir: exit %d", code)
	}
	var r map[string]any
	if json.Unmarshal(out.Bytes(), &r) != nil || r["verified"] != false || !strings.Contains(r["problem"].(string), "manifest") {
		t.Fatalf("json %s", out.String())
	}
	// A minimal valid backup: a JSON state file and no audit log.
	state := filepath.Join(dir, "state.json")
	os.WriteFile(state, []byte(`{"nodes":{},"jobs":{},"next_job":1}`), 0o600)
	sum := sha256File(t, state)
	m, _ := json.Marshal(map[string]any{"kind": "baft-bcc-emergency-backup", "state_file": "/var/lib/bcc/state.json",
		"files": []map[string]any{{"name": "state.json", "source": "/var/lib/bcc/state.json", "size": fileSize(t, state), "sha256": sum}}})
	os.WriteFile(filepath.Join(dir, "manifest.json"), m, 0o600)
	out.Reset()
	if code := runVerifyBackup([]string{"--dir", dir}, &out, &errb); code != 0 || !strings.Contains(out.String(), "emergency backup verified") {
		t.Fatalf("exit %d: %s %s", code, out.String(), errb.String())
	}
}

func sha256File(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return sha256Hex(b)
}

func fileSize(t *testing.T, p string) int64 {
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
