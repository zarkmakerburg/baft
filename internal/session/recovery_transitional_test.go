package session

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/zarkmakerburg/baft/internal/protocol"
	"github.com/zarkmakerburg/baft/internal/recovery"
)

// mirrorOffer builds the offer of a peer that holds the same Flows as local,
// except those listed in closed, which it has already closed and tombstoned.
func mirrorOffer(p *Peer,local RecoveryOffer,closed ...uint64) RecoveryOffer {
	gone:=map[uint64]bool{}
	for _,id:=range closed{gone[id]=true}
	peer:=RecoveryOffer{CandidateID:local.CandidateID,NextEpoch:local.NextEpoch,Snapshot:recovery.Snapshot{
		SessionID:local.Snapshot.SessionID,BootID:p.PeerBootID(),Epoch:local.Snapshot.Epoch,
	},Routes:map[uint64]string{},ClosedStreams:closed}
	for _,lf:=range local.Snapshot.Flows{
		if gone[lf.StreamID]{continue}
		peer.Snapshot.Flows=append(peer.Snapshot.Flows,recovery.FlowSnapshot{StreamID:lf.StreamID,OpenNonce:lf.OpenNonce,RxCredit:64})
		peer.Routes[lf.StreamID]=local.Routes[lf.StreamID]
	}
	return peer
}

// A carrier can fail while an OPEN is still unanswered. Whether the listener
// saw that OPEN is unknowable, so the Flow cannot be part of a recovery
// snapshot; but it must not make recovery of every other Flow impossible.
func TestRecoveryAbandonsUnansweredOpenInsteadOfFreezing(t *testing.T){
	p,_,_,cancel:=recoveryFixture(t,1);defer cancel()
	pending:=newFlow(3,"route-b","aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"+fmtTwo(1),nil,p.allocator)
	p.mu.Lock();p.flows[3]=pending;p.mu.Unlock()

	offer,err:=p.BeginRecovery("candidate-1")
	if err!=nil{t.Fatalf("carrier loss during an unanswered OPEN made the session unrecoverable: %v (txn state %s)",err,p.RecoveryTransactionState())}
	if len(offer.Snapshot.Flows)!=1||offer.Snapshot.Flows[0].StreamID!=1{t.Fatalf("snapshot flows=%+v, want only the open Flow 1",offer.Snapshot.Flows)}
	select{
	case err:=<-pending.openDone:
		if !errors.Is(err,ErrOpenAbandoned){t.Fatalf("unanswered OpenFlow got %v, want ErrOpenAbandoned",err)}
	default:
		t.Fatal("the unanswered OpenFlow was left waiting")
	}
	if _,err:=p.getFlow(3);err==nil{t.Fatal("the unanswered Flow is still registered")}
	// The listener must learn about it in case the OPEN did arrive there.
	if len(offer.ClosedStreams)!=1||offer.ClosedStreams[0]!=3{t.Fatalf("offer closed streams=%v, want [3]",offer.ClosedStreams)}
}

// One side can finish and forget a Flow whose last FIN_ACK_CONFIRM (or RESET)
// never reached the other side because the carrier failed. The engine needs
// both snapshots to hold the same Flow set, so retrying alone cannot converge;
// applying the peer's tombstones does.
func TestRecoveryConvergesWhenPeerAlreadyClosedAFlow(t *testing.T){
	p,_,_,cancel:=recoveryFixture(t,2);defer cancel()
	// Flow 3: FIN and FIN_ACK done both ways, only the final confirm is missing.
	stale,err:=p.getFlow(3);if err!=nil{t.Fatal(err)}
	stale.mu.Lock();stale.txAcked=stale.txNext;stale.replay=nil;stale.finSent=true;stale.finAcked=true;stale.finRecv=true;stale.finAckSent=true;stale.mu.Unlock()

	// Without the peer's tombstones every attempt fails the same way.
	for _,candidate:=range []string{"candidate-1","candidate-2"}{
		local,err:=p.BeginRecovery(candidate);if err!=nil{t.Fatal(err)}
		peer:=mirrorOffer(p,local,3)
		if err:=p.ReconcileRecovery(candidate,peer);!errors.Is(err,recovery.ErrStateMismatch){t.Fatalf("%s: reconcile err=%v, want state mismatch",candidate,err)}
		p.AbortRecovery(candidate)
	}

	local,err:=p.BeginRecovery("candidate-3");if err!=nil{t.Fatal(err)}
	peer:=mirrorOffer(p,local,3)
	if n:=p.ClosePeerClosedFlows(peer.ClosedStreams);n!=1{t.Fatalf("closed %d flows from the peer's tombstones, want 1",n)}
	p.AbortRecovery("candidate-3")
	if _,err:=p.getFlow(3);err==nil{t.Fatal("Flow 3 survived although the peer had closed it")}
	if _,err:=p.getFlow(1);err!=nil{t.Fatal("the live Flow 1 was closed too")}

	local,err=p.BeginRecovery("candidate-4");if err!=nil{t.Fatal(err)}
	if len(local.Snapshot.Flows)!=1||local.Snapshot.Flows[0].StreamID!=1{t.Fatalf("snapshot flows=%+v, want only Flow 1",local.Snapshot.Flows)}
	if err:=p.ReconcileRecovery("candidate-4",mirrorOffer(p,local));err!=nil{t.Fatalf("recovery still does not converge: %v",err)}
}

func TestFailedSnapshotLeavesNoPreparingState(t *testing.T){
	p,_,_,cancel:=recoveryFixture(t,1);defer cancel()
	p.mu.Lock();session:=p.sessionID;p.sessionID="";p.mu.Unlock()
	if _,err:=p.BeginRecovery("candidate-1");err==nil{t.Fatal("snapshot without a session id must fail")}
	if st:=p.RecoveryTransactionState();st!=RecoveryTxnIdle{t.Fatalf("failed BeginRecovery left txn state %s, want IDLE",st)}
	p.mu.Lock();p.sessionID=session;p.mu.Unlock()
	if _,err:=p.BeginRecovery("candidate-2");err!=nil{t.Fatalf("recovery after a failed snapshot: %v",err)}
}

func TestPeerTombstonesDoNotTouchAnUnresolvedExactTransaction(t *testing.T){
	p,_,cancel,_:=makeUncertainFixture(t,"candidate-1");defer cancel()
	if !p.NeedsExactTransactionResolution(){t.Fatal("fixture is not in an unresolved exact transaction")}
	if n:=p.ClosePeerClosedFlows([]uint64{1});n!=0{t.Fatalf("closed %d flows of an unresolved exact transaction",n)}
	if _,err:=p.getFlow(1);err!=nil{t.Fatal("Flow of an unresolved exact transaction was closed")}
}

func TestRecoveryOfferClosedStreamsAreBoundedAndRoundTrip(t *testing.T){
	offer:=RecoveryOffer{CandidateID:"c",NextEpoch:2,Snapshot:recovery.Snapshot{SessionID:"s",BootID:"b",Epoch:1},Routes:map[uint64]string{},ClosedStreams:[]uint64{3,5},Rejected:true}
	var buf bytes.Buffer
	if err:=EncodeRecoveryOffer(&buf,offer);err!=nil{t.Fatal(err)}
	fr,err:=protocol.Decode(&buf);if err!=nil{t.Fatal(err)}
	got,err:=DecodeRecoveryOffer(fr);if err!=nil{t.Fatal(err)}
	if len(got.ClosedStreams)!=2||got.ClosedStreams[1]!=5||!got.Rejected{t.Fatalf("round trip lost fields: %+v",got)}

	for name,streams:=range map[string][]uint64{"zero stream":{0},"too many":make([]uint64,maxClosedFlowTombstones+1)}{
		for i:=range streams{if name=="too many"{streams[i]=uint64(2*i+1)}}
		offer.ClosedStreams=streams
		buf.Reset()
		if err:=EncodeRecoveryOffer(&buf,offer);err!=nil{t.Fatal(err)}
		fr,err:=protocol.Decode(&buf);if err!=nil{t.Fatal(err)}
		if _,err:=DecodeRecoveryOffer(fr);err==nil{t.Errorf("%s: offer accepted",name)}
	}
}

// OPEN_OK for a Flow the dialer already gave up on must not end the Session,
// and the listener must be told to close the Flow it opened.
func TestLateOpenAnswersForAbandonedFlowAreAbsorbed(t *testing.T){
	var out bytes.Buffer
	p,err:=New(Dialer,Carrier{In:bytes.NewReader(nil),Out:&out},"urn:baft:node:ex-01",nil,Options{NodeID:"ir-01",ExpectedPeerNodeID:"ex-01"});if err!=nil{t.Fatal(err)}
	fl:=newFlow(1,"main","00112233445566778899aabbccddeeff",nil,p.allocator)
	p.mu.Lock();p.flows[1]=fl;p.localReady=true;p.peerReady=true;p.markReadyLocked();p.mu.Unlock()
	fl.close();p.removeFlow(1)

	if err:=p.handleFrame(context.Background(),protocol.Frame{Type:protocol.TypeOpenOK,StreamID:1,Payload:[]byte("{}")});err!=nil{t.Fatalf("late OPEN_OK ended the session: %v",err)}
	fr,err:=protocol.Decode(&out);if err!=nil{t.Fatalf("no RESET sent for the Flow the listener opened: %v",err)}
	if fr.Type!=protocol.TypeReset||fr.StreamID!=1{t.Fatalf("sent %v on stream %d, want RESET on 1",fr.Type,fr.StreamID)}
	payload,_:=protocol.EncodeControl(protocol.OpenError{Code:protocol.ErrorTargetUnreachable})
	if err:=p.handleFrame(context.Background(),protocol.Frame{Type:protocol.TypeOpenErr,StreamID:1,Payload:payload});err!=nil{t.Fatalf("late OPEN_ERR ended the session: %v",err)}
	if err:=p.handleFrame(context.Background(),protocol.Frame{Type:protocol.TypeOpenOK,StreamID:9,Payload:[]byte("{}")});err==nil{t.Fatal("OPEN_OK for a never-known stream must remain a protocol error")}
}
