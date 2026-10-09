package bcc

import (
    "os"
    "path/filepath"
    "testing"
)

func TestUpsertNodeSaveFailureDoesNotChangeMemoryOrDisk(t *testing.T) {
    dir := t.TempDir()
    statePath := filepath.Join(dir, "state.db")
    store, err := OpenStore(statePath)
    if err != nil { t.Fatal(err) }
    original := Node{ID: "n1", Alias: "Original", Address: "127.0.0.1:21001", Role: "foreign"}
    if _, err := store.UpsertNode(original, ""); err != nil { t.Fatal(err) }

    blocked := filepath.Join(dir, "state-directory")
    if err := os.Mkdir(blocked, 0700); err != nil { t.Fatal(err) }
    store.path = blocked
    if _, err := store.UpsertNode(Node{ID: "n2", Alias: "New", Address: "127.0.0.1:21002", Role: "foreign"}, ""); err == nil {
        t.Fatal("injected state write failure unexpectedly succeeded")
    }
    if _, ok := store.GetNode("n2"); ok { t.Fatal("failed insert leaked into memory") }

    changed := original
    changed.Alias = "Changed"
    if _, err := store.UpsertNode(changed, ""); err == nil {
        t.Fatal("injected update write failure unexpectedly succeeded")
    }
    got, ok := store.GetNode("n1")
    if !ok || got.Alias != original.Alias { t.Fatalf("failed update leaked into memory: %+v", got) }

    store.path = statePath
    reopened, err := OpenStore(statePath)
    if err != nil { t.Fatal(err) }
    if _, ok := reopened.GetNode("n2"); ok { t.Fatal("failed insert reached disk") }
    got, ok = reopened.GetNode("n1")
    if !ok || got.Alias != original.Alias { t.Fatalf("failed update reached disk: %+v", got) }
}
