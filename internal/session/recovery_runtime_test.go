package session

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/protocol"
	"github.com/zarkmakerburg/baft/internal/recovery"
)

func recoveryFixture(t *testing.T,n int)(*Peer,*bytes.Buffer,context.Context,context.CancelFunc){
	t.Helper()
	var old bytes.Buffer
	p,err:=New(Dialer,Carrier{In:bytes.NewReader(nil),Out:&old},"urn:baft:node:peer",nil,Options{
		NodeID:"local",ExpectedPeerNodeID:"peer",RecoveryEnabled:true,RecoveryRetention:time.Second,CarrierID:"carrier-1",
	})
	if err!=nil{t.Fatal(err)}
	// Local-only session fixtures have no real peer reader to emit ACK frames.
	// Model authenticated peer acceptance explicitly through the dedicated
	// test hook; ambiguity tests disable this hook when they need uncertainty.
	p.recovery.peerAcceptanceTestHook=func(fl *flow,end uint64){ _ = fl.onAck(end) }
	p.mu.Lock()
	p.sessionID="11111111111111111111111111111111"
	p.peerBootID="22222222222222222222222222222222"
	p.mu.Unlock()
	for i:=0;i<n;i++{
		id:=uint64(1+i*2)
		fl:=newFlow(id,"route-"+string(rune('a'+i)), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"+string(rune('0'+i)),nil,p.allocator)
		// Replace generated test nonce with canonical 32-byte stable identity.
		fl.nonce="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"+fmtTwo(i)
		payload:=[]byte{byte(i+1),byte(i+2),byte(i+3),byte(i+4)}
		fl.openOK=true;fl.peerMax=64;fl.txNext=uint64(len(payload));fl.rxMax=64
		fl.replay=[]replayChunk{{start:0,end:uint64(len(payload)),data:append([]byte(nil),payload...)}}
		fl.finSent=true
		p.flows[id]=fl
	}
	// Unit fixtures do not run a real peer reader. Model the cumulative ACK
	// proof that the real Runtime obtains from authenticated peer acceptance.
	ctx,cancel:=context.WithCancel(context.Background())
	return p,&old,ctx,cancel
}

func fmtTwo(i int) string {
	const hex="0123456789abcdef"
	return string([]byte{hex[(i>>4)&15],hex[i&15]})
}

func peerOfferFor(t *testing.T,p *Peer,candidate string)(RecoveryOffer,RecoveryOffer){
	t.Helper()
	local,err:=p.BeginRecovery(candidate);if err!=nil{t.Fatal(err)}
	peer:=RecoveryOffer{CandidateID:candidate,NextEpoch:local.NextEpoch,Snapshot:recovery.Snapshot{
		SessionID:local.Snapshot.SessionID,BootID:p.PeerBootID(),Epoch:local.Snapshot.Epoch,
		Flows:make([]recovery.FlowSnapshot,0,len(local.Snapshot.Flows)),
	},Routes:map[uint64]string{}}
	for _,lf:=range local.Snapshot.Flows{
		peer.Snapshot.Flows=append(peer.Snapshot.Flows,recovery.FlowSnapshot{
			StreamID:lf.StreamID,OpenNonce:lf.OpenNonce,TxNext:0,TxAcked:0,
			RxAccepted:0,RxDelivered:0,RxCredit:64,FinRecv:false,FinAckSent:false,
		})
		peer.Routes[lf.StreamID]=local.Routes[lf.StreamID]
	}
	return local,peer
}

func TestOldEpochFramesRejectedAfterCommit(t *testing.T){
	p,_,ctx,cancel:=recoveryFixture(t,1);defer cancel()
	_,peer:=peerOfferFor(t,p,"carrier-2")
	if err:=p.ReconcileRecovery("carrier-2",peer);err!=nil{t.Fatal(err)}
	var next bytes.Buffer
	if _,err:=commitRecoveryAcceptedForTest(p,ctx,"carrier-2",Carrier{In:bytes.NewReader(nil),Out:&next});err!=nil{t.Fatal(err)}
	fl,_:=p.getOpenFlow(1);fl.mu.Lock();before:=fl.txAcked;fl.mu.Unlock()
	err:=p.HandleCarrierFrame(ctx,1,"carrier-1",protocol.Frame{Type:protocol.TypeAck,StreamID:1,Offset:4})
	if !errors.Is(err,recovery.ErrStaleEpoch){t.Fatalf("old frame err=%v",err)}
	fl.mu.Lock();after:=fl.txAcked;fl.mu.Unlock()
	if before!=after{t.Fatalf("old epoch mutated ACK %d -> %d",before,after)}
	if p.RecoveryEpoch()!=2||p.RecoveryOwner()!="carrier-2"{t.Fatalf("epoch/owner=%d/%s",p.RecoveryEpoch(),p.RecoveryOwner())}
}

func TestRecoveryFailureBeforeCommitKeepsOldOwner(t *testing.T){
	p,_,_,cancel:=recoveryFixture(t,1);defer cancel()
	_,peer:=peerOfferFor(t,p,"candidate-bad")
	fl,_:=p.getOpenFlow(1);fl.mu.Lock();fl.replay=nil;fl.mu.Unlock()
	if err:=p.ReconcileRecovery("candidate-bad",peer);err==nil{t.Fatal("expected replay reservation/availability failure")}
	p.AbortRecovery("candidate-bad")
	if p.RecoveryEpoch()!=1||p.RecoveryOwner()!="carrier-1"{t.Fatalf("partial transition epoch=%d owner=%s",p.RecoveryEpoch(),p.RecoveryOwner())}
	if p.recovery.IsFrozen(){t.Fatal("session remained frozen after abort")}
}

func TestRecoveryPeerBootChangeFailsClosed(t *testing.T){
	p,_,_,cancel:=recoveryFixture(t,1);defer cancel()
	_,peer:=peerOfferFor(t,p,"carrier-2")
	peer.Snapshot.BootID="33333333333333333333333333333333"
	err:=p.ReconcileRecovery("carrier-2",peer)
	if !errors.Is(err,recovery.ErrPeerRestarted){t.Fatalf("err=%v",err)}
	p.AbortRecovery("carrier-2")
	if p.RecoveryEpoch()!=1||p.RecoveryOwner()!="carrier-1"{t.Fatal("peer restart changed authority")}
}

func TestCompetingRecoveryCandidatesSingleWinner(t *testing.T){
	p,_,ctx,cancel:=recoveryFixture(t,1);defer cancel()
	_,peer:=peerOfferFor(t,p,"candidate-A")
	if _,err:=p.BeginRecovery("candidate-B");!errors.Is(err,recovery.ErrLeaseConflict){t.Fatalf("second candidate err=%v",err)}
	if err:=p.ReconcileRecovery("candidate-A",peer);err!=nil{t.Fatal(err)}
	var out bytes.Buffer
	if _,err:=commitRecoveryAcceptedForTest(p,ctx,"candidate-A",Carrier{In:bytes.NewReader(nil),Out:&out});err!=nil{t.Fatal(err)}
	if p.RecoveryOwner()!="candidate-A"||p.recovery.engine.Authorize(2,"candidate-B"){t.Fatal("competing candidate authorized")}
}

func TestRecoveryDuringFINPreservesCloseSemantics(t *testing.T){
	p,_,ctx,cancel:=recoveryFixture(t,1);defer cancel()
	_,peer:=peerOfferFor(t,p,"carrier-2")
	peer.Snapshot.Flows[0].FinRecv=true
	peer.Snapshot.Flows[0].FinAckSent=true
	if err:=p.ReconcileRecovery("carrier-2",peer);err!=nil{t.Fatal(err)}
	var out bytes.Buffer
	if _,err:=commitRecoveryAcceptedForTest(p,ctx,"carrier-2",Carrier{In:bytes.NewReader(nil),Out:&out});err!=nil{t.Fatal(err)}
	fl,_:=p.getOpenFlow(1)
	fl.mu.Lock();sent,acked,closed:=fl.finSent,fl.finAcked,fl.closed;fl.mu.Unlock()
	if !sent||acked||closed{t.Fatalf("FIN state sent=%v acked=%v closed=%v",sent,acked,closed)}
	foundFIN:=false
	for out.Len()>0{
		fr,err:=protocol.Decode(&out);if err!=nil{t.Fatal(err)}
		if fr.Type==protocol.TypeFin&&fr.StreamID==1{foundFIN=true}
	}
	if !foundFIN{t.Fatal("ambiguous FIN acknowledgement did not cause idempotent FIN retry")}
}

func TestMultiFlowCarrierReplacementNoDuplicateOrLoss(t *testing.T){
	p,_,ctx,cancel:=recoveryFixture(t,8);defer cancel()
	_,peer:=peerOfferFor(t,p,"carrier-2")
	if err:=p.ReconcileRecovery("carrier-2",peer);err!=nil{t.Fatal(err)}
	var out bytes.Buffer
	if _,err:=commitRecoveryAcceptedForTest(p,ctx,"carrier-2",Carrier{In:bytes.NewReader(nil),Out:&out});err!=nil{t.Fatal(err)}
	got:=map[uint64][]byte{}
	for out.Len()>0{
		fr,err:=protocol.Decode(&out);if err!=nil{t.Fatal(err)}
		if fr.Type==protocol.TypeData{got[fr.StreamID]=append(got[fr.StreamID],fr.Payload...)}
	}
	if len(got)!=8{t.Fatalf("replayed streams=%d",len(got))}
	for i:=0;i<8;i++{
		id:=uint64(1+i*2);want:=[]byte{byte(i+1),byte(i+2),byte(i+3),byte(i+4)}
		if !bytes.Equal(got[id],want){t.Fatalf("stream %d replay=%v want=%v",id,got[id],want)}
	}
	if p.RecoveryStats().ReplayedBytes!=32{t.Fatalf("replayed bytes=%d",p.RecoveryStats().ReplayedBytes)}
}

func TestRuntimeRecoveryRouteIdentityIsolation(t *testing.T){
	p,_,_,cancel:=recoveryFixture(t,2);defer cancel()
	_,peer:=peerOfferFor(t,p,"carrier-2")
	peer.Routes[1]="wrong-route"
	if err:=p.ReconcileRecovery("carrier-2",peer);!errors.Is(err,recovery.ErrStateMismatch){t.Fatalf("route mixing err=%v",err)}
	p.AbortRecovery("carrier-2")
	if p.RecoveryEpoch()!=1{t.Fatal("route mismatch committed")}
}

func TestRecoveryDisabledBaselineUnchanged(t *testing.T){
	var out bytes.Buffer
	p,err:=New(Dialer,Carrier{In:bytes.NewReader(nil),Out:&out},"urn:baft:node:peer",nil,Options{NodeID:"local",ExpectedPeerNodeID:"peer"})
	if err!=nil{t.Fatal(err)}
	if p.recovery!=nil||p.recoveryEnabled{t.Fatal("disabled baseline unexpectedly enabled ECRL")}
	if p.RecoveryEpoch()!=1{t.Fatalf("baseline epoch=%d",p.RecoveryEpoch())}
}


func TestRecoveryFailureImmediatelyBeforeCommitKeepsOldOwner(t *testing.T){
	p,_,ctx,cancel:=recoveryFixture(t,1);defer cancel()
	_,peer:=peerOfferFor(t,p,"candidate-precommit")
	if err:=p.ReconcileRecovery("candidate-precommit",peer);err!=nil{t.Fatal(err)}
	injected:=errors.New("injected before commit")
	p.recovery.beforeCommit=func()error{return injected}
	var out bytes.Buffer
	_,err:=p.CommitRecovery(ctx,"candidate-precommit",Carrier{In:bytes.NewReader(nil),Out:&out})
	if !errors.Is(err,injected){t.Fatalf("commit err=%v",err)}
	if p.RecoveryEpoch()!=1||p.RecoveryOwner()!="carrier-1"{t.Fatalf("authority changed epoch=%d owner=%s",p.RecoveryEpoch(),p.RecoveryOwner())}
	if !p.recovery.engine.Authorize(1,"carrier-1")||p.recovery.engine.Authorize(2,"candidate-precommit"){t.Fatal("candidate became authorized before commit")}
	p.AbortRecovery("candidate-precommit")
}

func TestRecoveryFreezeRejectsNewOpenOnLiveSession(t *testing.T){
	p,_,_,cancel:=recoveryFixture(t,1);defer cancel()
	p.mu.Lock()
	select{case <-p.readyCh:default:close(p.readyCh)}
	p.localReady=true;p.peerReady=true
	p.mu.Unlock()
	if _,err:=p.BeginRecovery("candidate-freeze");err!=nil{t.Fatal(err)}
	a,b:=net.Pipe();defer a.Close();defer b.Close()
	ctx,cancelOpen:=context.WithTimeout(context.Background(),time.Second);defer cancelOpen()
	err:=p.OpenFlow(ctx,"route-new",a)
	if !errors.Is(err,recovery.ErrResumeFrozen){t.Fatalf("new OPEN during recovery err=%v",err)}
	p.AbortRecovery("candidate-freeze")
}

func TestRecoveryDuringFINPreservesPendingFinAck(t *testing.T){
	p,_,ctx,cancel:=recoveryFixture(t,1);defer cancel()
	fl,_:=p.getOpenFlow(1)
	fl.mu.Lock()
	fl.finSent=false
	fl.finRecv=true
	fl.finRecvFinal=0
	fl.rxNext=0
	fl.rxWritten=0
	fl.finAckSent=false
	fl.mu.Unlock()

	_,peer:=peerOfferFor(t,p,"carrier-finack")
	peer.Snapshot.Flows[0].FinSent=true
	peer.Snapshot.Flows[0].TxNext=0
	peer.Snapshot.Flows[0].TxAcked=0
	if err:=p.ReconcileRecovery("carrier-finack",peer);err!=nil{t.Fatal(err)}
	var out bytes.Buffer
	if _,err:=commitRecoveryAcceptedForTest(p,ctx,"carrier-finack",Carrier{In:bytes.NewReader(nil),Out:&out});err!=nil{t.Fatal(err)}
	seen:=false
	for out.Len()>0{
		fr,err:=protocol.Decode(&out);if err!=nil{t.Fatal(err)}
		if fr.Type==protocol.TypeFinAck&&fr.StreamID==1&&fr.Offset==0{seen=true}
	}
	if !seen{t.Fatal("pending FIN_ACK was not replayed")}
	fl.mu.Lock();sent:=fl.finAckSent;closed:=fl.closed;fl.mu.Unlock()
	if !sent||closed{t.Fatalf("FIN_ACK state sent=%v closed=%v",sent,closed)}
}

func TestRecoveryBoundedReplayMissingBytesFailsClosed(t *testing.T){
	p,_,_,cancel:=recoveryFixture(t,1);defer cancel()
	_,peer:=peerOfferFor(t,p,"candidate-gap")
	fl,_:=p.getOpenFlow(1)
	fl.mu.Lock()
	fl.replay=[]replayChunk{{start:2,end:4,data:[]byte{3,4}}}
	fl.mu.Unlock()
	err:=p.ReconcileRecovery("candidate-gap",peer)
	if err==nil{t.Fatal("missing replay prefix was accepted")}
	p.AbortRecovery("candidate-gap")
	if p.RecoveryEpoch()!=1||p.RecoveryOwner()!="carrier-1"{t.Fatal("missing replay bytes changed authority")}
}


func prepareCommitFixture(t *testing.T,candidate string)(*Peer,context.Context,context.CancelFunc,*bytes.Buffer,RecoveryControl){
	t.Helper()
	p,_,ctx,cancel:=recoveryFixture(t,1)
	_,peer:=peerOfferFor(t,p,candidate)
	if err:=p.ReconcileRecovery(candidate,peer);err!=nil{cancel();t.Fatal(err)}
	out:=&bytes.Buffer{}
	ctl,err:=p.PrepareRecoveryCommit(ctx,candidate,Carrier{In:bytes.NewReader(nil),Out:out})
	if err!=nil{cancel();t.Fatal(err)}
	if err:=p.MarkRecoveryCommitReady(ctl);err!=nil{cancel();t.Fatal(err)}
	ctl.Phase=RecoveryPhaseCommit
	return p,ctx,cancel,out,ctl
}

func proveFinalizationForTest(t *testing.T,p *Peer,ctl RecoveryControl) RecoveryControl {
	t.Helper()
	finalCtl:=ctl;finalCtl.Phase=RecoveryPhaseFinalize;finalCtl.Status=RecoveryResolutionNone
	if err:=p.MarkFinalizationStarted(finalCtl);err!=nil{t.Fatal(err)}
	if err:=p.CompleteRecoveryFinalization(finalCtl);err!=nil{t.Fatal(err)}
	return finalCtl
}

func withSyntheticPeerAcceptance(p *Peer,fn func() (CommitResult,error))(CommitResult,error){
	a:=p.recovery
	a.mu.Lock()
	old:=a.peerAcceptanceTestHook
	a.peerAcceptanceTestHook=func(fl *flow,end uint64){ _ = fl.onAck(end) }
	a.mu.Unlock()
	defer func(){a.mu.Lock();a.peerAcceptanceTestHook=old;a.mu.Unlock()}()
	return fn()
}

func commitRecoveryAcceptedForTest(p *Peer,ctx context.Context,candidate string,c Carrier)(CommitResult,error){
	return withSyntheticPeerAcceptance(p,func()(CommitResult,error){return p.CommitRecovery(ctx,candidate,c)})
}

func finalizeRecoveryAcceptedForTest(p *Peer,ctx context.Context,ctl RecoveryControl) error {
	_,err:=withSyntheticPeerAcceptance(p,func()(CommitResult,error){
		err:=p.FinalizeRecoveryCommit(ctx,ctl)
		return CommitResult{},err
	})
	return err
}

func TestDuplicateCommitIsIdempotent(t *testing.T){
	p,ctx,cancel,out,ctl:=prepareCommitFixture(t,"candidate-idempotent");defer cancel()
	first,err:=p.PublishRecoveryCommit(ctl);if err!=nil{t.Fatal(err)}
	second,err:=p.PublishRecoveryCommit(ctl);if err!=nil{t.Fatal(err)}
	if !first.Committed||!second.Committed||first.Epoch!=2||second.Epoch!=2{t.Fatalf("results first=%+v second=%+v",first,second)}
	if got:=p.RecoveryStats().Commits;got!=1{t.Fatalf("authority commit counter=%d",got)}
	finalCtl:=proveFinalizationForTest(t,p,ctl)
	if err:=finalizeRecoveryAcceptedForTest(p,ctx,finalCtl);err!=nil{t.Fatal(err)}
	before:=append([]byte(nil),out.Bytes()...)
	if err:=finalizeRecoveryAcceptedForTest(p,ctx,finalCtl);err!=nil{t.Fatal(err)}
	if !bytes.Equal(before,out.Bytes()){t.Fatal("duplicate finalize replayed application bytes")}
	if p.RecoveryEpoch()!=2||p.RecoveryOwner()!="candidate-idempotent"{t.Fatal("duplicate commit changed authority")}
	t.Log("PASS duplicate COMMIT kept one authority transition and one replay")
}

func TestLostCommitACKRetryIsIdempotent(t *testing.T){
	p,ctx,cancel,out,ctl:=prepareCommitFixture(t,"candidate-ack-retry");defer cancel()
	if _,err:=p.PublishRecoveryCommit(ctl);err!=nil{t.Fatal(err)}
	ack1,err:=p.HandleCommittedRecoveryControl(ctl);if err!=nil{t.Fatal(err)}
	ack2,err:=p.HandleCommittedRecoveryControl(ctl);if err!=nil{t.Fatal(err)}
	if !sameRecoveryTransaction(ack1,ack2)||ack1.Phase!=RecoveryPhaseCommitAck||ack2.Phase!=RecoveryPhaseCommitAck{t.Fatalf("acks differ %+v %+v",ack1,ack2)}
	if got:=p.RecoveryStats().Commits;got!=1{t.Fatalf("duplicate COMMIT incremented commits=%d",got)}
	finalCtl:=proveFinalizationForTest(t,p,ctl)
	if err:=finalizeRecoveryAcceptedForTest(p,ctx,finalCtl);err!=nil{t.Fatal(err)}
	before:=len(out.Bytes())
	if _,err:=p.PublishRecoveryCommit(ctl);err!=nil{t.Fatal(err)}
	if got:=len(out.Bytes());got!=before{t.Fatalf("ACK retry caused replay bytes before=%d after=%d",before,got)}
	t.Log("PASS lost COMMIT_ACK retry resolved idempotently")
}

func TestPlanDigestMismatchRejectsCommit(t *testing.T){
	p,_,cancel,_,ctl:=prepareCommitFixture(t,"candidate-digest");defer cancel()
	bad:=ctl;bad.PlanDigest="00"+ctl.PlanDigest[2:]
	if bad.PlanDigest==ctl.PlanDigest{bad.PlanDigest="ff"+ctl.PlanDigest[2:]}
	res,err:=p.PublishRecoveryCommit(bad)
	if !errors.Is(err,recovery.ErrStateMismatch)&&!errors.Is(err,recovery.ErrNotPrepared){t.Fatalf("digest mismatch err=%v result=%+v",err,res)}
	if res.Committed||p.RecoveryEpoch()!=1||p.RecoveryOwner()!="carrier-1"{t.Fatalf("digest mismatch changed authority result=%+v epoch=%d owner=%s",res,p.RecoveryEpoch(),p.RecoveryOwner())}
	p.AbortRecovery("candidate-digest")
	t.Log("PASS plan digest mismatch rejected before commit")
}

func resolveCommittedForTest(t *testing.T,p *Peer,ctx context.Context,ctl RecoveryControl) {
	t.Helper()
	reply:=ctl;reply.Phase=RecoveryPhaseStatusReply;reply.Status=RecoveryResolutionCommitted
	if err:=p.NoteCommittedResolution(reply);err!=nil{t.Fatal(err)}
	var rebound bytes.Buffer
	if err:=p.RebindCommittedCarrier(ctx,ctl,Carrier{In:bytes.NewReader(nil),Out:&rebound});err!=nil{t.Fatal(err)}
	p.recovery.postCommitFault=nil
	finalCtl:=ctl;finalCtl.Phase=RecoveryPhaseFinalize
	if p.RecoveryTransactionState()!=RecoveryTxnFinalized{
		if err:=p.MarkFinalizationStarted(finalCtl);err!=nil{t.Fatal(err)}
		if err:=p.CompleteRecoveryFinalization(finalCtl);err!=nil{t.Fatal(err)}
	}
	if err:=finalizeRecoveryAcceptedForTest(p,ctx,finalCtl);err!=nil{t.Fatal(err)}
}

func TestPostCommitReplayFailureNeverReauthorizesOldEpoch(t *testing.T){
	p,ctx,cancel,_,ctl:=prepareCommitFixture(t,"candidate-postfail");defer cancel()
	injected:=errors.New("replay writer injected failure")
	fired:=false
	p.recovery.postCommitFault=func(stage string)error{
		if stage=="replay_write"&&!fired{fired=true;return injected}
		return nil
	}
	res,err:=p.CommitPreparedRecovery(ctx,ctl)
	if !res.Committed||!errors.Is(err,ErrPostCommitFailure){t.Fatalf("result=%+v err=%v",res,err)}
	if p.RecoveryEpoch()!=2||p.RecoveryOwner()!="candidate-postfail"{t.Fatalf("authority epoch=%d owner=%s",p.RecoveryEpoch(),p.RecoveryOwner())}
	if p.recovery.engine.Authorize(1,"carrier-1"){t.Fatal("old epoch re-authorized after post-commit failure")}
	p.AbortRecovery("candidate-postfail")
	if p.RecoveryEpoch()!=2||p.RecoveryOwner()!="candidate-postfail"{t.Fatal("Abort pretended to roll back committed authority")}
	if !p.RecoveryFrozen()||p.RecoveryTransactionState()!=RecoveryTxnFinalized{t.Fatalf("post-finalization delivery state=%s frozen=%v",p.RecoveryTransactionState(),p.RecoveryFrozen())}
	if _,err:=p.BeginRecovery("fresh-before-resolution");!errors.Is(err,ErrCommitUncertain){t.Fatalf("fresh recovery bypassed uncertainty: %v",err)}
	resolveCommittedForTest(t,p,ctx,ctl)
	if p.RecoveryFrozen(){t.Fatal("resolved transaction remained permanently frozen")}
	s:=p.RecoveryStats()
	if s.Commits!=1||s.PostCommitFailures!=1||s.Aborts!=0{t.Fatalf("metrics=%+v",s)}
	t.Log("PASS post-commit replay failure fenced old epoch until exact committed resolution finalized")
}

func TestPostCommitFailureCanRecoverToNextEpoch(t *testing.T){
	p,ctx,cancel,_,ctl:=prepareCommitFixture(t,"candidate-2");defer cancel()
	fired:=false
	p.recovery.postCommitFault=func(stage string)error{
		if stage=="replay_write"&&!fired{fired=true;return errors.New("candidate-2 died")}
		return nil
	}
	res,err:=p.CommitPreparedRecovery(ctx,ctl)
	if !res.Committed||!errors.Is(err,ErrPostCommitFailure){t.Fatalf("first result=%+v err=%v",res,err)}
	if _,err:=p.BeginRecovery("candidate-3");!errors.Is(err,ErrCommitUncertain){t.Fatalf("candidate-3 started before resolution: %v",err)}
	resolveCommittedForTest(t,p,ctx,ctl)
	_,peer:=peerOfferFor(t,p,"candidate-3")
	if err:=p.ReconcileRecovery("candidate-3",peer);err!=nil{t.Fatal(err)}
	var out bytes.Buffer
	res,err=commitRecoveryAcceptedForTest(p,ctx,"candidate-3",Carrier{In:bytes.NewReader(nil),Out:&out})
	if err!=nil{t.Fatal(err)}
	if !res.Committed||res.Epoch!=3||p.RecoveryEpoch()!=3||p.RecoveryOwner()!="candidate-3"{t.Fatalf("next recovery result=%+v epoch=%d owner=%s",res,p.RecoveryEpoch(),p.RecoveryOwner())}
	if p.recovery.engine.Authorize(1,"carrier-1")||p.recovery.engine.Authorize(2,"candidate-2"){t.Fatal("older epochs authorized after epoch-3 recovery")}
	t.Log("PASS post-commit uncertainty resolved exact epoch 2 before forward recovery to epoch 3")
}

func TestPostCommitFailureDoesNotLeaveSessionFrozen(t *testing.T){
	p,ctx,cancel,_,ctl:=prepareCommitFixture(t,"candidate-freeze-cleanup");defer cancel()
	p.recovery.postCommitFault=func(stage string)error{if stage=="after_authority_commit"{return errors.New("after commit")};return nil}
	res,err:=p.CommitPreparedRecovery(ctx,ctl)
	if !res.Committed||!errors.Is(err,ErrPostCommitFailure){t.Fatalf("result=%+v err=%v",res,err)}
	if !p.RecoveryFrozen(){t.Fatal("uncertain committed transaction was not frozen")}
	if _,err:=p.BeginRecovery("candidate-next");!errors.Is(err,ErrCommitUncertain){t.Fatalf("fresh recovery bypassed uncertainty: %v",err)}
	resolveCommittedForTest(t,p,ctx,ctl)
	if p.RecoveryFrozen(){t.Fatal("session remained frozen after committed resolution/finalize")}
	next,err:=p.BeginRecovery("candidate-next");if err!=nil{t.Fatalf("new recovery blocked after resolution: %v",err)}
	if next.NextEpoch!=3{t.Fatalf("next epoch=%d",next.NextEpoch)}
	p.AbortRecovery("candidate-next")
}

func TestRecoveryCommitMetricsReflectAuthorityChange(t *testing.T){
	p,ctx,cancel,_,ctl:=prepareCommitFixture(t,"candidate-metrics");defer cancel()
	p.recovery.postCommitFault=func(stage string)error{if stage=="after_authority_commit"{return errors.New("post commit metric fault")};return nil}
	res,err:=p.CommitPreparedRecovery(ctx,ctl)
	if !res.Committed||!errors.Is(err,ErrPostCommitFailure){t.Fatalf("result=%+v err=%v",res,err)}
	s:=p.RecoveryStats()
	if s.Commits!=1||s.PostCommitFailures!=1||s.CurrentEpoch!=2{t.Fatalf("metrics do not reflect authority change: %+v",s)}
	if s.Failures["post_commit_failure"]!=1{t.Fatalf("failure reasons=%v",s.Failures)}
	t.Log("PASS commit metric records authority publish independently of post-commit failure")
}


func makeUncertainFixture(t *testing.T,candidate string)(*Peer,context.Context,context.CancelFunc,RecoveryControl){
	t.Helper()
	p,ctx,cancel,_,ctl:=prepareCommitFixture(t,candidate)
	if err:=p.MarkCommitSent(ctl);err!=nil{cancel();t.Fatal(err)}
	if err:=p.MarkCommitUncertain(ctl);err!=nil{cancel();t.Fatal(err)}
	return p,ctx,cancel,ctl
}

func statusReplyFor(ctl RecoveryControl,status RecoveryResolutionStatus) RecoveryControl {
	r:=ctl
	r.Phase=RecoveryPhaseStatusReply
	r.Status=status
	return r
}

func TestCommitUncertainCannotAbortWithoutProof(t *testing.T){
	p,_,cancel,ctl:=makeUncertainFixture(t,"candidate-uncertain-abort");defer cancel()
	p.AbortRecovery(ctl.CandidateID)
	if p.RecoveryEpoch()!=1||p.RecoveryOwner()!="carrier-1"{t.Fatalf("uncertain abort changed authority epoch=%d owner=%s",p.RecoveryEpoch(),p.RecoveryOwner())}
	if p.RecoveryTransactionState()!=RecoveryTxnUncertain||!p.RecoveryFrozen(){t.Fatalf("state=%s frozen=%v",p.RecoveryTransactionState(),p.RecoveryFrozen())}
	t.Log("PASS COMMIT_UNCERTAIN cannot be rolled back without proof")
}

func TestCommittedResolutionCatchesUpExactTransaction(t *testing.T){
	p,ctx,cancel,ctl:=makeUncertainFixture(t,"candidate-catchup");defer cancel()
	reply:=statusReplyFor(ctl,RecoveryResolutionCommitted)
	if err:=p.NoteCommittedResolution(reply);err!=nil{t.Fatal(err)}
	if err:=p.NoteCommittedResolution(reply);err!=nil{t.Fatal(err)}
	var out bytes.Buffer
	if err:=p.RebindPreparedRecovery(ctx,ctl,Carrier{In:bytes.NewReader(nil),Out:&out});err!=nil{t.Fatal(err)}
	res,err:=p.PublishRecoveryCommit(ctl);if err!=nil{t.Fatal(err)}
	if !res.Committed||res.Epoch!=ctl.NextEpoch{t.Fatalf("result=%+v",res)}
	if p.RecoveryEpoch()!=2||p.RecoveryOwner()!=ctl.CandidateID{t.Fatalf("authority=%d/%s",p.RecoveryEpoch(),p.RecoveryOwner())}
	finalCtl:=proveFinalizationForTest(t,p,ctl)
	if err:=finalizeRecoveryAcceptedForTest(p,ctx,finalCtl);err!=nil{t.Fatal(err)}
	s:=p.RecoveryStats()
	if s.Commits!=1||s.ResolutionCommitted!=1{t.Fatalf("metrics=%+v",s)}
	if p.RecoveryFrozen(){t.Fatal("catch-up remained frozen")}
	t.Log("PASS committed proof caught up exact transaction identity once")
}

func TestNotCommittedResolutionAllowsAbort(t *testing.T){
	p,_,cancel,ctl:=makeUncertainFixture(t,"candidate-not-committed");defer cancel()
	reply:=statusReplyFor(ctl,RecoveryResolutionNotCommitted)
	if err:=p.ResolveNotCommitted(reply);err!=nil{t.Fatal(err)}
	if p.RecoveryEpoch()!=1||p.RecoveryOwner()!="carrier-1"{t.Fatalf("authority=%d/%s",p.RecoveryEpoch(),p.RecoveryOwner())}
	if p.RecoveryFrozen()||p.RecoveryTransactionState()!=RecoveryTxnAborted{t.Fatalf("state=%s frozen=%v",p.RecoveryTransactionState(),p.RecoveryFrozen())}
	next,err:=p.BeginRecovery("candidate-after-proof");if err!=nil{t.Fatal(err)}
	if next.NextEpoch!=2{t.Fatalf("next epoch=%d",next.NextEpoch)}
	p.AbortRecovery("candidate-after-proof")
	t.Log("PASS authenticated NOT_COMMITTED proof permits abort and fresh recovery")
}

func TestConflictingResolutionFailsClosed(t *testing.T){
	p,_,cancel,ctl:=makeUncertainFixture(t,"candidate-conflict");defer cancel()
	reply:=statusReplyFor(ctl,RecoveryResolutionConflict)
	if err:=p.NoteResolutionConflict(reply);!errors.Is(err,ErrCommitUncertain){t.Fatalf("conflict err=%v",err)}
	if p.RecoveryTransactionState()!=RecoveryTxnUncertain||!p.RecoveryFrozen(){t.Fatalf("state=%s frozen=%v",p.RecoveryTransactionState(),p.RecoveryFrozen())}
	if _,err:=p.BeginRecovery("candidate-bypass");!errors.Is(err,ErrCommitUncertain){t.Fatalf("fresh recovery bypass err=%v",err)}
	t.Log("PASS conflict/unknown resolution fails closed")
}

func TestUncertainBlocksFreshRecovery(t *testing.T){
	p,_,cancel,_:=makeUncertainFixture(t,"candidate-block");defer cancel()
	if _,err:=p.BeginRecovery("candidate-new");!errors.Is(err,ErrCommitUncertain){t.Fatalf("fresh recovery err=%v",err)}
	if p.RecoveryEpoch()!=1||p.RecoveryOwner()!="carrier-1"{t.Fatal("blocked recovery changed authority")}
}

func TestDuplicateResolutionIsIdempotent(t *testing.T){
	p,_,cancel,ctl:=makeUncertainFixture(t,"candidate-resolution-idem");defer cancel()
	reply:=statusReplyFor(ctl,RecoveryResolutionNotCommitted)
	if err:=p.ResolveNotCommitted(reply);err!=nil{t.Fatal(err)}
	if err:=p.ResolveNotCommitted(reply);err!=nil{t.Fatal(err)}
	s:=p.RecoveryStats()
	if s.ResolutionNotCommitted!=1||s.Aborts!=1{t.Fatalf("duplicate NOT_COMMITTED changed counters: %+v",s)}
	if p.RecoveryEpoch()!=1||p.RecoveryOwner()!="carrier-1"{t.Fatal("duplicate resolution changed authority")}
	t.Log("PASS duplicate status resolution is idempotent")
}

func TestFinalizeRetryIsIdempotent(t *testing.T){
	p,ctx,cancel,_,ctl:=prepareCommitFixture(t,"candidate-finalize-retry");defer cancel()
	fl,_:=p.getOpenFlow(1)
	fl.mu.Lock()
	fl.replay=[]replayChunk{
		{start:0,end:2,data:[]byte{1,2}},
		{start:2,end:4,data:[]byte{3,4}},
	}
	fl.mu.Unlock()
	// Re-materialize after changing the replay chunk boundaries.
	p.recovery.mu.Lock();p.recovery.prepared=nil;p.recovery.txnState=RecoveryTxnPreparing;p.recovery.mu.Unlock()
	var first bytes.Buffer
	prepared,err:=p.PrepareRecoveryCommit(ctx,ctl.CandidateID,Carrier{In:bytes.NewReader(nil),Out:&first});if err!=nil{t.Fatal(err)}
	if err:=p.MarkRecoveryCommitReady(prepared);err!=nil{t.Fatal(err)}
	ctl=prepared;ctl.Phase=RecoveryPhaseCommit
	calls:=0
	p.recovery.postCommitFault=func(stage string)error{
		if stage=="replay_write"{calls++;if calls==2{return errors.New("fail after replay prefix")}}
		return nil
	}
	// This test intentionally withholds peer acceptance for the first written
	// replay prefix so the exact transaction must conservatively retransmit it.
	p.recovery.peerAcceptanceTestHook=nil
	res,err:=p.CommitPreparedRecovery(ctx,ctl)
	if !res.Committed||!errors.Is(err,ErrPostCommitFailure){t.Fatalf("result=%+v err=%v",res,err)}
	reply:=statusReplyFor(ctl,RecoveryResolutionCommitted)
	if err:=p.NoteCommittedResolution(reply);err!=nil{t.Fatal(err)}
	p.recovery.postCommitFault=nil
	p.recovery.peerAcceptanceTestHook=func(fl *flow,end uint64){ _ = fl.onAck(end) }
	var second bytes.Buffer
	if err:=p.RebindCommittedCarrier(ctx,ctl,Carrier{In:bytes.NewReader(nil),Out:&second});err!=nil{t.Fatal(err)}
	if err:=p.FinalizeRecoveryCommit(ctx,ctl);err!=nil{t.Fatal(err)}
	if err:=p.FinalizeRecoveryCommit(ctx,ctl);err!=nil{t.Fatal(err)}
	var app []byte
	var accepted uint64
	var wireBytes uint64
	for _,buf:=range []*bytes.Buffer{&first,&second}{
		for buf.Len()>0{
			fr,e:=protocol.Decode(buf);if e!=nil{t.Fatal(e)}
			if fr.Type!=protocol.TypeData{continue}
			wireBytes+=uint64(len(fr.Payload))
			end:=fr.Offset+uint64(len(fr.Payload))
			if fr.Offset>accepted{t.Fatalf("wire replay gap offset=%d accepted=%d",fr.Offset,accepted)}
			if end<=accepted{continue}
			skip:=uint64(0)
			if fr.Offset<accepted{skip=accepted-fr.Offset}
			app=append(app,fr.Payload[skip:]...)
			accepted=end
		}
	}
	if !bytes.Equal(app,[]byte{1,2,3,4}){t.Fatalf("finalize retry duplicated/lost application data=%v",app)}
	if wireBytes!=6{t.Fatalf("expected conservative wire retry bytes=6 got=%d",wireBytes)}
	if p.RecoveryStats().ReplayedBytes!=wireBytes{t.Fatalf("replayed wire metric=%d want=%d",p.RecoveryStats().ReplayedBytes,wireBytes)}
	t.Log("PASS finalize retry conservatively retransmitted unacked prefix while application offsets deduped exact bytes")
}


func TestReplacementWaitRequiresApplicationReadyFrontier(t *testing.T){
	p,_,_,cancel:=recoveryFixture(t,1);defer cancel()
	p.mu.Lock()
	p.carrierEpoch=2
	p.carrierID="carrier-2"
	p.carrierGeneration=2
	p.mu.Unlock()

	waitCtx,waitCancel:=context.WithTimeout(context.Background(),20*time.Millisecond)
	defer waitCancel()
	err:=p.waitForReplacement(waitCtx,1,"carrier-1",1)
	if !errors.Is(err,context.DeadlineExceeded){
		t.Fatalf("physical carrier attachment released application pump before replay activation: %v",err)
	}

	if !p.publishReplacementReady(2){t.Fatal("generation 2 readiness was not published")}
	readyCtx,readyCancel:=context.WithTimeout(context.Background(),time.Second)
	defer readyCancel()
	if err:=p.waitForReplacement(readyCtx,1,"carrier-1",1);err!=nil{
		t.Fatalf("application-ready frontier did not release replacement waiter: %v",err)
	}
}


func TestInitialFinalizePublishesActivatedGeneration(t *testing.T){
	p,ctx,cancel,_,ctl:=prepareCommitFixture(t,"candidate-generation-2")
	defer cancel()
	p.replacementMu.Lock();beforeReady:=p.replacementReadyGeneration;p.replacementMu.Unlock()
	if beforeGen:=p.RecoveryCarrierGeneration();beforeGen!=1||beforeReady!=1{
		t.Fatalf("unexpected initial generations carrier=%d ready=%d",beforeGen,beforeReady)
	}
	if _,err:=p.PublishRecoveryCommit(ctl);err!=nil{t.Fatal(err)}
	finalCtl:=proveFinalizationForTest(t,p,ctl)
	if err:=finalizeRecoveryAcceptedForTest(p,ctx,finalCtl);err!=nil{t.Fatal(err)}
	p.replacementMu.Lock();afterReady:=p.replacementReadyGeneration;p.replacementMu.Unlock()
	afterGen:=p.RecoveryCarrierGeneration()
	if afterGen!=2||afterReady!=2{
		t.Fatalf("initial finalized recovery did not publish exact ready generation carrier=%d ready=%d",afterGen,afterReady)
	}
	waitCtx,waitCancel:=context.WithTimeout(context.Background(),time.Second);defer waitCancel()
	if err:=p.waitForReplacement(waitCtx,1,"carrier-1",1);err!=nil{
		t.Fatalf("generation-1 waiter remained blocked after finalized generation-2 recovery: %v",err)
	}
	t.Logf("PASS initial finalize generation carrier 1->%d ready 1->%d",afterGen,afterReady)
}

func TestReplacementReadyGenerationMonotonicAcrossRebind(t *testing.T){
	p,ctx,cancel,_,ctl:=prepareCommitFixture(t,"candidate-generation-rebind")
	defer cancel()
	if _,err:=p.PublishRecoveryCommit(ctl);err!=nil{t.Fatal(err)}
	finalCtl:=proveFinalizationForTest(t,p,ctl)
	if err:=finalizeRecoveryAcceptedForTest(p,ctx,finalCtl);err!=nil{t.Fatal(err)}

	var rebound bytes.Buffer
	if err:=p.RebindCommittedCarrier(ctx,ctl,Carrier{In:bytes.NewReader(nil),Out:&rebound});err!=nil{t.Fatal(err)}
	if err:=finalizeRecoveryAcceptedForTest(p,ctx,finalCtl);err!=nil{t.Fatal(err)}
	p.replacementMu.Lock();ready:=p.replacementReadyGeneration;p.replacementMu.Unlock()
	gen:=p.RecoveryCarrierGeneration()
	if gen!=3||ready!=3{t.Fatalf("rebind readiness mismatch carrier=%d ready=%d",gen,ready)}
	t.Logf("PASS ready generation monotonic 1->2->%d",ready)
}

func TestStaleReplacementGenerationCannotRegressOrWakeNewerState(t *testing.T){
	p,_,_,cancel:=recoveryFixture(t,1);defer cancel()
	p.mu.Lock();p.carrierGeneration=3;p.carrierEpoch=3;p.carrierID="carrier-3";sender:=p.sender;p.mu.Unlock()
	p.replacementMu.Lock();p.replacementReadyGeneration=3;p.replacementMu.Unlock()

	if p.publishReplacementReady(2){t.Fatal("stale generation published readiness")}
	p.replacementMu.Lock();ready:=p.replacementReadyGeneration;p.replacementMu.Unlock()
	if ready!=3{t.Fatalf("stale readiness regressed generation to %d",ready)}
	if p.onCarrierFailureForGeneration(errors.New("late generation-2 failure"),2){
		t.Fatal("stale carrier failure was accepted")
	}
	if sender!=nil&&sender.isStopped(){t.Fatal("stale carrier failure stopped generation-3 sender")}
	if got:=p.RecoveryCarrierGeneration();got!=3{t.Fatalf("stale event changed current carrier generation=%d",got)}

	before:=p.RecoveryStats()
	ctl:=RecoveryControl{SessionID:p.SessionID(),CandidateID:"carrier-2",NextEpoch:2,PlanDigest:"stale-generation-2"}
	res,err:=p.markPostCommitFailureForGeneration(errors.New("late post-commit generation-2 failure"),ctl,2)
	if !res.Committed||!errors.Is(err,ErrPostCommitFailure){t.Fatalf("stale post-commit result=%+v err=%v",res,err)}
	after:=p.RecoveryStats()
	if after.PostCommitFailures!=before.PostCommitFailures{t.Fatalf("stale failure changed post-commit metrics before=%+v after=%+v",before,after)}
	if sender!=nil&&sender.isStopped(){t.Fatal("stale post-commit failure stopped generation-3 sender")}
	if got:=p.RecoveryCarrierGeneration();got!=3{t.Fatalf("stale post-commit failure changed generation=%d",got)}
}

func TestWakeWithoutReadinessAdvanceDoesNotReleaseReplacement(t *testing.T){
	p,_,_,cancel:=recoveryFixture(t,1);defer cancel()
	p.mu.Lock();p.carrierGeneration=2;p.carrierEpoch=2;p.carrierID="carrier-2";p.mu.Unlock()
	p.replacementMu.Lock()
	if p.replacementReadyGeneration!=1{p.replacementMu.Unlock();t.Fatalf("unexpected initial ready generation=%d",p.replacementReadyGeneration)}
	close(p.replacementWait)
	p.replacementWait=make(chan struct{})
	p.replacementMu.Unlock()

	waitCtx,waitCancel:=context.WithTimeout(context.Background(),20*time.Millisecond)
	err:=p.waitForReplacement(waitCtx,1,"carrier-1",1)
	waitCancel()
	if !errors.Is(err,context.DeadlineExceeded){
		t.Fatalf("wake without readiness advancement incorrectly released waiter: %v",err)
	}

	if !p.publishReplacementReady(2){t.Fatal("generation 2 readiness not published")}
	readyCtx,readyCancel:=context.WithTimeout(context.Background(),time.Second);defer readyCancel()
	if err:=p.waitForReplacement(readyCtx,1,"carrier-1",1);err!=nil{
		t.Fatalf("explicit generation readiness did not release waiter: %v",err)
	}
}


func TestRecoveryRestoresAuthenticatedPeerCreditFrontier(t *testing.T){
	p,_,ctx,cancel:=recoveryFixture(t,1);defer cancel()
	fl,_:=p.getOpenFlow(1)
	fl.mu.Lock()
	fl.peerMax=fl.txNext // sender is exactly credit-exhausted on the failed carrier
	oldWait:=fl.creditWait
	before:=fl.peerMax
	fl.mu.Unlock()

	_,peer:=peerOfferFor(t,p,"carrier-credit-2")
	if peer.Snapshot.Flows[0].RxCredit<=before{t.Fatalf("fixture peer credit=%d before=%d",peer.Snapshot.Flows[0].RxCredit,before)}
	if err:=p.ReconcileRecovery("carrier-credit-2",peer);err!=nil{t.Fatal(err)}
	var out bytes.Buffer
	if _,err:=commitRecoveryAcceptedForTest(p,ctx,"carrier-credit-2",Carrier{In:bytes.NewReader(nil),Out:&out});err!=nil{t.Fatal(err)}

	fl.mu.Lock();after:=fl.peerMax;fl.mu.Unlock()
	if after!=peer.Snapshot.Flows[0].RxCredit{
		t.Fatalf("authenticated peer credit was not restored: before=%d after=%d want=%d",before,after,peer.Snapshot.Flows[0].RxCredit)
	}
	select{
	case <-oldWait:
	default:
		t.Fatal("credit-exhausted sender waiter was not released by recovered peer credit")
	}
	t.Logf("PASS recovery restored peer credit frontier %d->%d",before,after)
}


func TestRecoveryPeerCreditNeverRegressesPastNewerOldCarrierWindow(t *testing.T){
	p,_,ctx,cancel:=recoveryFixture(t,1);defer cancel()
	fl,_:=p.getOpenFlow(1)
	fl.mu.Lock()
	fl.peerMax=32
	fl.mu.Unlock()

	_,peer:=peerOfferFor(t,p,"carrier-credit-monotonic")
	peer.Snapshot.Flows[0].RxCredit=64
	if err:=p.ReconcileRecovery("carrier-credit-monotonic",peer);err!=nil{t.Fatal(err)}

	var out bytes.Buffer
	ctl,err:=p.PrepareRecoveryCommit(ctx,"carrier-credit-monotonic",Carrier{In:bytes.NewReader(nil),Out:&out})
	if err!=nil{t.Fatal(err)}

	// Simulate a later authenticated WINDOW arriving on the still-authoritative
	// old carrier after the immutable transaction snapshot has already been
	// materialized into prepared recovery actions.
	if err:=fl.onWindow(96);err!=nil{t.Fatal(err)}

	ctl.Phase=RecoveryPhaseCommit
	if _,err:=p.CommitPreparedRecovery(ctx,ctl);err!=nil{t.Fatal(err)}
	fl.mu.Lock();got:=fl.peerMax;fl.mu.Unlock()
	if got!=96{t.Fatalf("recovery regressed newer peer credit: got=%d want=96",got)}
	t.Logf("PASS immutable recovery credit=64 did not regress newer authoritative WINDOW=96")
}
