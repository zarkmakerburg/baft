package bcc

import (
    "bytes"
    "encoding/base64"
    "encoding/json"
    "errors"
    "os"
    "path/filepath"
    "testing"
    "strings"
    "time"
)

// A failed rollback must preserve the prepared journal and refuse to claim
// success. Once the I/O obstacle is removed, startup recovery can complete.
func TestRestoreJournalRollbackIOFailureThenRecovery(t *testing.T) {
    dir := t.TempDir()
    statePath := filepath.Join(dir, "state.db")
    auditPath := statePath + ".audit.jsonl"
    oldState, oldAudit := []byte("old state"), []byte("old audit")
    currentState, currentAudit := []byte("candidate state"), []byte("candidate audit")
    if err := os.WriteFile(statePath, currentState, 0600); err != nil { t.Fatal(err) }
    if err := os.WriteFile(auditPath, currentAudit, 0600); err != nil { t.Fatal(err) }

    j := restoreJournal{
        Version: restoreJournalVersion, Phase: "prepared",
        HadState: true, HadAudit: true,
        OldStateB64: base64.StdEncoding.EncodeToString(oldState),
        OldAuditB64: base64.StdEncoding.EncodeToString(oldAudit),
        StateStage: statePath + ".restore-state.stage",
        AuditStage: auditPath + ".restore-audit.stage",
    }
    journalPath := restoreJournalPath(statePath)
    raw, err := json.Marshal(j)
    if err != nil { t.Fatal(err) }
    if err := os.WriteFile(journalPath, raw, 0600); err != nil { t.Fatal(err) }

    // writeAtomic writes to statePath+".tmp"; a directory there simulates an
    // independent rollback write failure without depending on root/chmod.
    obstacle := statePath + ".tmp"
    if err := os.Mkdir(obstacle, 0700); err != nil { t.Fatal(err) }
    if err := rollbackFromJournal(j, statePath); err == nil {
        t.Fatal("rollback reported success despite I/O failure")
    }
    for path, want := range map[string][]byte{statePath: currentState, auditPath: currentAudit, journalPath: raw} {
        got, err := os.ReadFile(path)
        if err != nil || !bytes.Equal(got, want) { t.Fatalf("after failure %s: err=%v got=%q", path, err, got) }
    }
    if err := recoverRestoreTransaction(statePath); err == nil {
        t.Fatal("startup recovery reported success while I/O obstacle remained")
    }

    if err := os.Remove(obstacle); err != nil { t.Fatal(err) }
    if err := recoverRestoreTransaction(statePath); err != nil { t.Fatal(err) }
    for path, want := range map[string][]byte{statePath: oldState, auditPath: oldAudit} {
        got, err := os.ReadFile(path)
        if err != nil || !bytes.Equal(got, want) { t.Fatalf("after recovery %s: err=%v got=%q", path, err, got) }
    }
    if _, err := os.Stat(journalPath); !errors.Is(err, os.ErrNotExist) {
        t.Fatalf("journal should be removed after recovery: %v", err)
    }
}

// An in-process restore can fail after swapping state, and its rollback can
// independently fail. Both failures must reach the caller while the journal
// remains available for startup recovery.
func TestRestoreReportsRollbackIOFailure(t *testing.T) {
    dir := t.TempDir()
    statePath := filepath.Join(dir, "state.db")
    store, err := OpenStore(statePath)
    if err != nil { t.Fatal(err) }
    app, err := NewServer(store, "admin")
    if err != nil { t.Fatal(err) }
    if _, err := store.UpsertNode(Node{ID: "fixture-node", Alias: "Fixture", Address: "127.0.0.1:21001", Role: "foreign"}, ""); err != nil { t.Fatal(err) }
    if _, err := os.Stat(statePath); err != nil { t.Fatalf("persistent state precondition: %v", err) }
    backupPath := filepath.Join(dir, "snapshot.baftbak")
    key := backupTestKey()
    if _, err := app.BackupToFile(backupPath, key, time.Now().UTC()); err != nil { t.Fatal(err) }

    cause := errors.New("injected after state commit")
    obstacle := statePath + ".tmp"
    app.restoreFault = func(stage string) error {
        if stage != "after_state_commit" { return nil }
        if err := os.Mkdir(obstacle, 0700); err != nil { return err }
        return cause
    }
    err = app.RestoreFromFile(backupPath, key)
    if !errors.Is(err, cause) || !strings.Contains(err.Error(), "restore rollback failed") {
        t.Fatalf("must report commit and rollback failures, got %v", err)
    }
    if _, err := os.Stat(restoreJournalPath(statePath)); err != nil {
        t.Fatalf("prepared journal must survive rollback failure: %v", err)
    }
    if err := os.Remove(obstacle); err != nil { t.Fatal(err) }
    if err := recoverRestoreTransaction(statePath); err != nil { t.Fatal(err) }
    if _, err := os.Stat(restoreJournalPath(statePath)); !errors.Is(err, os.ErrNotExist) {
        t.Fatalf("journal should clear after recovery: %v", err)
    }
}
