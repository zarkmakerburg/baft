package identity

import (
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
