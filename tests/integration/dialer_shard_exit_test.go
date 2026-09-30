package integration_test

import (
	"testing"
	"time"
)

// Without recovery, a Shard whose carrier ends with a clean EOF can never carry
// traffic again. The dialer must report it instead of keeping the dead Shard in
// the round-robin rotation and silently closing every Nth new connection.
func TestDialerStopsWhenListenerEndsShardCleanly(t *testing.T) {
	p := startRuntimePair(t, 1, false)
	defer p.closeAllowErrors()
	c := openRecoveryFlow(t, p)
	defer c.Close()

	// Revoking the IR identity makes the EX handler end the carrier stream
	// normally, so the IR Shard reads a clean EOF rather than a transport error.
	p.exRuntime.Revocations.RevokeIdentity("urn:baft:node:ir-recovery")

	select {
	case err := <-p.irDone:
		p.irDone <- err
		if err == nil {
			t.Fatal("IR runtime returned nil after losing its only Shard")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("IR runtime kept running with a dead Shard")
	}
}
