package session

import (
	"testing"

	"github.com/zarkmakerburg/baft/internal/resources"
)

// FuzzFlowReceivePath drives a Flow's receive-path state machine with an
// arbitrary sequence of operations an authenticated-but-hostile peer could
// generate: DATA at arbitrary offsets/sizes, WINDOW advertisements, ACKs and
// FIN_ACKs. No input may panic or break the TWRL conservation invariants
// (delivered <= accepted <= credit, and the ring holds exactly accepted minus
// delivered). The op stream is drawn from the fuzz bytes.
func FuzzFlowReceivePath(f *testing.F) {
	f.Add([]byte{0, 8, 1, 0})
	f.Add([]byte{0, 255, 0, 200, 2, 0, 3, 0})
	f.Fuzz(func(t *testing.T, ops []byte) {
		a, err := resources.NewAllocator(resources.DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		fl := newFlow(1, "r", "00112233445566778899aabbccddeeff", nil, a)
		fl.openOK = true
		defer fl.close()
		scratch := make([]byte, cap(make([]byte, 0, defaultWindow)))
		// Grant an initial window directly (reserveReceiveWindow needs a Peer;
		// here we exercise the flow methods in isolation).
		if err := a.Reserve(fl.resourceID, resources.Receive, int64(defaultWindow)); err != nil {
			return
		}
		ring, err := newReceiveRing(int(defaultWindow))
		if err != nil {
			return
		}
		fl.mu.Lock()
		fl.receiveReserved = int64(defaultWindow)
		fl.rxRing = ring
		fl.rxMax = defaultWindow
		fl.mu.Unlock()

		i := 0
		next := func() byte {
			if i >= len(ops) {
				return 0
			}
			b := ops[i]
			i++
			return b
		}
		for i < len(ops) {
			switch next() % 4 {
			case 0: // DATA at offset, length drawn from the stream
				off := uint64(next()) * 256
				n := int(next())
				if cap(scratch) < n {
					n = cap(scratch)
				}
				ack, dup, derr := fl.acceptData(off, scratch[:n])
				if derr == nil {
					invariant(t, fl, ack, dup)
				}
			case 1: // WINDOW
				_ = fl.onWindow(uint64(next()) * 1024)
			case 2: // ACK
				_ = fl.onAck(uint64(next()) * 256)
			case 3: // FIN_ACK / confirm
				off := uint64(next()) * 256
				_ = fl.onFinAck(off)
				_ = fl.onFinAckConfirm(off)
			}
		}
	})
}

func invariant(t *testing.T, fl *flow, ack uint64, dup bool) {
	fl.mu.Lock()
	defer fl.mu.Unlock()
	if fl.rxWritten > fl.rxNext {
		t.Fatalf("delivered %d > accepted %d", fl.rxWritten, fl.rxNext)
	}
	if fl.rxNext > fl.rxMax {
		t.Fatalf("accepted %d > credit %d", fl.rxNext, fl.rxMax)
	}
	if fl.rxRing != nil {
		if uint64(fl.rxRing.Len()) != fl.rxNext-fl.rxWritten {
			t.Fatalf("ring %d != accepted-delivered %d", fl.rxRing.Len(), fl.rxNext-fl.rxWritten)
		}
	}
}
