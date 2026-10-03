package recovery

import (
	"errors"
	"testing"
)

func TestEngineRejectsForgedFINACKAcceptanceWithoutPeerSend(t *testing.T) {
	e := engineForMutation(t, 9, "old", nil)
	local, peer := stage1Snapshots(9)
	local.Flows[0].FinSent = true
	local.Flows[0].FinAcked = true
	peer.Flows[0].FinRecv = true
	peer.Flows[0].FinAckSent = false
	armFlows(t, e, local)
	if err := e.Prepare(10, "new"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Reconcile("new", local, peer, "peer-boot"); !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("forged FIN_ACK acceptance escaped fail-closed reconciliation: %v", err)
	}
}
