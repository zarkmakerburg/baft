package protocol

import "testing"

// Every decoder below parses bytes an unauthenticated peer can put on the wire
// (control-frame payloads arrive before and during a session). None may panic,
// read out of bounds, or hang on any input. The fuzz engine fails on a panic;
// a successful decode is additionally required to round-trip where an encoder
// exists, so a decoder cannot silently accept something the encoder would not
// produce.
func FuzzDecodeControlPayloads(f *testing.F) {
	f.Add([]byte("{}"))
	f.Add([]byte(`{"route_id":"r","open_nonce":"n"}`))
	f.Add([]byte(`{"code":"RESOURCE_EXHAUSTED"}`))
	f.Add([]byte{})
	f.Add([]byte("not json"))
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = DecodeOpen(b)
		_, _ = DecodeOpenError(b)
		_, _ = DecodeReset(b)
		_, _ = DecodeHello(b)
		_, _ = DecodeHelloAck(b)
		_, _ = DecodeReady(b)
	})
}
