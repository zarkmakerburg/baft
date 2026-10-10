package bcc

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestHTTPAPIFailsClosedAfterUncertainStateCommit(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(store, "admin-secret")
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	request := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Authorization", "Bearer admin-secret")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if got := request("/api/nodes").Code; got != http.StatusOK {
		t.Fatalf("healthy node API status = %d", got)
	}
	store.mu.Lock()
	_ = store.recordStateWrite(errStateCommitUncertain)
	store.mu.Unlock()
	if !store.PersistenceUncertain() {
		t.Fatal("uncertain commit did not mark the Store")
	}
	for _, path := range []string{"/api/nodes", "/api/agent/jobs", "/api/backups"} {
		if got := request(path).Code; got != http.StatusServiceUnavailable {
			t.Errorf("%s exposed stale state: status %d", path, got)
		}
	}
	if got := request("/").Code; got == http.StatusServiceUnavailable {
		t.Fatalf("static landing page status = %d", got)
	}
	if err := store.saveLocked(); !errors.Is(err, errStateCommitUncertain) {
		t.Fatalf("store accepted write after uncertain commit: %v", err)
	}
}
