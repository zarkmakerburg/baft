package bcc

import (
	"os"
	"path/filepath"
	"reflect"
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

func TestReconcileTopologyValidationFailureDoesNotKeepPartialBindings(t *testing.T) {
	key := topologyEdgeKey("ir", "r2")
	existing := TopologyBinding{Key: key, IRNode: "ir", RouteID: "r2", EXNode: "other"}
	store := &Store{st: state{
		IRPool: map[string]IRPoolMember{
			"ir": {NodeID: "ir", Enabled: true},
		},
		EXRoutes: map[string]ExplicitEXRoute{
			"r1": {ID: "r1", EXNode: "ex", Enabled: true},
			"r2": {ID: "r2", EXNode: "ex", Enabled: true},
		},
		TopologyBindings: map[string]TopologyBinding{key: existing},
	}}
	before := cloneTopologyBindings(store.st.TopologyBindings)

	if _, err := store.ReconcileTopology(time.Now().UTC()); err == nil {
		t.Fatal("conflicting EX binding unexpectedly accepted")
	}
	if !reflect.DeepEqual(store.st.TopologyBindings, before) {
		t.Fatalf("validation failure left a partial binding: %+v", store.st.TopologyBindings)
	}
}
