package bcc

import (
    "os"
    "path/filepath"
    "reflect"
    "testing"
    "time"
)

func TestSetFinancePolicySaveFailurePreservesVersionAndHistory(t *testing.T) {
    dir := t.TempDir()
    dbPath := filepath.Join(dir, "state.db")
    store, err := OpenStore(dbPath)
    if err != nil { t.Fatal(err) }
    if _, err := store.UpsertNode(Node{ID: "node", Address: "127.0.0.1:21100", Role: "foreign"}, ""); err != nil { t.Fatal(err) }
    base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
    if err := store.SetFinancePolicyAt("node", 100, 200, "USD", base.Add(24*time.Hour)); err != nil { t.Fatal(err) }
    beforeHistory := append([]FinancePolicy(nil), store.st.RateHistory["node"]...)
    beforePolicy := store.st.Policies["node"]
    beforeVersion := store.st.NextRateVersion

    blocked := filepath.Join(dir, "state-directory")
    if err := os.Mkdir(blocked, 0700); err != nil { t.Fatal(err) }
    store.path = blocked
    // Earlier effective time exercises sorting as well as failed persistence.
    if err := store.SetFinancePolicyAt("node", 300, 400, "USD", base); err == nil {
        t.Fatal("injected state write failure unexpectedly succeeded")
    }
    if store.st.NextRateVersion != beforeVersion || store.st.Policies["node"] != beforePolicy ||
        !reflect.DeepEqual(store.st.RateHistory["node"], beforeHistory) {
        t.Fatalf("failed policy leaked into memory: version=%d history=%+v policy=%+v",
            store.st.NextRateVersion, store.st.RateHistory["node"], store.st.Policies["node"])
    }
    store.path = dbPath
    reopened, err := OpenStore(dbPath)
    if err != nil { t.Fatal(err) }
    if reopened.st.NextRateVersion != beforeVersion || reopened.st.Policies["node"] != beforePolicy ||
        !reflect.DeepEqual(reopened.st.RateHistory["node"], beforeHistory) {
        t.Fatalf("failed policy reached disk: version=%d history=%+v policy=%+v",
            reopened.st.NextRateVersion, reopened.st.RateHistory["node"], reopened.st.Policies["node"])
    }
    if err := store.SetFinancePolicyAt("node", 300, 400, "USD", base); err != nil { t.Fatal(err) }
    if got := store.st.Policies["node"].Version; got != beforeVersion {
        t.Fatalf("retry skipped rate version: got %d, want %d", got, beforeVersion)
    }
}
