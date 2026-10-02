package bcc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func previewServer(t *testing.T, dir, name string) (*Store, *Server) {
	t.Helper()
	store, err := OpenStore(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	app, err := NewServer(store, "admin")
	if err != nil {
		t.Fatal(err)
	}
	return store, app
}

func addNode(t *testing.T, app *Server, id, env string) {
	t.Helper()
	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, authReq(http.MethodPost, "/api/nodes", "admin", map[string]any{"ID": id, "Alias": id, "Address": "127.0.0.1:32" + id[len(id)-1:] + "01", "Role": "foreign", "AgentTokenEnv": env}))
	if rr.Code != http.StatusCreated {
		t.Fatalf("add %s: %d %s", id, rr.Code, rr.Body.String())
	}
}

func dirSnapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			b, _ := os.ReadFile(p)
			out[p] = string(b)
		}
		return nil
	})
	return out
}

func TestRestorePreviewMatchesTheRealRestoreAndChangesNothing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PV_N1_OLD", "old1")
	t.Setenv("PV_N1_NEW", "new1")
	t.Setenv("PV_N2", "tok2")
	t.Setenv("PV_N3", "tok3")
	store, app := previewServer(t, dir, "state.json")
	addNode(t, app, "n1", "PV_N1_OLD")
	addNode(t, app, "n2", "PV_N2")
	backup := filepath.Join(dir, "b.baftbak")
	if _, err := app.BackupToFile(backup, backupTestKey(), time.Now().UTC().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	// After the backup: a third node, a token rotation and a revoke.
	addNode(t, app, "n3", "PV_N3")
	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, authReq(http.MethodPost, "/api/nodes/rotate-token", "admin", map[string]any{"node_id": "n1", "agent_token_env": "PV_N1_NEW", "grace_seconds": 0}))
	rr2 := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr2, authReq(http.MethodPost, "/api/nodes/revoke", "admin", map[string]any{"node_id": "n2", "reason": "post-backup"}))
	if rr.Code != 200 || rr2.Code != 200 {
		t.Fatalf("setup rotate=%d revoke=%d", rr.Code, rr2.Code)
	}

	before := dirSnapshot(t, dir)
	p, err := app.PreviewRestore(backup, backupTestKey())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, dirSnapshot(t, dir)) {
		t.Fatal("the preview changed files")
	}
	if !p.Verified || p.WouldRefuse != "" || !reflect.DeepEqual(p.Nodes.Removed, []string{"n3"}) {
		t.Fatalf("preview %+v", p)
	}
	if !reflect.DeepEqual(p.KeptSecurity, []string{"n1", "n2"}) {
		t.Fatalf("anti-rollback kept %v, want n1 (rotated) and n2 (revoked)", p.KeptSecurity)
	}
	if p.Nodes.Current != 3 || p.Nodes.After != 2 || len(p.Warnings) == 0 {
		t.Fatalf("diff %+v warnings %v", p.Nodes, p.Warnings)
	}
	if b, _ := json.Marshal(p); strings.Contains(string(b), tokenHash("tok2")) || strings.Contains(string(b), tokenHash("new1")) || strings.Contains(string(b), "new1") {
		t.Fatal("the preview exposes token material")
	}

	// The real restore does exactly what the preview said.
	if err := app.RestoreFromFile(backup, backupTestKey()); err != nil {
		t.Fatal(err)
	}
	after := stateSnapshotForTest(t, store)
	if len(after.Nodes) != p.Nodes.After {
		t.Fatalf("restored %d nodes, preview said %d", len(after.Nodes), p.Nodes.After)
	}
	if _, ok := after.Nodes["n3"]; ok {
		t.Fatal("n3 survived the restore the preview said would remove it")
	}
	if !after.Nodes["n2"].Revoked {
		t.Fatal("the revoke the preview said would be kept was rolled back")
	}
}

func TestRestorePreviewFilesReadsCopiesOnly(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PV_A", "a")
	store, app := previewServer(t, dir, "state.json")
	addNode(t, app, "n1", "PV_A")
	backup := filepath.Join(dir, "b.baftbak")
	if _, err := app.BackupToFile(backup, backupTestKey(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	addNode(t, app, "n2", "PV_A")

	before := dirSnapshot(t, dir)
	p, err := PreviewRestoreFiles(store.path, backup, backupTestKey(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, dirSnapshot(t, dir)) {
		t.Fatal("previewing from files changed the live directory")
	}
	want, _ := app.PreviewRestore(backup, backupTestKey())
	if !p.Verified || !reflect.DeepEqual(p.Nodes, want.Nodes) || p.AuditCurrent != want.AuditCurrent {
		t.Fatalf("file preview %+v differs from the server's %+v", p.Nodes, want.Nodes)
	}
}

func TestRestorePreviewRejectsWrongKeyAndTamper(t *testing.T) {
	dir := t.TempDir()
	_, app := previewServer(t, dir, "state.json")
	backup := filepath.Join(dir, "b.baftbak")
	if _, err := app.BackupToFile(backup, backupTestKey(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	wrong := make([]byte, 32)
	if p, _ := app.PreviewRestore(backup, wrong); p.Verified || p.Problem == "" {
		t.Fatalf("wrong key: %+v", p)
	}
	raw, _ := os.ReadFile(backup)
	os.WriteFile(backup, []byte(strings.Replace(string(raw), "\"ciphertext\": \"", "\"ciphertext\": \"AAAA", 1)), 0o600)
	if p, _ := app.PreviewRestore(backup, backupTestKey()); p.Verified || p.Problem == "" {
		t.Fatalf("tampered: %+v", p)
	}
}

func TestRestorePreviewSaysWhenTheRealRestoreWouldBeRefused(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PV_X", "x")
	t.Setenv("PV_Y", "y")
	// Backup from one BCC, current state from another with its own audit chain.
	_, other := previewServer(t, dir, "other.json")
	addNode(t, other, "n1", "PV_X")
	backup := filepath.Join(dir, "other.baftbak")
	if _, err := other.BackupToFile(backup, backupTestKey(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	_, app := previewServer(t, dir, "mine.json")
	addNode(t, app, "n9", "PV_Y")
	p, err := app.PreviewRestore(backup, backupTestKey())
	if err != nil {
		t.Fatal(err)
	}
	if p.WouldRefuse == "" {
		t.Fatalf("preview did not predict the refusal: %+v", p)
	}
	if err := app.RestoreFromFile(backup, backupTestKey()); err == nil || !strings.Contains(err.Error(), "restore refused") {
		t.Fatalf("real restore: %v", err)
	}
}
