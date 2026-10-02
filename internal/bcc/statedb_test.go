package bcc

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/telemetry"
)

func populatedState(t *testing.T, path string) *Store {
	t.Helper()
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ex-1", "ex-2"} {
		if _, err := store.UpsertNode(Node{ID: id, Alias: "EX " + id, Address: "127.0.0.1:1", Role: "foreign"}, "tok-"+id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.UpsertNode(Node{ID: "w-1", Alias: "worker", Address: "127.0.0.1:2", Role: "worker", PublicKey: "pk"}, "tok-w"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDeployJobs([]string{"ex-1", "ex-2"}, "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetFinancePolicyAt("ex-1", 1000, 3000, "IRR", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddTraffic("ex-1", "tok-ex-1", 3<<30, 1<<30); err != nil {
		t.Fatal(err)
	}
	if err := store.SetActiveAlerts(map[string]Alert{"stale/ex-2": {Type: "telemetry_stale", Status: "firing", NodeID: "ex-2"}}); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.st.History["ex-1"] = []HistoryPoint{{Timestamp: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), IngressBytes: 7, Routes: []telemetry.RouteSnapshot{{RouteID: "r"}}}}
	store.st.RetiredBootIDs["ex-1"] = map[string]bool{"boot-a": true}
	store.st.NextTelemetryIngestID = 42
	err = store.saveLocked()
	store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestStateIsASQLiteDatabaseAndRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := populatedState(t, path)
	raw, err := os.ReadFile(path)
	if err != nil || !isSQLiteFile(raw) {
		t.Fatalf("state file is not SQLite: %v", err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode %o", st.Mode().Perm())
	}
	if v, err := stateSchemaVersion(path); err != nil || v != len(stateMigrations) {
		t.Fatalf("schema version %d, %v", v, err)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonicalStateJSON(store.st), canonicalStateJSON(reopened.st)) {
		t.Fatalf("state changed across reopen\nwant %s\ngot  %s", canonicalStateJSON(store.st), canonicalStateJSON(reopened.st))
	}
	if reopened.st.NextTelemetryIngestID != 42 || len(reopened.st.FinanceLedger) == 0 || !reopened.st.RetiredBootIDs["ex-1"]["boot-a"] {
		t.Fatal("counters, ledger or retired boot IDs were lost")
	}
}

func TestJSONStateIsMigratedInPlace(t *testing.T) {
	dir := t.TempDir()
	src := populatedState(t, filepath.Join(dir, "src.json"))
	legacy, err := json.MarshalIndent(src.st, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !isSQLiteFile(raw) {
		t.Fatal("JSON state was not converted")
	}
	bak, err := os.ReadFile(path + ".json.bak")
	if err != nil || !bytes.Equal(bak, legacy) {
		t.Fatalf("original JSON not kept: %v", err)
	}
	if !bytes.Equal(canonicalStateJSON(src.st), canonicalStateJSON(store.st)) {
		t.Fatal("migrated state differs from the JSON state")
	}
	if _, err := os.Stat(path + ".sqlite-migrating"); !os.IsNotExist(err) {
		t.Fatal("migration left its temporary database")
	}
	// Agents keep authenticating with their existing tokens.
	if _, err := store.PullJobs("ex-1", "tok-ex-1"); err != nil {
		t.Fatalf("agent token lost in migration: %v", err)
	}
}

func TestLegacyJSONWithoutRateHistoryIsUpgraded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := `{"nodes":{},"jobs":{},"finance_policies":{"ex-1":{"node_id":"ex-1","cost_micros_per_gib":5,"revenue_micros_per_gib":9}},"next_job":3}`
	os.WriteFile(path, []byte(legacy), 0o600)
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	h := store.st.RateHistory["ex-1"]
	if len(h) != 1 || h[0].Currency != "IRR" || h[0].Version == 0 || store.st.NextJob != 3 {
		t.Fatalf("legacy upgrade lost data: %+v next_job=%d", h, store.st.NextJob)
	}
}

func TestNewerSchemaIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	populatedState(t, path)
	db, err := openStateDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, 'future')`, len(stateMigrations)+1); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := OpenStore(path); err == nil || !strings.Contains(err.Error(), "newer than this build") {
		t.Fatalf("newer schema accepted: %v", err)
	}
}

func TestCorruptStateDatabaseIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	populatedState(t, path)
	raw, _ := os.ReadFile(path)
	for i := 100; i < len(raw); i++ {
		raw[i] = 0xff
	}
	os.WriteFile(path, raw, 0o600)
	if _, err := OpenStore(path); err == nil {
		t.Fatal("corrupt database accepted")
	}
}

func TestLedgerAppendsAndRewritesStayExact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	var st state
	normalizeState(&st)
	entry := func(i int) FinanceLedgerEntry {
		return FinanceLedgerEntry{NodeID: "ex-1", Timestamp: time.Unix(int64(i), 0).UTC(), IngressBytes: uint64(i), Currency: "IRR"}
	}
	check := func(what string) {
		t.Helper()
		got, err := readStateDB(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(canonicalStateJSON(got), canonicalStateJSON(st)) {
			t.Fatalf("%s: stored ledger %d entries, want %d", what, len(got.FinanceLedger), len(st.FinanceLedger))
		}
	}
	for i := 0; i < 5; i++ {
		st.FinanceLedger = append(st.FinanceLedger, entry(i))
		if err := writeStateDB(path, st); err != nil {
			t.Fatal(err)
		}
	}
	check("appends")
	// Same length, different content: must not be mistaken for a prefix.
	st.FinanceLedger[4] = entry(99)
	writeStateDB(path, st)
	check("replaced last entry")
	// Shorter ledger (as after a restore): full rewrite.
	st.FinanceLedger = st.FinanceLedger[:2]
	writeStateDB(path, st)
	check("shrunk")
	st.FinanceLedger = nil
	writeStateDB(path, st)
	check("emptied")
}
