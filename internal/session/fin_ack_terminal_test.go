package session

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/zarkmakerburg/baft/internal/protocol"
	"github.com/zarkmakerburg/baft/internal/recovery"
)

type emitThenErrorWriter struct {
	b     bytes.Buffer
	err   error
	fired bool
}

func (w *emitThenErrorWriter) Write(p []byte) (int, error) {
	n, _ := w.b.Write(p)
	if !w.fired {
		w.fired = true
		return n, w.err
	}
	return n, nil
}

func TestRecoveryFINACKAmbiguousWriteKeepsSentAndRetriesUntilConfirm(t *testing.T) {
	p, _, _, cancel := recoveryFixture(t, 1)
	defer cancel()

	fl := p.flows[1]
	fl.mu.Lock()
	fl.txAcked = fl.txNext
	fl.finSent = true
	fl.finAcked = true
	fl.finRecv = true
	fl.finRecvFinal = 0
	fl.rxNext = 0
	fl.rxWritten = 0
	fl.rxMax = defaultWindow
	fl.finAckSent = false
	fl.finAckConfirmed = false
	fl.mu.Unlock()

	first := &emitThenErrorWriter{err: context.Canceled}
	p.writer.mu.Lock()
	p.writer.w = first
	p.writer.mu.Unlock()

	err := p.ackRemoteFin(fl)
	if !errors.Is(err, ErrCarrierUnavailable) || !errors.Is(err, context.Canceled) {
		t.Fatalf("ambiguous FIN_ACK write error classification=%v", err)
	}
	fr, decErr := protocol.Decode(bytes.NewReader(first.b.Bytes()))
	if decErr != nil {
		t.Fatalf("FIN_ACK bytes were not emitted before local error: %v", decErr)
	}
	if fr.Type != protocol.TypeFinAck || fr.StreamID != fl.id || fr.Offset != 0 {
		t.Fatalf("unexpected first terminal frame: %+v", fr)
	}

	fl.mu.Lock()
	sent, confirmed := fl.finAckSent, fl.finAckConfirmed
	fl.mu.Unlock()
	if !sent || confirmed {
		t.Fatalf("ambiguous write rolled back terminal state sent=%v confirmed=%v", sent, confirmed)
	}

	local, _, snapErr := p.recoverySnapshot()
	if snapErr != nil {
		t.Fatal(snapErr)
	}
	lf := local.Flows[0]
	peer := recovery.Snapshot{
		SessionID: local.SessionID,
		BootID:    p.peerBootID,
		Epoch:     local.Epoch,
		Flows: []recovery.FlowSnapshot{{
			StreamID:        lf.StreamID,
			OpenNonce:       lf.OpenNonce,
			TxNext:          lf.RxAccepted,
			TxAcked:         lf.RxDelivered,
			RxAccepted:      lf.TxNext,
			RxDelivered:     lf.TxNext,
			RxCredit:        lf.TxNext + defaultWindow,
			FinSent:         lf.FinRecv,
			FinRecv:         lf.FinSent,
			FinAcked:        lf.FinAckSent,
			FinAckSent:      lf.FinAcked,
			FinAckConfirmed: false,
		}},
	}
	if _, err := recovery.Reconcile(local, peer, p.peerBootID); err != nil {
		t.Fatalf("reconcile rejected monotonic ambiguous FIN_ACK state: %v local=%+v peer=%+v", err, local, peer)
	}

	var retry bytes.Buffer
	p.writer.mu.Lock()
	p.writer.w = &retry
	p.writer.mu.Unlock()
	if err := p.ackRemoteFin(fl); err != nil {
		t.Fatalf("FIN_ACK retry failed: %v", err)
	}
	retryFrame, err := protocol.Decode(bytes.NewReader(retry.Bytes()))
	if err != nil {
		t.Fatalf("FIN_ACK was not resent: %v", err)
	}
	if retryFrame.Type != protocol.TypeFinAck || retryFrame.StreamID != fl.id {
		t.Fatalf("unexpected retry frame: %+v", retryFrame)
	}
	if err := fl.onFinAckConfirm(0); err != nil {
		t.Fatalf("FIN_ACK confirmation failed: %v", err)
	}
	p.finishIfComplete(fl)

	p.mu.Lock()
	_, exists := p.flows[fl.id]
	p.mu.Unlock()
	if exists {
		t.Fatal("flow remained live after idempotent FIN_ACK retry and confirmation")
	}
}
