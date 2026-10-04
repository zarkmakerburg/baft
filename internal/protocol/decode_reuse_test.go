package protocol

import (
	"bytes"
	"testing"
)

func TestDecodeReuseSharesOnlyDataPayloads(t *testing.T) {
	var stream bytes.Buffer
	frames := []Frame{
		{Type: TypeData, StreamID: 1, Offset: 0, Payload: bytes.Repeat([]byte{1}, 1000)},
		{Type: TypePing, Payload: []byte("abcdefgh")},
		{Type: TypeData, StreamID: 1, Offset: 1000, Payload: bytes.Repeat([]byte{2}, MaxPayloadSize)},
		{Type: TypeData, StreamID: 1, Offset: 1000 + MaxPayloadSize, Payload: []byte{3, 3}},
	}
	for _, f := range frames {
		if err := Encode(&stream, f); err != nil {
			t.Fatal(err)
		}
	}
	var buf []byte
	var ping Frame
	var firstData []byte
	for i, want := range frames {
		got, err := DecodeReuse(&stream, &buf)
		if err != nil {
			t.Fatal(err)
		}
		if got.Type != want.Type || got.Offset != want.Offset || !bytes.Equal(got.Payload, want.Payload) {
			t.Fatalf("frame %d decoded wrong", i)
		}
		switch {
		case got.Type == TypePing:
			ping = got
		case i == 0:
			firstData = got.Payload
		}
	}
	if !bytes.Equal(ping.Payload, []byte("abcdefgh")) {
		t.Fatal("a control payload was overwritten by a later DATA frame")
	}
	if &firstData[0] != &buf[0] {
		t.Fatal("DATA payload did not use the reuse buffer")
	}
	if cap(buf) != MaxPayloadSize {
		t.Fatalf("reuse buffer cap=%d", cap(buf))
	}
}

func TestDecodeReuseDoesNotAllocateForData(t *testing.T) {
	var one bytes.Buffer
	if err := Encode(&one, Frame{Type: TypeData, StreamID: 9, Payload: make([]byte, 32*1024)}); err != nil {
		t.Fatal(err)
	}
	raw := one.Bytes()
	buf := make([]byte, MaxPayloadSize)
	r := bytes.NewReader(raw)
	allocs := testing.AllocsPerRun(200, func() {
		r.Reset(raw)
		if _, err := DecodeReuse(r, &buf); err != nil {
			t.Fatal(err)
		}
	})
	// The 24-byte header array escapes through io.Reader (as before); the
	// payload must not allocate.
	if allocs > 1 {
		t.Fatalf("DecodeReuse allocates %.1f times per DATA frame", allocs)
	}
}
