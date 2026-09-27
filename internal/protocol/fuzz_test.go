package protocol

import (
	"bytes"
	"testing"
)

func FuzzDecode(f *testing.F) {
	seeds := [][]byte{{}, make([]byte, HeaderSize), {0,0,0,24,byte(TypeAck)}}
	for _, s := range seeds { f.Add(s) }
	f.Fuzz(func(t *testing.T, b []byte) { _, _ = Decode(bytes.NewReader(b)) })
}
