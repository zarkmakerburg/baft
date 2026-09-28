package recovery

import (
	"fmt"
	"testing"
)

func TestSixRouteECRLEngineHealth(t *testing.T) {
	for i := 1; i <= 6; i++ {
		name := fmt.Sprintf("route-%02d", i)
		t.Run(name, func(t *testing.T) {
			candidate := "resume-" + name
			engine, err := NewEngine(1, "carrier-old", EngineOptions{})
			if err != nil {
				t.Fatal(err)
			}
			local := Snapshot{
				SessionID: name,
				BootID:    "local-boot",
				Epoch:     1,
				Flows: []FlowSnapshot{{
					StreamID:    1,
					OpenNonce:   "open-1",
					TxNext:      10,
					TxAcked:     4,
					RxAccepted:  6,
					RxDelivered: 6,
					RxCredit:    32,
				}},
			}
			peer := Snapshot{
				SessionID: name,
				BootID:    "peer-boot",
				Epoch:     1,
				Flows: []FlowSnapshot{{
					StreamID:    1,
					OpenNonce:   "open-1",
					TxNext:      8,
					TxAcked:     4,
					RxAccepted:  7,
					RxDelivered: 6,
					RxCredit:    32,
				}},
			}
			if err := engine.SetActiveFlows([]FlowIdentity{{StreamID: 1, OpenNonce: "open-1"}}); err != nil {
				t.Fatal(err)
			}
			if err := engine.Prepare(2, candidate); err != nil {
				t.Fatal(err)
			}
			plan, err := engine.Reconcile(candidate, local, peer, "peer-boot")
			if err != nil {
				t.Fatal(err)
			}
			if err := engine.Commit(2, candidate, plan); err != nil {
				t.Fatal(err)
			}
			if engine.Authorize(1, "carrier-old") {
				t.Fatal("old carrier remained authorized")
			}
			if !engine.Authorize(2, candidate) {
				t.Fatal("new carrier was not authorized")
			}
			t.Logf("PASS %s ecrl epoch=%d owner=%s replay_from=%d", name, engine.CurrentEpoch(), engine.Owner(), plan.Flows[0].LocalReplayFrom)
		})
	}
}
