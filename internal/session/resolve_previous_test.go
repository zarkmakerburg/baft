package session

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/zarkmakerburg/baft/internal/recovery"
	"github.com/zarkmakerburg/baft/internal/routes"
)

// finalizedStableFixture returns a dialer whose epoch-2 transaction is
// finalized and, from this side's view, stable.
func finalizedStableFixture(t *testing.T,candidate string)(*Peer,RecoveryControl,func()){
	t.Helper()
	p,ctx,cancel,_,ctl:=prepareCommitFixture(t,candidate)
	p.recovery.postCommitFault=func(stage string)error{if stage=="after_authority_commit"{return errors.New("after commit")};return nil}
	if res,err:=p.CommitPreparedRecovery(ctx,ctl);!res.Committed||!errors.Is(err,ErrPostCommitFailure){cancel();t.Fatalf("result=%+v err=%v",res,err)}
	resolveCommittedForTest(t,p,ctx,ctl)
	proveFixtureTransactionStableForTest(t,p)
	if p.NeedsExactTransactionResolution(){cancel();t.Fatal("fixture still needs exact resolution")}
	return p,ctl,cancel
}

// A listener that still owes proof for the last transaction answers a fresh
// offer with ResolvePrevious. The dialer must then resolve that exact
// transaction instead of offering fresh epochs that are refused until
// retention expires.
func TestDeferToPreviousTransactionPinsLastCommit(t *testing.T){
	p,ctl,cancel:=finalizedStableFixture(t,"candidate-prev");defer cancel()
	if _,err:=p.BeginRecovery("candidate-fresh");err!=nil{t.Fatal(err)}
	if err:=p.DeferToPreviousTransaction("candidate-other");!errors.Is(err,recovery.ErrStateMismatch){t.Fatalf("wrong candidate: %v",err)}
	if err:=p.DeferToPreviousTransaction("candidate-fresh");err!=nil{t.Fatal(err)}

	if st:=p.RecoveryTransactionState();st!=RecoveryTxnFinalized{t.Fatalf("state=%s, want FINALIZED",st)}
	if p.RecoveryFrozen(){t.Fatal("withdrawn candidate left the session frozen")}
	if p.RecoveryEpoch()!=ctl.NextEpoch||p.RecoveryOwner()!=ctl.CandidateID{t.Fatal("withdrawing the candidate changed authority")}
	if !p.NeedsExactTransactionResolution()||!p.NeedsRecovery(){t.Fatal("previous transaction is not pinned for resolution")}
	if _,err:=p.BeginRecovery("candidate-fresh-2");!errors.Is(err,ErrCommitUncertain){t.Fatalf("fresh epoch offered while pinned: %v",err)}
	q,err:=p.CommitStatusQuery();if err!=nil{t.Fatal(err)}
	if q.Phase!=RecoveryPhaseStatusQuery||!sameRecoveryTransaction(q,ctl){t.Fatalf("status query %+v does not name the previous transaction",q)}

	p.ClearPeerExactRequirement()
	if p.NeedsExactTransactionResolution(){t.Fatal("still pinned after resolution")}
	next,err:=p.BeginRecovery("candidate-fresh-3");if err!=nil{t.Fatal(err)}
	if next.NextEpoch!=ctl.NextEpoch+1{t.Fatalf("next epoch=%d",next.NextEpoch)}
	p.AbortRecovery("candidate-fresh-3")
}

// A conflict reply means the listener does not hold this transaction, so the
// pin must not outlive it.
func TestResolutionConflictReleasesPeerPin(t *testing.T){
	p,ctl,cancel:=finalizedStableFixture(t,"candidate-conflict");defer cancel()
	if _,err:=p.BeginRecovery("candidate-fresh");err!=nil{t.Fatal(err)}
	if err:=p.DeferToPreviousTransaction("candidate-fresh");err!=nil{t.Fatal(err)}
	reply:=ctl;reply.Phase=RecoveryPhaseStatusReply;reply.Status=RecoveryResolutionConflict
	if err:=p.ValidateStatusReply(reply);err!=nil{t.Fatalf("conflict about the pinned transaction rejected: %v",err)}
	_ = p.NoteResolutionConflict(reply)
	if p.NeedsExactTransactionResolution(){t.Fatal("conflict left the previous transaction pinned")}
}

// Rebinding a transaction after activation must also carry Flows opened since
// then; otherwise their first frame fails the new sender and their in-flight
// data is never replayed.
func TestRebindAfterActivationCarriesNewerFlows(t *testing.T){
	p,ctl,cancel:=finalizedStableFixture(t,"candidate-rebind");defer cancel()
	fl:=newFlow(99,"route-new","bbbbbbbbbbbbbbbbbbbbbbbbbbbbbb00",nil,p.allocator)
	fl.openOK=true;fl.peerMax=64;fl.rxMax=64;fl.txNext=3;fl.txAcked=1
	fl.replay=[]replayChunk{{start:0,end:3,data:[]byte{7,8,9}}}
	p.mu.Lock();p.flows[99]=fl;p.mu.Unlock()

	var out bytes.Buffer
	if err:=p.RebindPreparedRecovery(p.runCtxForTest(),ctl,Carrier{In:bytes.NewReader(nil),Out:&out});err!=nil{t.Fatal(err)}
	p.recovery.mu.Lock()
	prep:=p.recovery.prepared
	var carried *preparedFlowRecovery
	for i:=range prep.flows{if prep.flows[i].flow==fl{carried=&prep.flows[i]}}
	p.recovery.mu.Unlock()
	if carried==nil{t.Fatal("rebind dropped a Flow opened after activation")}
	if carried.replayFrom!=1||carried.replayHighWatermark!=3{t.Fatalf("replay range %d..%d, want 1..3",carried.replayFrom,carried.replayHighWatermark)}
	if err:=prep.sender.addFlow(99);err==nil{t.Fatal("new sender did not register the newer Flow")}
}

func (p *Peer) runCtxForTest() context.Context {
	p.mu.Lock();defer p.mu.Unlock()
	if p.runCtx!=nil{return p.runCtx}
	return context.Background()
}

// A closed Flow can never prove an outstanding obligation, so it must not
// keep the transaction unstable (and the Session pinned to it) forever.
func TestClosedFlowDoesNotPinTransactionStability(t *testing.T){
	p,_,cancel:=finalizedStableFixture(t,"candidate-closed");defer cancel()
	p.recovery.mu.Lock()
	act:=&p.recovery.prepared.flows[0]
	act.resendFIN=true
	fl:=act.flow
	p.recovery.mu.Unlock()
	fl.mu.Lock();fl.finAcked=false;fl.mu.Unlock()
	if st:=p.RecoveryStability();st.FinStable{t.Fatal("fixture obligation not counted")}
	fl.close()
	if st:=p.RecoveryStability();!st.FinStable||!st.TransactionStable{t.Fatalf("closed Flow still pins the transaction: %+v",st)}
}

// A listener Flow the dialer never granted window to may still be waiting for
// an OPEN_OK lost with the failed carrier, so an exact rebind repeats its
// OPEN_OK instead of planning a replay.
func TestRebindRepeatsOpenOKForUngrantedListenerFlow(t *testing.T){
	tbl,err:=routes.New([]routes.Route{{ID:"main",Target:"127.0.0.1:2443",AllowedPeers:map[string]struct{}{"urn:baft:node:ir-01":{}}}});if err!=nil{t.Fatal(err)}
	l,err:=New(Listener,Carrier{In:bytes.NewReader(nil),Out:&bytes.Buffer{}},"urn:baft:node:ir-01",tbl,Options{NodeID:"ex-01",ExpectedPeerNodeID:"ir-01"});if err!=nil{t.Fatal(err)}
	ungranted:=newFlow(1,"main","00112233445566778899aabbccddeeff",nil,l.allocator);ungranted.openOK=true
	granted:=newFlow(3,"main","00112233445566778899aabbccddeef0",nil,l.allocator);granted.openOK=true;granted.peerMax=64;granted.txNext=9;granted.txAcked=4
	if e,ok:=l.rebindEntryForLiveFlow(ungranted);!ok||!e.repeatOpenOK||e.replayHighWatermark!=0{t.Fatalf("ungranted listener Flow: %+v ok=%v",e,ok)}
	if e,ok:=l.rebindEntryForLiveFlow(granted);!ok||e.repeatOpenOK||e.replayFrom!=4||e.replayHighWatermark!=9{t.Fatalf("granted listener Flow: %+v ok=%v",e,ok)}
	ungranted.close()
	if _,ok:=l.rebindEntryForLiveFlow(ungranted);ok{t.Fatal("closed Flow included in the rebind")}

	d,_,_,cancel:=recoveryFixture(t,0);defer cancel()
	open:=newFlow(5,"main","00112233445566778899aabbccddeef1",nil,d.allocator);open.openOK=true
	if e,ok:=d.rebindEntryForLiveFlow(open);!ok||e.repeatOpenOK{t.Fatalf("dialer Flow: %+v ok=%v",e,ok)}
}
