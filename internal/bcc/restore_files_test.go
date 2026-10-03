package bcc

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRestoreFilesOfflineUsesTransactionalRestoreAndAudit(t *testing.T) {
	root := t.TempDir()
	stateFile := filepath.Join(root, "state.db")
	backupFile := filepath.Join(root, "snapshot.baftbak")
	key := backupTestKey()

	store, err := OpenStore(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertNode(Node{ID: "n-before", Alias: "Before", Address: "127.0.0.1:21001", Role: "foreign"}, "token-before"); err != nil {
		t.Fatal(err)
	}
	app, err := NewServer(store, "admin")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	if _, err := app.BackupToFile(backupFile, key, at); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertNode(Node{ID: "n-after", Alias: "After", Address: "127.0.0.1:21002", Role: "foreign"}, "token-after"); err != nil {
		t.Fatal(err)
	}

	if err := RestoreFiles(stateFile, backupFile, key, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	nodes := reopened.ListNodes()
	if len(nodes) != 1 || nodes[0].ID != "n-before" {
		t.Fatalf("nodes after restore=%+v", nodes)
	}
	audit, err := OpenAuditLog(stateFile + ".audit.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := audit.List(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 || entries[len(entries)-1].Action != "backup.restore" || entries[len(entries)-1].Outcome != "success" {
		t.Fatalf("restore audit=%+v", entries)
	}
}

func TestRestoreFilesRefusesWhileBCCStateLockIsHeld(t *testing.T) {
	root := t.TempDir()
	stateFile := filepath.Join(root, "state.db")
	backupFile := filepath.Join(root, "snapshot.baftbak")
	key := backupTestKey()

	store, err := OpenStore(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertNode(Node{ID: "n1", Alias: "N1", Address: "127.0.0.1:22001", Role: "foreign"}, "token"); err != nil {
		t.Fatal(err)
	}
	app, err := NewServer(store, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.BackupToFile(backupFile, key, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	release, err := LockState(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	err = RestoreFiles(stateFile, backupFile, key, time.Now().UTC())
	if err == nil || !strings.Contains(err.Error(), "stop BCC before restoring") {
		t.Fatalf("restore with live lock err=%v", err)
	}
}
