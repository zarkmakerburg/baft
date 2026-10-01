package recovery

import (
	"errors"
	"fmt"
	"sort"
)

var (
	ErrStateMismatch = errors.New("STATE_MISMATCH")
	ErrPeerRestarted = errors.New("PEER_RESTARTED")
)

type FlowSnapshot struct {
	StreamID    uint64
	OpenNonce   string
	TxNext      uint64
	TxAcked     uint64
	RxAccepted  uint64
	RxDelivered uint64
	RxCredit    uint64
	FinSent     bool
	FinRecv     bool
	FinAcked    bool
	FinAckSent  bool
	FinAckConfirmed bool
}

type Snapshot struct {
	SessionID string
	BootID    string
	Epoch     uint64
	Flows     []FlowSnapshot
}

type FlowPlan struct {
	StreamID               uint64
	LocalReplayFrom        uint64
	PeerReplayFrom         uint64
	LocalAckAdvanceTo      uint64
	PeerAckAdvanceTo       uint64
	LocalReleaseThrough    uint64
	PeerReleaseThrough     uint64
	LocalFinAckCanAdvance  bool
	PeerFinAckCanAdvance   bool
	LocalFinAckConfirmCanAdvance bool
	PeerFinAckConfirmCanAdvance  bool
}

type Plan struct {
	SessionID string
	Epoch     uint64
	Flows     []FlowPlan
}

func (s Snapshot) Validate() error {
	if s.SessionID == "" || s.BootID == "" || s.Epoch == 0 {
		return fmt.Errorf("%w: invalid session identity", ErrStateMismatch)
	}
	seen := make(map[uint64]struct{}, len(s.Flows))
	for _, f := range s.Flows {
		if f.StreamID == 0 || f.OpenNonce == "" {
			return fmt.Errorf("%w: invalid flow identity", ErrStateMismatch)
		}
		if _, ok := seen[f.StreamID]; ok {
			return fmt.Errorf("%w: duplicate stream %d", ErrStateMismatch, f.StreamID)
		}
		seen[f.StreamID] = struct{}{}
		if f.TxAcked > f.TxNext {
			return fmt.Errorf("%w: tx_ack > tx_next on stream %d", ErrStateMismatch, f.StreamID)
		}
		if f.RxDelivered > f.RxAccepted || f.RxAccepted > f.RxCredit {
			return fmt.Errorf("%w: invalid receive watermarks on stream %d", ErrStateMismatch, f.StreamID)
		}
		if f.FinAcked && !f.FinSent {
			return fmt.Errorf("%w: FIN acknowledged before FIN sent on stream %d", ErrStateMismatch, f.StreamID)
		}
		if f.FinAckSent && !f.FinRecv {
			return fmt.Errorf("%w: FIN_ACK sent before FIN received on stream %d", ErrStateMismatch, f.StreamID)
		}
		if f.FinAckConfirmed && !f.FinAckSent {
			return fmt.Errorf("%w: FIN_ACK confirmed before FIN_ACK sent on stream %d", ErrStateMismatch, f.StreamID)
		}
	}
	return nil
}

// Reconcile correlates one endpoint's authoritative receive state with the
// other endpoint's send state. It never trusts an acknowledgement that would
// move beyond bytes the sender actually produced.
func Reconcile(local, peer Snapshot, expectedPeerBootID string) (Plan, error) {
	if err := local.Validate(); err != nil {
		return Plan{}, err
	}
	if err := peer.Validate(); err != nil {
		return Plan{}, err
	}
	if expectedPeerBootID == "" || peer.BootID != expectedPeerBootID {
		return Plan{}, ErrPeerRestarted
	}
	if local.SessionID != peer.SessionID || local.Epoch != peer.Epoch {
		return Plan{}, ErrStateMismatch
	}

	peerFlows := make(map[uint64]FlowSnapshot, len(peer.Flows))
	for _, f := range peer.Flows {
		peerFlows[f.StreamID] = f
	}
	if len(local.Flows) != len(peer.Flows) {
		return Plan{}, ErrStateMismatch
	}

	out := Plan{SessionID: local.SessionID, Epoch: local.Epoch, Flows: make([]FlowPlan, 0, len(local.Flows))}
	for _, lf := range local.Flows {
		pf, ok := peerFlows[lf.StreamID]
		if !ok || pf.OpenNonce != lf.OpenNonce {
			return Plan{}, fmt.Errorf("%w: flow identity differs for stream %d", ErrStateMismatch, lf.StreamID)
		}

		// Peer RxAccepted is authoritative evidence for how much of our Tx it
		// admitted to bounded memory. It can recover a lost ACK, but can never
		// exceed TxNext or move behind an ACK we already observed.
		if pf.RxAccepted < lf.TxAcked || pf.RxAccepted > lf.TxNext || lf.TxNext > pf.RxCredit {
			return Plan{}, fmt.Errorf("%w: peer receive/local send contradiction on stream %d", ErrStateMismatch, lf.StreamID)
		}
		// Symmetric check for the peer's Tx versus our authoritative receive.
		if lf.RxAccepted < pf.TxAcked || lf.RxAccepted > pf.TxNext || pf.TxNext > lf.RxCredit {
			return Plan{}, fmt.Errorf("%w: local receive/peer send contradiction on stream %d", ErrStateMismatch, lf.StreamID)
		}

		// FIN facts are monotonic. A side cannot claim it observed/acked an
		// event that the other side says never happened.
		if lf.FinRecv && !pf.FinSent {
			return Plan{}, fmt.Errorf("%w: local FIN receive without peer FIN send", ErrStateMismatch)
		}
		if pf.FinRecv && !lf.FinSent {
			return Plan{}, fmt.Errorf("%w: peer FIN receive without local FIN send", ErrStateMismatch)
		}
		if lf.FinAcked && !pf.FinAckSent {
			return Plan{}, fmt.Errorf("%w: local FIN_ACK receive without peer FIN_ACK send", ErrStateMismatch)
		}
		if pf.FinAcked && !lf.FinAckSent {
			return Plan{}, fmt.Errorf("%w: peer FIN_ACK receive without local FIN_ACK send", ErrStateMismatch)
		}
		if lf.FinAckConfirmed && !pf.FinAcked {
			return Plan{}, fmt.Errorf("%w: local FIN_ACK confirmation without peer FIN_ACK acceptance", ErrStateMismatch)
		}
		if pf.FinAckConfirmed && !lf.FinAcked {
			return Plan{}, fmt.Errorf("%w: peer FIN_ACK confirmation without local FIN_ACK acceptance", ErrStateMismatch)
		}

		out.Flows = append(out.Flows, FlowPlan{
			StreamID: lf.StreamID,
			LocalReplayFrom: pf.RxAccepted,
			PeerReplayFrom: lf.RxAccepted,
			LocalAckAdvanceTo: pf.RxAccepted,
			PeerAckAdvanceTo: lf.RxAccepted,
			LocalReleaseThrough: lf.TxAcked,
			PeerReleaseThrough: pf.TxAcked,
			// FIN_ACK write success is not acceptance proof. Same-process
			// recovery never fabricates FinAcked from peer write state; an
			// ambiguous FIN_ACK is retried and accepted idempotently.
			LocalFinAckCanAdvance: false,
			PeerFinAckCanAdvance: false,
			LocalFinAckConfirmCanAdvance: pf.FinAcked && lf.FinAckSent && !lf.FinAckConfirmed,
			PeerFinAckConfirmCanAdvance: lf.FinAcked && pf.FinAckSent && !pf.FinAckConfirmed,
		})
	}
	sort.Slice(out.Flows, func(i, j int) bool { return out.Flows[i].StreamID < out.Flows[j].StreamID })
	return out, nil
}
