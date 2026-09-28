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
	if _,err:=p.CommitRecovery(ctx,"carrier-2",Carrier{In:bytes.NewReader(nil),Out:&next});err!=nil{t.Fatal(err)}
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
	if _,err:=p.CommitRecovery(ctx,"candidate-A",Carrier{In:bytes.NewReader(nil),Out:&out});err!=nil{t.Fatal(err)}
	if p.RecoveryOwner()!="candidate-A"||p.recovery.engine.Authorize(2,"candidate-B"){t.Fatal("competing candidate authorized")}
}

func TestRecoveryDuringFINPreservesCloseSemantics(t *testing.T){
	p,_,ctx,cancel:=recoveryFixture(t,1);defer cancel()
	_,peer:=peerOfferFor(t,p,"carrier-2")
	peer.Snapshot.Flows[0].FinRecv=true
	peer.Snapshot.Flows[0].FinAckSent=true
	if err:=p.ReconcileRecovery("carrier-2",peer);err!=nil{t.Fatal(err)}
	var out bytes.Buffer
	if _,err:=p.CommitRecovery(ctx,"carrier-2",Carrier{In:bytes.NewReader(nil),Out:&out});err!=nil{t.Fatal(err)}
	fl,_:=p.getOpenFlow(1)
	fl.mu.Lock();sent,acked,closed:=fl.finSent,fl.finAcked,fl.closed;fl.mu.Unlock()
	if !sent||!acked||closed{t.Fatalf("FIN state sent=%v acked=%v closed=%v",sent,acked,closed)}
}

func TestMultiFlowCarrierReplacementNoDuplicateOrLoss(t *testing.T){
	p,_,ctx,cancel:=recoveryFixture(t,8);defer cancel()
	_,peer:=peerOfferFor(t,p,"carrier-2")
	if err:=p.ReconcileRecovery("carrier-2",peer);err!=nil{t.Fatal(err)}
	var out bytes.Buffer
	if _,err:=p.CommitRecovery(ctx,"carrier-2",Carrier{In:bytes.NewReader(nil),Out:&out});err!=nil{t.Fatal(err)}
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
	if _,err:=p.CommitRecovery(ctx,"carrier-finack",Carrier{In:bytes.NewReader(nil),Out:&out});err!=nil{t.Fatal(err)}
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
