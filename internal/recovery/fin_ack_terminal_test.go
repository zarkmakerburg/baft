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

func TestEngineFINACKConfirmationIsSymmetricAndRequiresAcceptance(t *testing.T) {
	tests := []struct {
		name             string
		localAck         bool
		peerAccepted     bool
		alreadyConfirmed bool
		wantLocalConfirm bool
		wantPeerConfirm  bool
	}{
		{name: "local write without peer acceptance", localAck: true},
		{name: "local peer acceptance", localAck: true, peerAccepted: true, wantLocalConfirm: true},
		{name: "local already confirmed", localAck: true, peerAccepted: true, alreadyConfirmed: true},
		{name: "peer write without local acceptance"},
		{name: "peer local acceptance", peerAccepted: true, wantPeerConfirm: true},
		{name: "peer already confirmed", peerAccepted: true, alreadyConfirmed: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			local, peer := validBaseSnapshotsForHarness()
			lf, pf := &local.Flows[0], &peer.Flows[0]
			if tc.localAck {
				lf.FinRecv = true
				lf.FinAckSent = true
				pf.FinSent = true
				if tc.peerAccepted {
					pf.FinAcked = true
				}
				if tc.alreadyConfirmed {
					lf.FinAckConfirmed = true
				}
			} else {
				lf.FinSent = true
				pf.FinRecv = true
				pf.FinAckSent = true
				if tc.peerAccepted {
					lf.FinAcked = true
				}
				if tc.alreadyConfirmed {
					pf.FinAckConfirmed = true
				}
			}

			e := engineForMutation(t, local.Epoch, "old", nil)
			armFlows(t, e, local)
			if err := e.Prepare(local.Epoch+1, "candidate"); err != nil {
				t.Fatalf("Prepare(): %v", err)
			}
			plan, err := e.Reconcile("candidate", local, peer, peer.BootID)
			if err != nil {
				t.Fatalf("Reconcile(): %v", err)
			}
			got := plan.Flows[0]
			if got.LocalFinAckConfirmCanAdvance != tc.wantLocalConfirm ||
				got.PeerFinAckConfirmCanAdvance != tc.wantPeerConfirm {
				t.Fatalf("FIN_ACK confirmation local=%v peer=%v; want local=%v peer=%v",
					got.LocalFinAckConfirmCanAdvance, got.PeerFinAckConfirmCanAdvance,
					tc.wantLocalConfirm, tc.wantPeerConfirm)
			}
		})
	}
}
