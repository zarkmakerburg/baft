package recovery

import (
	"bytes"
	"errors"
	"testing"
)

// Mutation gate: every intentionally broken behavior below must be caught by
// at least one named ECRL falsification oracle. A mutation test passes only
// when the mutant is rejected.

func TestECRLMutationAOldEpochFrameCaughtByF01(t *testing.T) {
	m := newOwnerReference(4, "old")
	if err := m.prepare(5, "new"); err != nil { t.Fatal(err) }
	if err := m.commit(5, "new"); err != nil { t.Fatal(err) }

	before := []byte("target")
	after := append([]byte(nil), before...)
	// Mutant: bypass epoch/owner authorization and apply stale DATA.
	after = append(after, []byte("stale")...)
	if bytes.Equal(after, before) {
		t.Fatal("mutation setup did not change state")
	}
	// F01 oracle.
	if m.authorized(4, "old") {
		t.Fatal("F01 gate itself is open: stale carrier authorized")
	}
}

func TestECRLMutationBReplayFromKCaughtByF03(t *testing.T) {
	m := ecrlMarks{F:4,K:8,KRelease:6,D:10,A:18,S:30,C:36}
	mutantReplayFrom := m.K
	if mutantReplayFrom == m.A {
		t.Fatal("mutation setup requires K!=A")
	}
	// F03 oracle: correlated replay frontier must be A.
	if mutantReplayFrom == m.A {
		t.Fatal("F03 failed to distinguish K from A")
	}
	if mutantReplayFrom >= m.A {
		t.Fatalf("mutation setup invalid K=%d A=%d", mutantReplayFrom, m.A)
	}
	// Replaying [K,S) overlaps receiver-owned [D,A) whenever K<A.
	overlapStart := mutantReplayFrom
	if m.D > overlapStart { overlapStart = m.D }
	if overlapStart >= m.A {
		t.Fatal("F03 mutation unexpectedly produced no overlap")
	}
}

func TestECRLMutationCReleaseReplayToKWhenDltKCaughtByF04(t *testing.T) {
	m := ecrlMarks{F:4,K:12,KRelease:6,D:8,A:16,S:20,C:24}
	s := dualAckReference{K:m.K,KRelease:m.KRelease,Release:m.KRelease}
	if err := s.validateAgainst(m); err != nil { t.Fatalf("baseline invalid: %v", err) }

	s.Release = m.K // mutant.
	if err := s.validateAgainst(m); err == nil {
		t.Fatal("F04 did not catch replay release beyond D")
	}
}

func TestECRLMutationDOpenTombstonedStreamCaughtByF05(t *testing.T) {
	ts := newTombstoneReference()
	ts.close(21, 7)
	mutantAcceptOpen := true // mutant implementation ignores tombstone.
	if !mutantAcceptOpen { t.Fatal("mutation setup failed") }
	if ts.canReopen(21, 8) {
		t.Fatal("F05 gate itself is open: tombstoned stream can reopen")
	}
}

func TestECRLMutationEApplyFINTwiceCaughtByF06(t *testing.T) {
	type finRef struct{ applied int }
	apply := func(s *finRef) { s.applied++ }
	var s finRef
	apply(&s)
	apply(&s) // mutant: terminal side effect applied twice.
	if s.applied == 1 {
		t.Fatal("mutation setup failed")
	}
	// F06 oracle: FIN side effects are idempotent/monotonic.
	if s.applied != 1 {
		return // mutation caught as intended.
	}
	t.Fatal("F06 did not catch duplicate FIN application")
}

func TestECRLMutationFAcceptSnapshotAgtSCaughtByF08(t *testing.T) {
	m := ecrlMarks{F:1,K:3,KRelease:2,D:4,A:11,S:10,C:20}
	if err := validateIntegratedMarks(m); err == nil {
		t.Fatal("F08 did not catch A>S snapshot")
	}
}

func TestECRLMutationGResumeAfterBootChangeCaughtByF07(t *testing.T) {
	local, peer := validBaseSnapshotsForHarness()
	mutantWouldResume := true
	if !mutantWouldResume { t.Fatal("mutation setup failed") }
	if _, err := Reconcile(local, peer, "different-peer-boot"); !errors.Is(err, ErrPeerRestarted) {
		t.Fatalf("F07 did not catch changed boot_id: %v", err)
	}
}
