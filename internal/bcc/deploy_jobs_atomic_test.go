package bcc

import (
    "os"
    "path/filepath"
    "testing"
)

func TestCreateDeployJobsInvalidBatchAndSaveFailureAreAtomic(t *testing.T) {
    dir := t.TempDir()
    statePath := filepath.Join(dir, "state.db")
    store, err := OpenStore(statePath)
    if err != nil { t.Fatal(err) }
    if _, err := store.UpsertNode(Node{ID:"n1", Alias:"One", Address:"127.0.0.1:21001", Role:"foreign"}, ""); err != nil { t.Fatal(err) }
    start := store.st.NextJob

    if _, err := store.CreateDeployJobs([]string{"n1", "missing"}, "v1.0.0"); err == nil {
        t.Fatal("unknown node should reject the whole batch")
    }
    if got := store.ListJobs(); len(got) != 0 || store.st.NextJob != start {
        t.Fatalf("rejected batch changed RAM: jobs=%d next=%d", len(got), store.st.NextJob)
    }

    blocked := filepath.Join(dir, "state-directory")
    if err := os.Mkdir(blocked, 0700); err != nil { t.Fatal(err) }
    store.path = blocked
    if _, err := store.CreateDeployJobs([]string{"n1"}, "v1.0.0"); err == nil {
        t.Fatal("injected state write failure unexpectedly succeeded")
    }
    if got := store.ListJobs(); len(got) != 0 || store.st.NextJob != start {
        t.Fatalf("failed save changed RAM: jobs=%d next=%d", len(got), store.st.NextJob)
    }
    store.path = statePath
    reopened, err := OpenStore(statePath)
    if err != nil { t.Fatal(err) }
    if got := reopened.ListJobs(); len(got) != 0 || reopened.st.NextJob != start {
        t.Fatalf("failed batch/save changed disk: jobs=%d next=%d", len(got), reopened.st.NextJob)
    }
}
