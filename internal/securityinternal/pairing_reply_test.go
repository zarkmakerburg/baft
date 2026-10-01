package securityinternal

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestPairingReplyRoundTripAndTamper(t *testing.T) {
	ex, _ := GenerateKeyPair()
	ir, _ := GenerateKeyPair()
	d, psk, err := NewPairingDescriptor("192.0.2.1:8443", "192.0.2.1", "urn:baft:node:ex-1", ex.Public, []byte("ca"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := NewPairingReply(d, ex.Public, psk, "urn:baft:node:ir-1", ir.Public)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	id, pub, err := DecodePairingReply(reply, psk, ex.Public, now)
	if err != nil || id != "urn:baft:node:ir-1" || !bytes.Equal(pub, ir.Public) {
		t.Fatalf("round trip: %q %v", id, err)
	}

	otherPSK := bytes.Repeat([]byte{7}, 32)
	if _, _, err := DecodePairingReply(reply, otherPSK, ex.Public, now); err == nil {
		t.Fatal("reply accepted under a different PSK")
	}
	other, _ := GenerateKeyPair()
	if _, _, err := DecodePairingReply(reply, psk, other.Public, now); err == nil {
		t.Fatal("reply accepted for a different responder key")
	}
	if _, _, err := DecodePairingReply(reply, psk, ex.Public, time.Unix(d.ExpiresUnix, 0)); err == nil {
		t.Fatal("expired reply accepted")
	}

	// Substituting the key or identity without the PSK must fail.
	raw, _ := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(reply, PairingReplyPrefix))
	for _, mutate := range []func(*PairingReply){
		func(r *PairingReply) { r.PublicKey = base64.RawURLEncoding.EncodeToString(other.Public) },
		func(r *PairingReply) { r.NodeIdentity = "urn:baft:node:evil" },
		func(r *PairingReply) { r.ExpiresUnix += 3600 },
	} {
		var r PairingReply
		_ = json.Unmarshal(raw, &r)
		mutate(&r)
		b, _ := json.Marshal(r)
		if _, _, err := DecodePairingReply(PairingReplyPrefix+base64.RawURLEncoding.EncodeToString(b), psk, ex.Public, now); err == nil {
			t.Fatal("tampered reply accepted")
		}
	}
}
