package bcc

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReconcileTopologyFailedSaveDoesNotKeepUnpersistedBindings(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertNode(Node{ID: "ir", Address: "127.0.0.1:27101", Role: "worker"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertNode(Node{ID: "ex", Address: "127.0.0.1:27102", Role: "foreign"}, ""); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.st.IRPool = map[string]IRPoolMember{
		"ir": {NodeID: "ir", Enabled: true},
	}
	store.st.EXRoutes = map[string]ExplicitEXRoute{
		"r1": {ID: "r1", EXNode: "ex", Enabled: true},
	}
	store.st.TopologyBindings = nil
	if err := store.saveLocked(); err != nil {
		store.mu.Unlock()
		t.Fatal(err)
	}
	store.mu.Unlock()

	blocked := filepath.Join(dir, "state-directory")
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	store.path = blocked
	if _, err := store.ReconcileTopology(time.Now().UTC()); err == nil {
		t.Fatal("injected state write failure unexpectedly succeeded")
	}
	if store.st.TopologyBindings != nil {
		t.Fatalf("failed reconcile left unpersisted bindings: %+v", store.st.TopologyBindings)
	}

	store.path = dbPath
	reopened, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.st.TopologyBindings) != 0 {
		t.Fatalf("failed reconcile reached disk: %+v", reopened.st.TopologyBindings)
	}
}
