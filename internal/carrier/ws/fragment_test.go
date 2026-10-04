package ws

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

func maskedFrame(fin bool, opcode byte, payload []byte) []byte {
	first := opcode
	if fin {
		first |= 0x80
	}
	if len(payload) >= 126 {
		panic("test helper supports short frames only")
	}
	out := []byte{first, 0x80 | byte(len(payload)), 0, 0, 0, 0}
	return append(out, payload...)
}

func connForFrames(data []byte) *Conn {
	return &Conn{
		nc:       &fakeConn{r: bytes.NewReader(nil)},
		isClient: false,
		br:       bufio.NewReader(bytes.NewReader(data)),
		closed:   make(chan struct{}),
	}
}

func TestFragmentedMessageStreamsBeforeFIN(t *testing.T) {
	// Only the first non-FIN fragment exists in the input. A byte-stream
	// implementation must return it immediately; waiting to reconstruct the
	// whole WebSocket message would block/read EOF before delivering "abc".
	c := connForFrames(maskedFrame(false, opBinary, []byte("abc")))
	buf := make([]byte, 3)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatalf("Read first fragment: %v", err)
	}
	if n != 3 || string(buf[:n]) != "abc" {
		t.Fatalf("got n=%d payload=%q", n, buf[:n])
	}
	if !c.fragmented {
		t.Fatal("fragmentation state was not retained after non-FIN frame")
	}
}

func TestFragmentedMessageStreamsContinuationsWithControlInterleave(t *testing.T) {
	data := append([]byte{}, maskedFrame(false, opBinary, []byte("ab"))...)
	data = append(data, maskedFrame(true, opPing, []byte("p"))...)
	data = append(data, maskedFrame(true, opContinuation, []byte("cd"))...)
	c := connForFrames(data)

	buf := make([]byte, 2)
	n, err := c.Read(buf)
	if err != nil || n != 2 || string(buf[:n]) != "ab" {
		t.Fatalf("first read n=%d err=%v payload=%q", n, err, buf[:n])
	}
	n, err = c.Read(buf)
	if err != nil || n != 2 || string(buf[:n]) != "cd" {
		t.Fatalf("continuation read n=%d err=%v payload=%q", n, err, buf[:n])
	}
	if c.fragmented {
		t.Fatal("fragmentation state remained set after FIN continuation")
	}
}

func TestOrphanContinuationRejected(t *testing.T) {
	c := connForFrames(maskedFrame(true, opContinuation, []byte("x")))
	buf := make([]byte, 1)
	if _, err := c.Read(buf); err == nil || !strings.Contains(err.Error(), "continuation without") {
		t.Fatalf("orphan continuation err=%v", err)
	}
}

func TestNewBinaryDuringFragmentRejected(t *testing.T) {
	data := append([]byte{}, maskedFrame(false, opBinary, []byte("a"))...)
	data = append(data, maskedFrame(true, opBinary, []byte("b"))...)
	c := connForFrames(data)
	buf := make([]byte, 1)
	if n, err := c.Read(buf); err != nil || n != 1 || string(buf[:n]) != "a" {
		t.Fatalf("first read n=%d err=%v payload=%q", n, err, buf[:n])
	}
	if _, err := c.Read(buf); err == nil || !strings.Contains(err.Error(), "before fragmented message completed") {
		t.Fatalf("second binary err=%v", err)
	}
}
