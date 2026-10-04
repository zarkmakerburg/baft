package securityinternal

import (
	"testing"
	"time"
)

// A pairing code / reply is parsed from attacker-controllable input before any
// session authentication: a malformed BAFTPAIR1 or BAFTREPLY1 string must be
// rejected cleanly, never panic, never read out of bounds, never hang. These
// are the earliest reachable parsers, so a bug here is a pre-auth defect.
func seedPairing(f *testing.F) string {
	resp, err := GenerateKeyPair()
	if err != nil {
		f.Fatal(err)
	}
	d, _, err := NewPairingDescriptor("127.0.0.1:8443", "127.0.0.1", "urn:baft:node:ex", resp.Public, []byte("ca"), time.Minute)
	if err != nil {
		f.Fatal(err)
	}
	code, err := d.Encode()
	if err != nil {
		f.Fatal(err)
	}
	return code
}

func FuzzDecodePairingDescriptor(f *testing.F) {
	f.Add(seedPairing(f))
	f.Add("BAFTPAIR1:")
	f.Add("BAFTPAIR1:!!!!")
	f.Add("")
	f.Add("BAFTPAIR1:" + "QQ")
	f.Fuzz(func(t *testing.T, s string) {
		_, _, _, _, _ = DecodePairingDescriptor(s, time.Unix(1_000_000, 0))
	})
}

func FuzzDecodePairingReply(f *testing.F) {
	resp, _ := GenerateKeyPair()
	f.Add("BAFTREPLY1:", []byte("psk"), resp.Public)
	f.Add("", []byte{}, []byte{})
	f.Add("BAFTREPLY1:!!!", []byte("0123456789abcdef0123456789abcdef"), resp.Public)
	f.Fuzz(func(t *testing.T, s string, psk, responderPub []byte) {
		_, _, _ = DecodePairingReply(s, psk, responderPub, time.Unix(1_000_000, 0))
	})
}
