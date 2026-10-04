package protocol

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// legacyEncode is the previous two-write encoding, kept to prove the bytes on
// the stream did not change.
func legacyEncode(f Frame) []byte {
	var hdr [HeaderSize]byte
	binary.BigEndian.PutUint32(hdr[0:4], uint32(HeaderSize+len(f.Payload)))
	hdr[4] = byte(f.Type)
	binary.BigEndian.PutUint64(hdr[8:16], f.StreamID)
	binary.BigEndian.PutUint64(hdr[16:24], f.Offset)
	return append(hdr[:], f.Payload...)
}

type countingWriter struct {
	bytes.Buffer
	writes int
}

func (w *countingWriter) Write(p []byte) (int, error) { w.writes++; return w.Buffer.Write(p) }

func TestEncodeIsOneWriteWithUnchangedBytes(t *testing.T) {
	big := bytes.Repeat([]byte{0xa5}, MaxPayloadSize)
	frames := []Frame{
		{Type: TypeData, StreamID: 7, Offset: 1 << 40, Payload: big},
		{Type: TypeData, StreamID: 3, Offset: 9, Payload: []byte("x")},
		{Type: TypeWindow, StreamID: 3, Offset: 65536},
		{Type: TypePing, Payload: []byte("12345678")},
	}
	for _, f := range frames {
		var w countingWriter
		if err := Encode(&w, f); err != nil {
			t.Fatal(err)
		}
		if w.writes != 1 {
			t.Fatalf("%v: %d writes, want 1", f.Type, w.writes)
		}
		if !bytes.Equal(w.Bytes(), legacyEncode(f)) {
			t.Fatalf("%v: encoded bytes changed", f.Type)
		}
		got, err := Decode(bytes.NewReader(w.Bytes()))
		if err != nil || got.Type != f.Type || got.StreamID != f.StreamID || got.Offset != f.Offset || !bytes.Equal(got.Payload, f.Payload) {
			t.Fatalf("%v: round trip failed: %v", f.Type, err)
		}
	}
	// A reused buffer must not leak a previous, longer payload.
	var w countingWriter
	if err := Encode(&w, frames[0]); err != nil {
		t.Fatal(err)
	}
	w.Reset()
	if err := Encode(&w, frames[1]); err != nil || !bytes.Equal(w.Bytes(), legacyEncode(frames[1])) {
		t.Fatalf("reused buffer leaked bytes: %x", w.Bytes())
	}
}

func TestEncodeDoesNotAllocateOnTheHotPath(t *testing.T) {
	f := Frame{Type: TypeData, StreamID: 5, Offset: 0, Payload: make([]byte, 32*1024)}
	var w bytes.Buffer
	w.Grow(64 * 1024)
	allocs := testing.AllocsPerRun(200, func() {
		w.Reset()
		if err := Encode(&w, f); err != nil {
			t.Fatal(err)
		}
	})
	if allocs > 0 {
		t.Fatalf("Encode allocates %.1f times per frame", allocs)
	}
}

func BenchmarkEncode32K(b *testing.B) {
	f := Frame{Type: TypeData, StreamID: 5, Payload: make([]byte, 32*1024)}
	var w bytes.Buffer
	w.Grow(64 * 1024)
	b.SetBytes(int64(len(f.Payload)))
	for i := 0; i < b.N; i++ {
		w.Reset()
		_ = Encode(&w, f)
	}
}
