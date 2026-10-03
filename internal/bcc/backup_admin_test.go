package bcc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAdminRestorePreviewAPIIsBoundedToConfiguredBackupDir(t *testing.T) {
	root := t.TempDir()
	stateFile := filepath.Join(root, "state.db")
	store, err := OpenStore(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertNode(Node{ID: "n-before", Alias: "Before", Address: "127.0.0.1:20001", Role: "foreign"}, "secret-before"); err != nil {
		t.Fatal(err)
	}
	app, err := NewServer(store, "admin-secret")
	if err != nil {
		t.Fatal(err)
	}
	backupDir := filepath.Join(root, "backups")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	key := backupTestKey()
	if err := app.ConfigureBackupAdmin(backupDir, key); err != nil {
		t.Fatal(err)
	}
	name := "daily-20261003T000000Z.baftbak"
	if _, err := app.BackupToFile(filepath.Join(backupDir, name), key, time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertNode(Node{ID: "n-after", Alias: "After", Address: "127.0.0.1:20002", Role: "foreign"}, "secret-after"); err != nil {
		t.Fatal(err)
	}

	h := app.Handler()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authReq(http.MethodGet, "/api/backups", "", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized list status=%d body=%s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authReq(http.MethodGet, "/api/backups", "admin-secret", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rr.Code, rr.Body.String())
	}
	var list struct {
		Backups []BackupAdminEntry `json:"backups"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Backups) != 1 || list.Backups[0].Name != name {
		t.Fatalf("backups=%+v", list.Backups)
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authReq(http.MethodPost, "/api/backups/restore-preview", "admin-secret", map[string]string{"filename": "../" + name}))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("path traversal status=%d body=%s", rr.Code, rr.Body.String())
	}

	linkName := "daily-symlink.baftbak"
	if err := os.Symlink(filepath.Join(backupDir, name), filepath.Join(backupDir, linkName)); err != nil {
		t.Fatal(err)
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authReq(http.MethodPost, "/api/backups/restore-preview", "admin-secret", map[string]string{"filename": linkName}))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("symlink status=%d body=%s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authReq(http.MethodPost, "/api/backups/restore-preview", "admin-secret", map[string]string{"filename": name}))
	if rr.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", rr.Code, rr.Body.String())
	}
	var preview RestorePreview
	if err := json.Unmarshal(rr.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if !preview.Verified || preview.Problem != "" || preview.WouldRefuse != "" {
		t.Fatalf("preview=%+v", preview)
	}
	if preview.Nodes.Current != 2 || preview.Nodes.After != 1 {
		t.Fatalf("node diff=%+v", preview.Nodes)
	}
}

func TestAdminRestorePreviewDisabledWithoutBackupConfiguration(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	app, err := NewServer(store, "admin")
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, authReq(http.MethodGet, "/api/backups", "admin", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}
