package session

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/resources"
)

func TestReceiveRingGrowKeepsWrappedBytesAndHeldPeek(t *testing.T) {
	r, err := newReceiveRing(8)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Write([]byte("abcdef")); err != nil {
		t.Fatal(err)
	}
	if err := r.Consume(4); err != nil {
		t.Fatal(err)
	}
	if err := r.Write([]byte("ghijkl")); err != nil { // wraps: "efghijkl"
		t.Fatal(err)
	}
	held, err := r.Peek(context.Background(), 3)
	if err != nil || string(held) != "efg" {
		t.Fatalf("peek=%q err=%v", held, err)
	}
	if err := r.Grow(16); err != nil {
		t.Fatal(err)
	}
	if string(held) != "efg" {
		t.Fatalf("held peek changed after Grow: %q", held)
	}
	if err := r.Consume(len(held)); err != nil {
		t.Fatal(err)
	}
	if err := r.Write([]byte("mnopqrst")); err != nil {
		t.Fatal(err)
	}
	var got []byte
	for r.Len() > 0 {
		b, err := r.Peek(context.Background(), 64)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, b...)
		if err := r.Consume(len(b)); err != nil {
			t.Fatal(err)
		}
	}
	if string(got) != "hijklmnopqrst" {
		t.Fatalf("got %q", got)
	}
	if r.Capacity() != 16 {
		t.Fatalf("capacity=%d", r.Capacity())
	}
	if err := r.Grow(16); err == nil {
		t.Fatal("Grow to the same size must fail")
	}
	r.Close()
	if err := r.Grow(32); err == nil {
		t.Fatal("Grow after Close must fail")
	}
}

// fakeWindowClock replaces windowClock for one test; step advances it.
func fakeWindowClock(t *testing.T) (step func(time.Duration)) {
	t.Helper()
	now := time.Unix(1_000_000, 0)
	windowClock = func() time.Time { return now }
	t.Cleanup(func() { windowClock = time.Now })
	return func(d time.Duration) { now = now.Add(d) }
}

func autotunePeer(t *testing.T, l resources.Limits) (*Peer, *resources.Allocator) {
	t.Helper()
	a, err := resources.NewAllocator(l)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	p, err := New(Dialer, Carrier{In: bytes.NewReader(nil), Out: &out}, "urn:baft:node:ex-01", nil, Options{NodeID: "ir-01", ExpectedPeerNodeID: "ex-01", Resources: a})
	if err != nil {
		t.Fatal(err)
	}
	return p, a
}

// deliverWindow models a peer that sends exactly up to the advertised credit
// and a target that accepts all of it, then asks for the next grant.
func deliverWindow(t *testing.T, p *Peer, f *flow, step func(time.Duration), rtt time.Duration) uint64 {
	t.Helper()
	step(rtt) // the window's bytes arrive one round trip after its grant
	f.mu.Lock()
	start, end := f.rxNext, f.rxMax
	f.mu.Unlock()
	if _, _, err := f.acceptData(start, make([]byte, end-start)); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	n := int(f.rxNext - f.rxWritten)
	f.mu.Unlock()
	if err := f.rxRing.Consume(n); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.rxWritten += uint64(n)
	f.mu.Unlock()
	max, err := p.reserveReceiveWindow(f)
	if err != nil {
		t.Fatal(err)
	}
	return max
}

func TestReceiveWindowGrowsWhileCreditLimitedAndDrained(t *testing.T) {
	step := fakeWindowClock(t)
	p, a := autotunePeer(t, resources.DefaultLimits())
	f := newFlow(1, "main", "00112233445566778899aabbccddeeff", nil, a)
	f.openOK = true
	if _, err := p.reserveReceiveWindow(f); err != nil {
		t.Fatal(err)
	}
	deliverWindow(t, p, f, step, 100*time.Millisecond) // first turn only samples the RTT
	if f.receiveReserved != int64(defaultWindow) {
		t.Fatalf("grew before an RTT sample and a timed turn: %d", f.receiveReserved)
	}
	want := int64(defaultWindow)
	for want < int64(maxWindow) {
		max := deliverWindow(t, p, f, step, 100*time.Millisecond)
		want *= 2
		f.mu.Lock()
		reserved, written := f.receiveReserved, f.rxWritten
		f.mu.Unlock()
		if reserved != want || f.rxRing.Capacity() != int(want) || max != written+uint64(want) {
			t.Fatalf("reserved=%d ring=%d credit=%d written=%d want window %d", reserved, f.rxRing.Capacity(), max, written, want)
		}
		if s := a.Snapshot(); s.ReceiveUsed != want {
			t.Fatalf("allocator receive=%d want %d", s.ReceiveUsed, want)
		}
	}
	deliverWindow(t, p, f, step, 100*time.Millisecond)
	if f.receiveReserved != int64(maxWindow) {
		t.Fatalf("window grew past maxWindow: %d", f.receiveReserved)
	}
	if v := p.ConservationSnapshot().Violations; v != 0 {
		t.Fatalf("TWRL violations after growth: %d", v)
	}
	f.close()
	if s := a.Snapshot(); s.TotalUsed != 0 {
		t.Fatalf("close leaked reservation: %#v", s)
	}
}

func TestReceiveWindowDoesNotGrowForSlowTargetOrUnusedCredit(t *testing.T) {
	step := fakeWindowClock(t)
	defer step(0)
	p, a := autotunePeer(t, resources.DefaultLimits())
	f := newFlow(1, "main", "00112233445566778899aabbccddeeff", nil, a)
	f.openOK = true
	if _, err := p.reserveReceiveWindow(f); err != nil {
		t.Fatal(err)
	}
	// Peer filled the window but the target took only a quarter: the ring is
	// still more than half full, so the target is the bottleneck.
	if _, _, err := f.acceptData(0, make([]byte, defaultWindow)); err != nil {
		t.Fatal(err)
	}
	if err := f.rxRing.Consume(int(defaultWindow / 4)); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.rxWritten = defaultWindow / 4
	f.mu.Unlock()
	if _, err := p.reserveReceiveWindow(f); err != nil {
		t.Fatal(err)
	}
	if f.receiveReserved != int64(defaultWindow) {
		t.Fatalf("slow target grew window to %d", f.receiveReserved)
	}
	// The flag was consumed: a later drain without new credit-limited sending
	// does not grow either.
	f.mu.Lock()
	n := int(f.rxNext - f.rxWritten)
	f.mu.Unlock()
	if err := f.rxRing.Consume(n); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.rxWritten = f.rxNext
	f.mu.Unlock()
	if _, err := p.reserveReceiveWindow(f); err != nil {
		t.Fatal(err)
	}
	if f.receiveReserved != int64(defaultWindow) {
		t.Fatalf("window grew without credit-limited sending: %d", f.receiveReserved)
	}
	// Partial use of the credit is not credit-limited.
	if _, _, err := f.acceptData(f.rxNext, make([]byte, 1024)); err != nil {
		t.Fatal(err)
	}
	if f.rxCreditLimited {
		t.Fatal("partial window use marked the flow credit-limited")
	}
	f.close()
}

func TestReceiveWindowGrowthKeepsHalfThePoolFree(t *testing.T) {
	const kib = 1024
	step := fakeWindowClock(t)
	l := resources.Limits{Total: 1024 * kib, Receive: 512 * kib, Replay: 512 * kib, PerFlowReceive: 512 * kib, PerFlowReplay: 512 * kib}
	p, a := autotunePeer(t, l)
	f := newFlow(1, "main", "00112233445566778899aabbccddeeff", nil, a)
	f.openOK = true
	if _, err := p.reserveReceiveWindow(f); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 7; i++ {
		deliverWindow(t, p, f, step, 100*time.Millisecond)
	}
	// 64 -> 128 -> 256 KiB; 512 KiB would leave less than 256 KiB free.
	if f.receiveReserved != 256*kib {
		t.Fatalf("window=%d KiB, want 256 KiB", f.receiveReserved/kib)
	}
	g := newFlow(2, "main", "00112233445566778899aabbccddeef0", nil, a)
	g.openOK = true
	if _, err := p.reserveReceiveWindow(g); err != nil {
		t.Fatalf("new flow could not get its initial window: %v", err)
	}
	f.close()
	g.close()
	if s := a.Snapshot(); s.TotalUsed != 0 {
		t.Fatalf("leak: %#v", s)
	}
}

// On a short path the round trip is tiny and a window turns over in more than
// two round trips: the window is not the limit, so it does not grow.
func TestReceiveWindowDoesNotGrowWhenTurnsAreSlowerThanTwoRTT(t *testing.T) {
	step := fakeWindowClock(t)
	p, a := autotunePeer(t, resources.DefaultLimits())
	f := newFlow(1, "main", "00112233445566778899aabbccddeeff", nil, a)
	f.openOK = true
	if _, err := p.reserveReceiveWindow(f); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		// Bytes arrive 1 ms after each grant (the RTT), but each window
		// takes 5 ms to turn over: the sender or the path is the limit.
		deliverWindow(t, p, f, step, time.Millisecond)
		step(4 * time.Millisecond)
	}
	if f.rxMinRTT != time.Millisecond {
		t.Fatalf("min RTT=%v, want 1ms", f.rxMinRTT)
	}
	if f.receiveReserved != int64(defaultWindow) {
		t.Fatalf("window grew to %d on a path where it is not the limit", f.receiveReserved)
	}
	f.close()
}
