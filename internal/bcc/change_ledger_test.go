package bcc

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestChangeLedgerDeterministicSecretFreeDiff(t *testing.T) {
	a := ConfigSnapshot{Generation: 1, Listeners: []string{"b:2", "a:1"}}
	b := ConfigSnapshot{Generation: 2, ConfigSHA256: strings.Repeat("a", 64), Listeners: []string{"a:1", "b:2"}, RouteID: "r1"}
	r := ChangeRecord{At: time.Unix(1, 0).UTC(), Actor: "bcc", NodeID: "n1", Before: a, After: b, Outcome: "APPLIED"}
	d1 := changeDigest(r)
	r.Before.Listeners = []string{"a:1", "b:2"}
	if d1 != changeDigest(r) {
		t.Fatal("digest changed with listener ordering")
	}
	raw, _ := json.Marshal(r)
	for _, secret := range []string{"password", "private_key", "payload"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("secret-bearing field %q in record", secret)
		}
	}
	diff := snapshotDiff(a, b)
	if len(diff) == 0 || diff[0].Field > diff[len(diff)-1].Field {
		t.Fatal("diff not deterministic")
	}
}
func TestChangeLedgerConcurrentAppendCollisionSafe(t *testing.T) {
	s := &Store{st: state{}}
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s.mu.Lock()
			s.appendChangeLocked(ChangeRecord{At: time.Unix(int64(i), 0), Actor: "test", NodeID: fmt.Sprintf("n%d", i), Outcome: "APPLIED"})
			s.mu.Unlock()
		}(i)
	}
	wg.Wait()
	if len(s.st.ChangeLedger) != 40 {
		t.Fatalf("len=%d", len(s.st.ChangeLedger))
	}
	seen := map[string]bool{}
	for _, r := range s.st.ChangeLedger {
		if seen[r.ID] {
			t.Fatal("duplicate id")
		}
		seen[r.ID] = true
	}
	if s.st.NextChangeSequence != 40 {
		t.Fatalf("sequence=%d", s.st.NextChangeSequence)
	}
}
func TestChangeLedgerPersistsAcrossRestart(t *testing.T) {
	p := t.TempDir() + "/state.json"
	s, err := OpenStore(p)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	want := s.appendChangeLocked(ChangeRecord{At: time.Unix(2, 0).UTC(), Actor: "bcc", NodeID: "n1", Outcome: "APPLIED", After: ConfigSnapshot{Generation: 2, ConfigSHA256: strings.Repeat("b", 64)}})
	if err := s.saveLocked(); err != nil {
		t.Fatal(err)
	}
	s.mu.Unlock()
	s2, err := OpenStore(p)
	if err != nil {
		t.Fatal(err)
	}
	got := s2.ListChangeRecords("n1", "")
	if len(got) != 1 || got[0].Digest != want.Digest || got[0].Sequence != want.Sequence {
		t.Fatalf("restart lost ledger: %+v", got)
	}
}
func TestChangeLedgerIncludedInBackupPayload(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	app, err := NewServer(store, "admin")
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	want := store.appendChangeLocked(ChangeRecord{At: time.Unix(3, 0).UTC(), Actor: "bcc", NodeID: "n-backup", Outcome: "APPLIED", After: ConfigSnapshot{Generation: 4, UnitSHA256: strings.Repeat("c", 64)}})
	if err := store.saveLocked(); err != nil {
		t.Fatal(err)
	}
	store.mu.Unlock()
	blob, _, err := app.CreateBackupBytes(backupTestKey(), time.Unix(4, 0))
	if err != nil {
		t.Fatal(err)
	}
	payload, _, err := decodeBackup(blob, backupTestKey())
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.State.ChangeLedger) != 1 || payload.State.ChangeLedger[0].Digest != want.Digest {
		t.Fatalf("backup lost ledger: %+v", payload.State.ChangeLedger)
	}
}
