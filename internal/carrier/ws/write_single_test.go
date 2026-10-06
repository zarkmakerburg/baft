package ws

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

type countingNetConn struct {
	bytes.Buffer
	writes int
}

func (c *countingNetConn) Read([]byte) (int, error) { return 0, io.EOF }
func (c *countingNetConn) Write(p []byte) (int, error) {
	c.writes++
	return c.Buffer.Write(p)
}
func (c *countingNetConn) Close() error                       { return nil }
func (c *countingNetConn) LocalAddr() net.Addr                { return dummyAddr("local") }
func (c *countingNetConn) RemoteAddr() net.Addr               { return dummyAddr("remote") }
func (c *countingNetConn) SetDeadline(time.Time) error        { return nil }
func (c *countingNetConn) SetReadDeadline(time.Time) error    { return nil }
func (c *countingNetConn) SetWriteDeadline(time.Time) error   { return nil }

type dummyAddr string

func (a dummyAddr) Network() string { return "test" }
func (a dummyAddr) String() string  { return string(a) }

func decodeWrittenFrame(t *testing.T, wire []byte, wantMasked bool) []byte {
	t.Helper()
	if len(wire) < 2 {
		t.Fatalf("short frame: %d", len(wire))
	}
	if wire[0] != 0x82 {
		t.Fatalf("first byte = %#x, want FIN+binary", wire[0])
	}
	masked := wire[1]&0x80 != 0
	if masked != wantMasked {
		t.Fatalf("masked=%v want %v", masked, wantMasked)
	}
	n := int(wire[1] & 0x7f)
	off := 2
	switch n {
	case 126:
		if len(wire) < 4 {
			t.Fatal("short 16-bit length")
		}
		n = int(wire[2])<<8 | int(wire[3])
		off = 4
	case 127:
		t.Fatal("test payload unexpectedly used 64-bit length")
	}
	var key []byte
	if masked {
		if len(wire) < off+4 {
			t.Fatal("short mask key")
		}
		key = wire[off : off+4]
		off += 4
	}
	if len(wire) != off+n {
		t.Fatalf("wire len=%d want %d", len(wire), off+n)
	}
	out := append([]byte(nil), wire[off:]...)
	if masked {
		for i := range out {
			out[i] ^= key[i&3]
		}
	}
	return out
}

func TestWriteFrameUsesSingleUnderlyingWrite(t *testing.T) {
	payload := bytes.Repeat([]byte{0xa5}, 98) // Noise IK msg1-sized field probe.

	for _, tc := range []struct {
		name     string
		isClient bool
	}{
		{name: "client-masked", isClient: true},
		{name: "server-unmasked", isClient: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := &countingNetConn{}
			c := newConn(raw, nil, tc.isClient)
			n, err := c.Write(payload)
			if err != nil {
				t.Fatal(err)
			}
			if n != len(payload) {
				t.Fatalf("Write n=%d want %d", n, len(payload))
			}
			if raw.writes != 1 {
				t.Fatalf("underlying writes=%d want 1", raw.writes)
			}
			got := decodeWrittenFrame(t, raw.Bytes(), tc.isClient)
			if !bytes.Equal(got, payload) {
				t.Fatal("wire payload changed")
			}
		})
	}
}
