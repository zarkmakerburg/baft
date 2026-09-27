package recovery

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"math/rand"
	"testing"
)

// This file is a deterministic falsification harness for the ECRL design.
// It is intentionally a test-only reference model. Passing these tests does
// not mean Stage D is implemented; it only means the current formal model
// survives the listed attacks/failures.

type ecrlMarks struct {
	F        uint64
	K        uint64
	KRelease uint64
	D        uint64
	A        uint64
	S        uint64
	C        uint64
}

func validateIntegratedMarks(m ecrlMarks) error {
	if m.F > m.D {
		return errors.New("F>D")
	}
	if m.KRelease > m.D {
		return errors.New("K_release>D")
	}
	if m.KRelease > m.K {
		return errors.New("K_release>K")
	}
	if m.D > m.A {
		return errors.New("D>A")
	}
	if m.K > m.A {
		return errors.New("K>A")
	}
	if m.A > m.S {
		return errors.New("A>S")
	}
	if m.S > m.C {
		return errors.New("S>C")
	}
	return nil
}

type dualAckReference struct {
	K        uint64
	KRelease uint64
	Release  uint64
}

func (s *dualAckReference) observe(accepted, delivered uint64) error {
	if delivered > accepted {
		return errors.New("delivery watermark exceeds acceptance watermark")
	}
	if accepted > s.K {
		s.K = accepted
	}
	if delivered > s.KRelease {
		s.KRelease = delivered
	}
	// The only safe normal-operation replay release frontier is the
	// sender-observed delivery watermark, never the acceptance watermark.
	s.Release = s.KRelease
	return nil
}

func (s dualAckReference) validateAgainst(m ecrlMarks) error {
	if s.K > m.A {
		return errors.New("observed acceptance ACK exceeds peer A")
	}
	if s.KRelease > m.D {
		return errors.New("replay-release knowledge exceeds peer D")
	}
	if s.Release > s.KRelease || s.Release > m.D {
		return errors.New("replay freed beyond delivered watermark")
	}
	return nil
}

type ownerReference struct {
	lease *EpochLease
	owner map[uint64]string
}

func newOwnerReference(epoch uint64, owner string) *ownerReference {
	l, _ := NewEpochLease(epoch)
	return &ownerReference{lease: l, owner: map[uint64]string{epoch: owner}}
}

func (m *ownerReference) prepare(next uint64, candidate string) error {
	return m.lease.Prepare(next, candidate)
}

func (m *ownerReference) commit(next uint64, candidate string) error {
	if err := m.lease.Commit(next, candidate); err != nil {
		return err
	}
	m.owner[next] = candidate
	return nil
}

func (m *ownerReference) authorized(epoch uint64, carrier string) bool {
	if !m.lease.Authorize(epoch) {
		return false
	}
	return m.owner[epoch] == carrier
}

type tombstoneReference struct {
	closedAt map[uint64]uint64
}

func newTombstoneReference() *tombstoneReference {
	return &tombstoneReference{closedAt: map[uint64]uint64{}}
}

func (t *tombstoneReference) close(streamID, epoch uint64) {
	t.closedAt[streamID] = epoch
}

func (t *tombstoneReference) canReopen(streamID, epoch uint64) bool {
	closedAt, ok := t.closedAt[streamID]
	if !ok {
		return true
	}
	return epoch < closedAt
}

func partitionHandoff(source []byte, m ecrlMarks) (prefix, ring, replay, combined []byte, err error) {
	if err = validateIntegratedMarks(m); err != nil {
		return nil, nil, nil, nil, err
	}
	if m.S > uint64(len(source)) {
		return nil, nil, nil, nil, errors.New("S exceeds source")
	}
	prefix = append([]byte(nil), source[:m.D]...)
	ring = append([]byte(nil), source[m.D:m.A]...)
	replay = append([]byte(nil), source[m.A:m.S]...)
	combined = append(combined, prefix...)
	combined = append(combined, ring...)
	combined = append(combined, replay...)
	return
}

func validBaseSnapshotsForHarness() (Snapshot, Snapshot) {
	local := Snapshot{SessionID: "s", BootID: "local-boot", Epoch: 9, Flows: []FlowSnapshot{{
		StreamID: 1, OpenNonce: "n",
		TxNext: 100, TxAcked: 40,
		RxAccepted: 80, RxDelivered: 64, RxCredit: 128,
	}}}
	peer := Snapshot{SessionID: "s", BootID: "peer-boot", Epoch: 9, Flows: []FlowSnapshot{{
		StreamID: 1, OpenNonce: "n",
		TxNext: 120, TxAcked: 64,
		RxAccepted: 72, RxDelivered: 70, RxCredit: 134,
	}}}
	return local, peer
}

func TestECRLF01ZombieCarrier(t *testing.T) {
	m := newOwnerReference(7, "carrier-old")
	if err := m.prepare(8, "carrier-new"); err != nil { t.Fatal(err) }
	if err := m.commit(8, "carrier-new"); err != nil { t.Fatal(err) }

	target := []byte("prefix")
	before := append([]byte(nil), target...)
	if m.authorized(7, "carrier-old") {
		target = append(target, []byte("ZOMBIE")...)
	}
	if !bytes.Equal(target, before) {
		t.Fatal("F01: zombie carrier changed target bytes after commit")
	}
	if !m.authorized(8, "carrier-new") {
		t.Fatal("F01: committed carrier was not authorized")
	}
}

func TestECRLF02DualCandidate(t *testing.T) {
	m := newOwnerReference(1, "carrier-a")
	if err := m.prepare(2, "candidate-a"); err != nil { t.Fatal(err) }
	if err := m.prepare(2, "candidate-b"); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("F02: second candidate was not rejected: %v", err)
	}
	if err := m.commit(2, "candidate-a"); err != nil { t.Fatal(err) }
	if !m.authorized(2, "candidate-a") {
		t.Fatal("F02: winner lost ownership")
	}
	if m.authorized(2, "candidate-b") {
		t.Fatal("F02: losing candidate obtained effective ownership")
	}
}

func TestECRLF03LostACK(t *testing.T) {
	local, peer := validBaseSnapshotsForHarness()
	// local K=40, but peer authoritative A=72.
	plan, err := Reconcile(local, peer, "peer-boot")
	if err != nil { t.Fatal(err) }
	if got := plan.Flows[0].LocalReplayFrom; got != 72 {
		t.Fatalf("F03: replay frontier=%d want peer A=72", got)
	}
	if got := plan.Flows[0].LocalAckAdvanceTo; got != 72 {
		t.Fatalf("F03: recovered ACK=%d want 72", got)
	}
}

func TestECRLF04AcceptedNotDeliveredPartition(t *testing.T) {
	source := []byte("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	m := ecrlMarks{F: 4, K: 8, KRelease: 6, D: 10, A: 18, S: 30, C: 36}
	prefix, ring, replay, combined, err := partitionHandoff(source, m)
	if err != nil { t.Fatal(err) }
	if len(prefix) != 10 || len(ring) != 8 || len(replay) != 12 {
		t.Fatalf("F04: wrong partition sizes prefix=%d ring=%d replay=%d", len(prefix), len(ring), len(replay))
	}
	if !bytes.Equal(combined, source[:m.S]) {
		t.Fatalf("F04: handoff produced gap/overlap: got=%q want=%q", combined, source[:m.S])
	}
}

func TestECRLF04DltKReplayReleaseSafety(t *testing.T) {
	m := ecrlMarks{F:4, K:12, KRelease:6, D:8, A:16, S:20, C:24}
	if err := validateIntegratedMarks(m); err != nil {
		t.Fatalf("F04: baseline D<K state must be valid: %v", err)
	}
	ack := dualAckReference{}
	if err := ack.observe(m.K, m.KRelease); err != nil {
		t.Fatal(err)
	}
	if err := ack.validateAgainst(m); err != nil {
		t.Fatalf("F04: dual-level ACK baseline was unsafe: %v", err)
	}

	mutated := ack
	mutated.Release = m.K // mutation: free replay through acceptance K while D<K.
	if err := mutated.validateAgainst(m); err == nil {
		t.Fatal("F04: releasing replay through K while D<K was not detected")
	}
}

func TestECRLF05TombstoneResurrection(t *testing.T) {
	ts := newTombstoneReference()
	ts.close(11, 9)
	for _, epoch := range []uint64{9, 10, 11, 100} {
		if ts.canReopen(11, epoch) {
			t.Fatalf("F05: tombstoned stream reopened at epoch %d", epoch)
		}
	}
	if !ts.canReopen(13, 10) {
		t.Fatal("F05: unrelated stream was incorrectly blocked")
	}
}

func TestECRLF06LostFIN(t *testing.T) {
	local, peer := validBaseSnapshotsForHarness()
	local.Flows[0].FinSent = true
	peer.Flows[0].FinRecv = false // FIN was lost with the old Carrier.

	// Reference rule: a sent FIN that the peer has not observed is a
	// terminal-control replay obligation, not permission to advance state.
	replayFIN := local.Flows[0].FinSent && !peer.Flows[0].FinRecv
	if !replayFIN {
		t.Fatal("F06: lost FIN was not identified as a replay obligation")
	}
	if peer.Flows[0].FinAckSent {
		t.Fatal("F06: peer cannot acknowledge a FIN it never received")
	}
}

func TestECRLF06LostFINACK(t *testing.T) {
	local, peer := validBaseSnapshotsForHarness()
	local.Flows[0].FinSent = true
	peer.Flows[0].FinRecv = true
	peer.Flows[0].FinAckSent = true
	plan, err := Reconcile(local, peer, "peer-boot")
	if err != nil { t.Fatal(err) }
	if !plan.Flows[0].LocalFinAckCanAdvance {
		t.Fatal("F06: correlated state could not recover lost FIN_ACK")
	}
}

func TestECRLF07PeerRestart(t *testing.T) {
	local, peer := validBaseSnapshotsForHarness()
	if _, err := Reconcile(local, peer, "different-peer-boot"); !errors.Is(err, ErrPeerRestarted) {
		t.Fatalf("F07: peer restart was not rejected: %v", err)
	}
}

func TestECRLF08InconsistentSnapshot(t *testing.T) {
	invalid := []ecrlMarks{
		{F: 0, K: 11, KRelease: 0, D: 2, A: 10, S: 12, C: 20}, // K>A
		{F: 0, K: 2, KRelease: 0, D: 11, A: 10, S: 12, C: 20}, // D>A
		{F: 0, K: 2, KRelease: 0, D: 3, A: 13, S: 12, C: 20}, // A>S
		{F: 0, K: 2, KRelease: 0, D: 3, A: 10, S: 21, C: 20}, // S>C
	}
	for i, m := range invalid {
		if err := validateIntegratedMarks(m); err == nil {
			t.Fatalf("F08: invalid watermark set %d was accepted: %#v", i, m)
		}
	}

	local, peer := validBaseSnapshotsForHarness()
	peer.Flows[0].RxAccepted = local.Flows[0].TxNext + 1
	if _, err := Reconcile(local, peer, "peer-boot"); !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("F08: A>S contradiction was not rejected by reconciliation: %v", err)
	}
}

func TestECRLF09DeterministicPropertySweep(t *testing.T) {
	for k := uint64(0); k <= 5; k++ {
		for d := uint64(0); d <= 5; d++ {
			for a := uint64(0); a <= 5; a++ {
				for s := uint64(0); s <= 5; s++ {
					for c := uint64(0); c <= 5; c++ {
						for f := uint64(0); f <= d; f++ {
							for kr := uint64(0); kr <= d; kr++ {
								m := ecrlMarks{F:f,K:k,KRelease:kr,D:d,A:a,S:s,C:c}
								wantValid := f <= d && kr <= d && kr <= k && d <= a && k <= a && a <= s && s <= c
								err := validateIntegratedMarks(m)
								if (err == nil) != wantValid {
									t.Fatalf("F09: classifier mismatch for %#v err=%v wantValid=%v", m, err, wantValid)
								}
								if !wantValid {
									continue
								}
								source := []byte("abcdef")
								_, ring, replay, combined, err := partitionHandoff(source, m)
								if err != nil { t.Fatalf("F09: valid state rejected %#v: %v", m, err) }
								if uint64(len(ring)+len(replay)) != s-d {
									t.Fatalf("F09: conservation length failed %#v", m)
								}
								if !bytes.Equal(combined, source[:s]) {
									t.Fatalf("F09: conservation bytes failed %#v", m)
								}
							}
						}
					}
				}
			}
		}
	}
}

func TestECRLF10ExactByteStream(t *testing.T) {
	const n = 256 * 1024
	rng := rand.New(rand.NewSource(0xEC12))
	source := make([]byte, n)
	if _, err := rng.Read(source); err != nil { t.Fatal(err) }
	wantHash := sha256.Sum256(source)

	for i := 0; i < 512; i++ {
		s := uint64(1 + rng.Intn(n))
		a := uint64(rng.Intn(int(s + 1)))
		d := uint64(rng.Intn(int(a + 1)))
		k := uint64(rng.Intn(int(a + 1)))
		c := s + uint64(rng.Intn(4096))

		m := ecrlMarks{F:0,K:k,KRelease:0,D:d,A:a,S:s,C:c}
		prefix, ring, replay, combined, err := partitionHandoff(source, m)
		if err != nil { t.Fatalf("F10 case %d: %v", i, err) }
		if !bytes.Equal(combined, source[:s]) {
			t.Fatalf("F10 case %d: duplicate or missing byte in reconstructed prefix", i)
		}
		if len(prefix)+len(ring)+len(replay) != int(s) {
			t.Fatalf("F10 case %d: byte-count mismatch", i)
		}
	}

	// Full-stream terminal case: exact byte count and hash must match.
	full := ecrlMarks{F:n/4,K:n/3,KRelease:n/3,D:n/2,A:3*n/4,S:n,C:n}
	_, _, _, got, err := partitionHandoff(source, full)
	if err != nil { t.Fatal(err) }
	gotHash := sha256.Sum256(got)
	if len(got) != len(source) || gotHash != wantHash {
		t.Fatalf("F10: exact stream mismatch len=%d/%d hash=%x/%x", len(got), len(source), gotHash, wantHash)
	}
}
