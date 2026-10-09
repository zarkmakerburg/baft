package bcc

import (
    "os"
    "path/filepath"
    "testing"
)

func TestJobTransitionsDoNotSurviveFailedSaveInMemory(t *testing.T) {
    dir := t.TempDir()
    dbPath := filepath.Join(dir, "state.db")
    store, err := OpenStore(dbPath)
    if err != nil { t.Fatal(err) }
    if _, err := store.UpsertNode(Node{ID:"node",Address:"127.0.0.1:22000",Role:"foreign"}, "agent-secret"); err != nil { t.Fatal(err) }
    jobs, err := store.CreateDeployJobs([]string{"node"}, "v1.0.0")
    if err != nil { t.Fatal(err) }
    jobID := jobs[0].ID
    blocked := filepath.Join(dir, "state-directory")
    if err := os.Mkdir(blocked, 0700); err != nil { t.Fatal(err) }
    store.path = blocked
    if _, err := store.PullJobs("node", "agent-secret"); err == nil { t.Fatal("pull must fail when dispatch cannot persist") }
    if got := store.st.Jobs[jobID].Status; got != "queued" { t.Fatalf("failed pull dispatched in memory: %s",got) }

    store.path = dbPath
    dispatched, err := store.PullJobs("node", "agent-secret")
    if err != nil || len(dispatched) != 1 || dispatched[0].ID != jobID {
        t.Fatalf("retry did not deliver job: %+v %v",dispatched,err)
    }
    store.path = blocked
    if err := store.AckJobOutput("node", "agent-secret", jobID, "succeeded", "done", "private-output"); err == nil {
        t.Fatal("ack must fail when completion cannot persist")
    }
    if got := store.st.Jobs[jobID]; got.Status != "dispatched" || got.Output != "" {
        t.Fatalf("failed ack completed or exposed output in memory: %+v",got)
    }
    store.path = dbPath
    reopened, err := OpenStore(dbPath)
    if err != nil { t.Fatal(err) }
    if got := reopened.st.Jobs[jobID]; got.Status != "dispatched" || got.Output != "" {
        t.Fatalf("failed ack reached disk: %+v",got)
    }
}
