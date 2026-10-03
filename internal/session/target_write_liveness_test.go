package session

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/protocol"
)

type targetWriteTimeoutError struct{}

func (targetWriteTimeoutError) Error() string   { return "target write timeout" }
func (targetWriteTimeoutError) Timeout() bool   { return true }
func (targetWriteTimeoutError) Temporary() bool { return true }

type targetWriteTestConn struct {
	mu       sync.Mutex
	maxWrite int
	alwaysTO bool
	total    int
	onTotal  func(int)
	deadline time.Time
	closed   bool
}

func (c *targetWriteTestConn) Read([]byte) (int, error) { return 0, io.EOF }

func (c *targetWriteTestConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, net.ErrClosed
	}
	if c.alwaysTO {
		return 0, targetWriteTimeoutError{}
	}
	n := len(p)
	if c.maxWrite > 0 && n > c.maxWrite {
		n = c.maxWrite
	}
	c.total += n
	if c.onTotal != nil {
		c.onTotal(c.total)
	}
	return n, nil
}

func (c *targetWriteTestConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

func (c *targetWriteTestConn) LocalAddr() net.Addr             { return testNetAddr("local") }
func (c *targetWriteTestConn) RemoteAddr() net.Addr            { return testNetAddr("remote") }
func (c *targetWriteTestConn) SetDeadline(time.Time) error     { return nil }
func (c *targetWriteTestConn) SetReadDeadline(time.Time) error { return nil }
func (c *targetWriteTestConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadline = t
	c.mu.Unlock()
	return nil
}

type testNetAddr string

func (a testNetAddr) Network() string { return "test" }
func (a testNetAddr) String() string  { return string(a) }

func targetPumpFixture(t *testing.T, conn net.Conn, payload []byte) (*Peer, *flow, *bytes.Buffer) {
	t.Helper()
	alloc := defaultAllocator()
	var out bytes.Buffer
	p := &Peer{
		sender:       newOutboundSender(&frameWriter{w: &out}, false),
		allocator:    alloc,
		flows:        map[uint64]*flow{},
		closedFlows:  map[uint64]struct{}{},
		finishedFins: map[uint64]uint64{},
	}
	fl := newFlow(1, "route", "00112233445566778899aabbccddeeff", conn, alloc)
	fl.openOK = true
	ring, err := newReceiveRing(int(defaultWindow))
	if err != nil {
		t.Fatal(err)
	}
	if err := ring.Write(payload); err != nil {
		t.Fatal(err)
	}
	fl.rxRing = ring
	fl.receiveReserved = int64(defaultWindow)
	fl.rxNext = uint64(len(payload))
	fl.rxMax = defaultWindow
	p.flows[fl.id] = fl
	return p, fl, &out
}

func TestPumpTargetAdvancesCreditOnPartialWriteProgress(t *testing.T) {
	payload := bytes.Repeat([]byte{0x5a}, dataChunk)
	ctx, cancel := context.WithCancel(context.Background())
	conn := &targetWriteTestConn{maxWrite: 1024}
	conn.onTotal = func(n int) {
		if n >= len(payload) {
			cancel()
		}
	}
	p, fl, _ := targetPumpFixture(t, conn, payload)

	done := make(chan struct{})
	go func() {
		defer close(done)
		p.pumpTargetWithPolicy(ctx, fl, time.Millisecond, 100*time.Millisecond)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pumpTarget did not finish after delivered payload")
	}

	fl.mu.Lock()
	written := fl.rxWritten
	max := fl.rxMax
	fl.mu.Unlock()
	if written != uint64(len(payload)) {
		t.Fatalf("rxWritten=%d want=%d", written, len(payload))
	}
	if max != uint64(len(payload))+defaultWindow {
		t.Fatalf("rxMax=%d want=%d", max, uint64(len(payload))+defaultWindow)
	}
	if got := fl.rxRing.Len(); got != 0 {
		t.Fatalf("receive ring bytes=%d want=0", got)
	}
}

func TestPumpTargetResetsOnlyFlowAfterBoundedNoProgress(t *testing.T) {
	payload := bytes.Repeat([]byte{0x7b}, dataChunk)
	conn := &targetWriteTestConn{alwaysTO: true}
	p, fl, out := targetPumpFixture(t, conn, payload)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	p.pumpTargetWithPolicy(ctx, fl, time.Millisecond, 15*time.Millisecond)
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("no-progress flow reset took too long: %v", elapsed)
	}

	fl.mu.Lock()
	closed := fl.closed
	code := fl.resetCode
	fl.mu.Unlock()
	if !closed {
		t.Fatal("stalled target flow was not closed")
	}
	if code != protocol.ErrorTargetUnreachable {
		t.Fatalf("reset code=%q want=%q", code, protocol.ErrorTargetUnreachable)
	}
	if out.Len() == 0 {
		t.Fatal("RESET was not emitted")
	}
}
