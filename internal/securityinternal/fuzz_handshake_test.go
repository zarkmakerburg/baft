package securityinternal

import (
	"bytes"
	"testing"
)

// readHandshakeFrame parses the length-prefixed Noise handshake frames that an
// unauthenticated peer sends first. A malformed or oversized prefix must be
// rejected cleanly: never panic, never allocate unboundedly, never hang.
func FuzzReadHandshakeFrame(f *testing.F) {
	f.Add([]byte{0, 0})
	f.Add([]byte{0, 5, 1, 2, 3, 4, 5})
	f.Add([]byte{0xff, 0xff})
	f.Add([]byte{0x10, 0x00})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		msg, err := readHandshakeFrame(bytes.NewReader(b))
		if err == nil && len(msg) > maxHandshakeFrameSize {
			t.Fatalf("accepted an oversized handshake frame: %d > %d", len(msg), maxHandshakeFrameSize)
		}
	})
}
