package identity

import (
	"math/big"
	"testing"
	"time"
)

func TestRevocationSetNotifiesExistingWatchers(t *testing.T) {
	r := NewRevocationSet()
	ch, unregister := r.Watch("urn:baft:node:ir-01", "03", "aa:bb")
	defer unregister()
	r.RevokeIdentity("urn:baft:node:ir-01")
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("watcher was not notified")
	}
	if !r.IsRevoked("urn:baft:node:ir-01", "3", "aabb") {
		t.Fatal("identity should be revoked")
	}
}

func TestRevocationSetNormalizesSerialAndFingerprint(t *testing.T) {
	r := NewRevocationSet()
	r.RevokeSerial("0A")
	if !r.IsRevoked("", "0a", "") {
		t.Fatal("serial normalization failed")
	}
	r.RevokeFingerprint("AA:BB:CC")
	if !r.IsRevoked("", "", "aabbcc") {
		t.Fatal("fingerprint normalization failed")
	}
}

// Operators copy serials from openssl, which pads to whole bytes ("0A:1B"),
// while the carrier derives the peer serial with big.Int.Text(16) ("a1b").
func TestRevokedSerialMatchesCarrierSerialFormat(t *testing.T) {
	r := NewRevocationSet()
	carrierSerial := big.NewInt(0x0a1b).Text(16)
	ch, unregister := r.Watch("urn:baft:node:ir-01", carrierSerial, "")
	defer unregister()
	r.RevokeSerial("0A:1B")
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("watcher for serial a1b was not notified by revoking 0A:1B")
	}
	if !r.IsRevoked("urn:baft:node:ir-01", carrierSerial, "") {
		t.Fatalf("serial %q should be revoked", carrierSerial)
	}
	if r.IsRevoked("", "1b", "") {
		t.Fatal("different serial must not be revoked")
	}
}
