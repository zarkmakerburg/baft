package session

import (
	"bytes"
	"context"
	"testing"

	"github.com/zarkmakerburg/baft/internal/protocol"
	"github.com/zarkmakerburg/baft/internal/resources"
)

// readyPeer builds a Dialer Peer past READY with one open flow, for feeding it
// hostile post-authentication frames.
func readyPeer(t *testing.T) (*Peer, *flow) {
	t.Helper()
	a, err := resources.NewAllocator(resources.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	p, err := New(Dialer, Carrier{In: bytes.NewReader(nil), Out: &out}, "urn:baft:node:ex", nil,
		Options{NodeID: "ir", ExpectedPeerNodeID: "ex", Resources: a})
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.localReady = true
	p.peerReady = true
	p.markReadyLocked()
	p.mu.Unlock()
	fl := newFlow(1, "main", "00112233445566778899aabbccddeeff", nil, a)
	fl.openOK = true
	p.mu.Lock()
	p.flows[1] = fl
	p.mu.Unlock()
	if _, err := p.reserveReceiveWindow(fl); err != nil {
		t.Fatal(err)
	}
	return p, fl
}

// An authenticated peer that sends DATA beyond the advertised window must be
// refused (RESET), not crash the session or deliver the bytes.
func TestIntrusionDataBeyondCreditIsRejected(t *testing.T) {
	p, fl := readyPeer(t)
	fl.mu.Lock()
	credit := fl.rxMax
	fl.mu.Unlock()
	// Use a payload within MaxPayloadSize but past the advertised window, so
	// the flow-control check (not frame validation) is what must reject it.
	over := make([]byte, credit+1)
	_ = p.handleFrame(context.Background(), protocol.Frame{Type: protocol.TypeData, StreamID: 1, Offset: 0, Payload: over})
	// Rejection means: the offset never advanced and the flow was torn down
	// (RESET), never that the bytes were accepted.
	fl.mu.Lock()
	acc := fl.rxNext
	fl.mu.Unlock()
	if acc != 0 {
		t.Fatalf("over-credit DATA advanced accepted offset to %d", acc)
	}
	if _, err := p.getFlow(1); err == nil {
		t.Fatal("flow survived an over-credit DATA frame (should be reset)")
	}
}

// DATA for a stream that was never opened must be a clean protocol error, not a
// panic or a silent accept.
func TestIntrusionDataForUnknownStream(t *testing.T) {
	p, _ := readyPeer(t)
	err := p.handleFrame(context.Background(), protocol.Frame{Type: protocol.TypeData, StreamID: 99, Offset: 0, Payload: []byte("x")})
	if err == nil {
		t.Fatal("DATA for an unknown stream was accepted")
	}
}

// A FIN_ACK that acknowledges more than was ever sent must be rejected.
func TestIntrusionFinAckBeyondSentIsRejected(t *testing.T) {
	_, fl := readyPeer(t)
	fl.mu.Lock()
	fl.finSent = true
	fl.txNext = 10
	fl.mu.Unlock()
	if err := fl.onFinAck(9999); err == nil {
		t.Fatal("FIN_ACK beyond txNext was accepted")
	}
}
