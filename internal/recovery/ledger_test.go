package recovery

import (
	"errors"
	"testing"
)

func baseSnapshots()(Snapshot,Snapshot){
	local:=Snapshot{SessionID:"s",BootID:"local-boot",Epoch:9,Flows:[]FlowSnapshot{{
		StreamID:1,OpenNonce:"n",TxNext:100,TxAcked:40,RxAccepted:80,RxDelivered:64,RxCredit:128,
	}}}
	peer:=Snapshot{SessionID:"s",BootID:"peer-boot",Epoch:9,Flows:[]FlowSnapshot{{
		StreamID:1,OpenNonce:"n",TxNext:120,TxAcked:64,RxAccepted:72,RxDelivered:70,RxCredit:134,
	}}}
	return local,peer
}

func TestReconcileRecoversLostACKWithoutInventingBytes(t *testing.T){
	local,peer:=baseSnapshots()
	p,err:=Reconcile(local,peer,"peer-boot");if err!=nil{t.Fatal(err)}
	if len(p.Flows)!=1{t.Fatalf("flows=%d",len(p.Flows))}
	f:=p.Flows[0]
	if f.LocalAckAdvanceTo!=72||f.LocalReplayFrom!=72{t.Fatalf("local plan=%#v",f)}
	if f.PeerAckAdvanceTo!=80||f.PeerReplayFrom!=80{t.Fatalf("peer plan=%#v",f)}
}

func TestReconcileRejectsPeerClaimBeyondLocalTx(t *testing.T){
	local,peer:=baseSnapshots();peer.Flows[0].RxAccepted=101
	if _,err:=Reconcile(local,peer,"peer-boot");!errors.Is(err,ErrStateMismatch){t.Fatalf("err=%v",err)}
}

func TestReconcileRejectsPeerRestart(t *testing.T){
	local,peer:=baseSnapshots()
	if _,err:=Reconcile(local,peer,"different-boot");!errors.Is(err,ErrPeerRestarted){t.Fatalf("err=%v",err)}
}

func TestReconcileRejectsFlowIdentityMismatch(t *testing.T){
	local,peer:=baseSnapshots();peer.Flows[0].OpenNonce="other"
	if _,err:=Reconcile(local,peer,"peer-boot");!errors.Is(err,ErrStateMismatch){t.Fatalf("err=%v",err)}
}

func TestReconcileCanRecoverLostFinAck(t *testing.T){
	local,peer:=baseSnapshots()
	local.Flows[0].FinSent=true
	peer.Flows[0].FinRecv=true
	peer.Flows[0].FinAckSent=true
	p,err:=Reconcile(local,peer,"peer-boot");if err!=nil{t.Fatal(err)}
	if !p.Flows[0].LocalFinAckCanAdvance{t.Fatal("lost FIN_ACK was not recoverable from correlated peer state")}
}
