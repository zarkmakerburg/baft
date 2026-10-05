package bcc

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func readyRotationStore(t *testing.T) (*Store, string) {
	t.Helper()
	s := tunnelStore(t)
	tn := newTunnel(t, s, time.Now().UTC())
	s.mu.Lock()
	live := s.st.Tunnels[tn.ID]
	live.Phase = TunnelActive
	live.JobID = ""
	live.UpdatedAt = time.Now().UTC()
	s.st.Tunnels[tn.ID] = live
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	s.mu.Unlock()
	return s, tn.ID
}

func breakAuditAppend(t *testing.T, a *AuditLog) {
	t.Helper()
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.path = filepath.Join(blocker, "audit.jsonl")
}

func TestCertRotationStartCommitsDurableAuditIntentBeforeDelivery(t *testing.T) {
	store, tunnelID := readyRotationStore(t)
	statePath := store.Path()
	app, err := NewServer(store, "admin")
	if err != nil {
		t.Fatal(err)
	}
	breakAuditAppend(t, app.audit)

	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, authReq(http.MethodPost, "/api/tunnels/rotate-cert", "admin", map[string]any{
		"tunnel_id": tunnelID,
	}))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("start status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-BAFT-Audit-State"); got != "pending" {
		t.Fatalf("audit state header=%q, want pending", got)
	}
	pending := store.PendingSecurityAuditIntents()
	if len(pending) != 1 || pending[0].Action != "cert.rotation.start" {
		t.Fatalf("pending intents=%+v", pending)
	}
	if len(store.ListCertRotations()) != 1 {
		t.Fatal("rotation mutation was not committed")
	}

	reopened, err := OpenStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.PendingSecurityAuditIntents(); len(got) != 1 {
		t.Fatalf("pending intent did not survive restart: %+v", got)
	}
	restarted, err := NewServer(reopened, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.PendingSecurityAuditIntents(); len(got) != 0 {
		t.Fatalf("restart did not drain intent: %+v", got)
	}
	entries, err := restarted.audit.List(0)
	if err != nil {
		t.Fatal(err)
	}
	var starts int
	for _, e := range entries {
		if e.Action == "cert.rotation.start" {
			starts++
			if e.IntentID == "" {
				t.Fatal("delivered security audit has no intent id")
			}
		}
	}
	if starts != 1 {
		t.Fatalf("start audit count=%d entries=%+v", starts, entries)
	}
	if err := restarted.audit.Verify(); err != nil {
		t.Fatalf("audit chain verify: %v", err)
	}

	againStore, err := OpenStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	again, err := NewServer(againStore, "admin")
	if err != nil {
		t.Fatal(err)
	}
	entries, _ = again.audit.List(0)
	starts = 0
	for _, e := range entries {
		if e.Action == "cert.rotation.start" {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("second restart duplicated start audit: %d", starts)
	}
}

func TestCertRotationCancelCommitsAuditIntentAndReturnsTrueState(t *testing.T) {
	store, tunnelID := readyRotationStore(t)
	statePath := store.Path()
	app, err := NewServer(store, "admin")
	if err != nil {
		t.Fatal(err)
	}

	start := httptest.NewRecorder()
	app.Handler().ServeHTTP(start, authReq(http.MethodPost, "/api/tunnels/rotate-cert", "admin", map[string]any{
		"tunnel_id": tunnelID,
	}))
	if start.Code != http.StatusAccepted {
		t.Fatalf("start=%d body=%s", start.Code, start.Body.String())
	}
	rots := store.ListCertRotations()
	if len(rots) != 1 {
		t.Fatalf("rotations=%+v", rots)
	}

	breakAuditAppend(t, app.audit)
	cancel := httptest.NewRecorder()
	app.Handler().ServeHTTP(cancel, authReq(http.MethodPost, "/api/cert-rotations/cancel", "admin", map[string]any{
		"id": rots[0].ID, "reason": "operator requested",
	}))
	if cancel.Code != http.StatusOK {
		t.Fatalf("cancel status=%d body=%s", cancel.Code, cancel.Body.String())
	}
	if got := cancel.Header().Get("X-BAFT-Audit-State"); got != "pending" {
		t.Fatalf("cancel audit state=%q, want pending", got)
	}
	got, ok := store.GetCertRotation(rots[0].ID)
	if !ok || got.Phase != CertRotRolledBack {
		t.Fatalf("cancel response/state is not committed truth: %+v", got)
	}
	pending := store.PendingSecurityAuditIntents()
	if len(pending) != 1 || pending[0].Action != "cert.rotation.cancel" {
		t.Fatalf("pending cancel intent=%+v", pending)
	}

	reopened, err := OpenStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewServer(reopened, "admin")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := restarted.audit.List(0)
	if err != nil {
		t.Fatal(err)
	}
	var starts, cancels int
	for _, e := range entries {
		switch e.Action {
		case "cert.rotation.start":
			starts++
		case "cert.rotation.cancel":
			cancels++
		}
	}
	if starts != 1 || cancels != 1 {
		t.Fatalf("audit counts start=%d cancel=%d entries=%+v", starts, cancels, entries)
	}
	if err := restarted.audit.Verify(); err != nil {
		t.Fatalf("audit chain verify: %v", err)
	}
}

func TestSecurityAuditRestartDoesNotDuplicateAppendBeforeAckCrashWindow(t *testing.T) {
	store, _ := readyRotationStore(t)
	now := time.Now().UTC().Round(0)
	store.mu.Lock()
	intent, err := store.enqueueSecurityAuditLocked(AuditEntry{
		Timestamp: now, Actor: "bcc", Action: "cert.rotation.complete", Target: "rot-crash",
		Outcome: "success", Details: map[string]any{"detail": "committed"},
	})
	if err == nil {
		err = store.saveLocked()
	}
	store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	audit, err := OpenAuditLog(store.Path() + ".audit.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := audit.Append(intent); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after append but before the outbox ACK reaches SQLite.
	reopened, err := OpenStore(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.PendingSecurityAuditIntents()) != 1 {
		t.Fatal("crash-window intent is not pending")
	}
	restarted, err := NewServer(reopened, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.PendingSecurityAuditIntents()) != 0 {
		t.Fatal("existing delivered intent was not ACKed on restart")
	}
	entries, err := restarted.audit.List(0)
	if err != nil {
		t.Fatal(err)
	}
	var matches int
	for _, e := range entries {
		if e.IntentID == intent.IntentID {
			matches++
		}
	}
	if matches != 1 {
		t.Fatalf("intent delivered %d times, want exactly once", matches)
	}
	if err := restarted.audit.Verify(); err != nil {
		t.Fatalf("audit chain verify: %v", err)
	}
}

func TestConcurrentSecurityAuditFlushIsExactlyOnce(t *testing.T) {
	store, _ := readyRotationStore(t)
	app, err := NewServer(store, "admin")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Round(0)
	store.mu.Lock()
	intent, err := store.enqueueSecurityAuditLocked(AuditEntry{
		Timestamp: now, Actor: "bcc", Action: "cert.rotation.complete", Target: "rot-concurrent",
		Outcome: "success", Details: map[string]any{"detail": "concurrent drain"},
	})
	if err == nil {
		err = store.saveLocked()
	}
	store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	const workers = 64
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			<-start
			errs <- app.FlushSecurityAuditIntents()
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent flush: %v", err)
		}
	}
	if got := store.PendingSecurityAuditIntents(); len(got) != 0 {
		t.Fatalf("pending after concurrent flush: %+v", got)
	}
	entries, err := app.audit.List(0)
	if err != nil {
		t.Fatal(err)
	}
	var matches int
	for _, e := range entries {
		if e.IntentID == intent.IntentID {
			matches++
		}
	}
	if matches != 1 {
		t.Fatalf("intent delivered %d times under concurrent flush, want exactly once", matches)
	}
	if err := app.audit.Verify(); err != nil {
		t.Fatalf("audit chain verify: %v", err)
	}
}
