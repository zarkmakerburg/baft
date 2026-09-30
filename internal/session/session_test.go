package session

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/protocol"
	"github.com/zarkmakerburg/baft/internal/resources"
	"github.com/zarkmakerburg/baft/internal/routes"
)

type callbackWriter struct {
	called bool
	fn func()
}

func (w *callbackWriter) Write(p []byte) (int,error) {
	if !w.called {
		w.called=true
		if w.fn!=nil { w.fn() }
	}
	return len(p),nil
}

func TestFinAckConfirmCanArriveBeforeFinAckWriteReturns(t *testing.T) {
	w:=&callbackWriter{}
	p,err:=New(Dialer,Carrier{In:bytes.NewReader(nil),Out:w},"urn:baft:node:ex-01",nil,Options{
		NodeID:"ir-01",ExpectedPeerNodeID:"ex-01",ProfileID:"secure-fast",ProfileVersion:1,ConfigRevision:"test",
	})
	if err!=nil{t.Fatal(err)}
	fl:=newFlow(1,"main","00112233445566778899aabbccddeeff",nil,p.allocator)
	fl.openOK=true
	fl.finRecv=true
	fl.finRecvFinal=0
	fl.rxNext=0
	fl.rxWritten=0
	p.mu.Lock();p.flows[fl.id]=fl;p.mu.Unlock()

	var confirmErr error
	w.fn=func(){confirmErr=fl.onFinAckConfirm(0)}
	if err:=p.ackRemoteFin(fl);err!=nil{t.Fatalf("FIN_ACK send: %v",err)}
	if confirmErr!=nil{t.Fatalf("peer FIN_ACK_CONFIRM raced local send publication: %v",confirmErr)}
	fl.mu.Lock()
	sent,inFlight,confirmed:=fl.finAckSent,fl.finAckWriteInFlight,fl.finAckConfirmed
	fl.mu.Unlock()
	if !sent||inFlight||!confirmed{
		t.Fatalf("terminal proof state sent=%v in_flight=%v confirmed=%v",sent,inFlight,confirmed)
	}
}

func TestFlowRejectsAckPastTxNext(t *testing.T) {
	f:=newFlow(1,"main","00112233445566778899aabbccddeeff",nil)
	f.openOK=true
	f.txNext=10
	if err:=f.onAck(11);err==nil { t.Fatal("expected invalid ACK") }
	if err:=f.onAck(7);err!=nil { t.Fatal(err) }
	if f.txAcked!=7 { t.Fatalf("txAcked=%d",f.txAcked) }
	if err:=f.onAck(5);err!=nil { t.Fatal(err) }
	if f.txAcked!=7 { t.Fatalf("old ACK moved state backwards: %d",f.txAcked) }
}

func TestFlowDuplicateDataDoesNotEnterReceiveRingTwice(t *testing.T) {
	f:=newFlow(1,"main","00112233445566778899aabbccddeeff",nil)
	f.openOK=true; f.rxMax=100
	r,err:=newReceiveRing(100);if err!=nil{t.Fatal(err)}
	f.rxRing=r
	ack,dup,err:=f.acceptData(0,[]byte("abc"));if err!=nil||dup||ack!=3{t.Fatalf("first: ack=%d dup=%v err=%v",ack,dup,err)}
	ack,dup,err=f.acceptData(0,[]byte("abc"));if err!=nil||!dup||ack!=3{t.Fatalf("duplicate: ack=%d dup=%v err=%v",ack,dup,err)}
	ack,dup,err=f.acceptData(1,[]byte("bcXYZ"));if err!=nil||dup||ack!=6{t.Fatalf("overlap: ack=%d dup=%v err=%v",ack,dup,err)}
	if got:=r.Len();got!=6{t.Fatalf("ring bytes=%d want=6",got)}
	p,err:=r.Peek(context.Background(),100);if err!=nil{t.Fatal(err)}
	if string(p)!="abcXYZ"{t.Fatalf("ring=%q",p)}
}

func TestFlowRejectsWindowRegression(t *testing.T) {
	f:=newFlow(1,"main","00112233445566778899aabbccddeeff",nil);f.openOK=true
	if err:=f.onWindow(100);err!=nil{t.Fatal(err)}
	if err:=f.onWindow(99);err==nil{t.Fatal("expected backwards WINDOW rejection")}
}

func TestOpenIsIdempotentAndDoesNotRedial(t *testing.T) {
	tbl,err:=routes.New([]routes.Route{{ID:"main",Target:"127.0.0.1:2443",AllowedPeers:map[string]struct{}{"urn:baft:node:ir-01":{}}}});if err!=nil{t.Fatal(err)}
	var out bytes.Buffer
	p,err:=New(Listener,Carrier{In:bytes.NewReader(nil),Out:&out},"urn:baft:node:ir-01",tbl,Options{NodeID:"ex-01",ExpectedPeerNodeID:"ir-01",ProfileID:"secure-fast",ProfileVersion:1,ConfigRevision:"test"});if err!=nil{t.Fatal(err)}
	var dials atomic.Int32;var remote net.Conn
	p.dial=func(context.Context,string,string)(net.Conn,error){dials.Add(1);a,b:=net.Pipe();remote=b;return a,nil}
	req,_:=protocol.EncodeControl(protocol.OpenRequest{RouteID:"main",OpenNonce:"00112233445566778899aabbccddeeff"})
	fr:=protocol.Frame{Type:protocol.TypeOpen,StreamID:1,Payload:req}
	if err:=p.handleOpen(context.Background(),fr);err!=nil{t.Fatal(err)}
	if err:=p.handleOpen(context.Background(),fr);err!=nil{t.Fatal(err)}
	if got:=dials.Load();got!=1{t.Fatalf("dial count=%d",got)}
	if remote!=nil{_ = remote.Close()}
	p.closeAll();p.wg.Wait()
}

func TestResetClosesOnlyReferencedFlowAndRecordsFixedCode(t *testing.T) {
	var out bytes.Buffer
	p,err:=New(Dialer,Carrier{In:bytes.NewReader(nil),Out:&out},"urn:baft:node:ex-01",nil,Options{NodeID:"ir-01",ExpectedPeerNodeID:"ex-01",ProfileID:"secure-fast",ProfileVersion:1,ConfigRevision:"test"});if err!=nil{t.Fatal(err)}
	local,remote:=net.Pipe();defer remote.Close()
	fl:=newFlow(1,"main","00112233445566778899aabbccddeeff",local,p.allocator);fl.openOK=true
	p.mu.Lock();p.flows[1]=fl;p.localReady=true;p.peerReady=true;p.markReadyLocked();p.mu.Unlock()
	payload,err:=protocol.EncodeControl(protocol.Reset{Code:protocol.ErrorAdminDrain});if err!=nil{t.Fatal(err)}
	if err:=p.handleFrame(context.Background(),protocol.Frame{Type:protocol.TypeReset,StreamID:1,Payload:payload});err!=nil{t.Fatal(err)}
	if _,err:=p.getFlow(1);err==nil{t.Fatal("RESET must remove flow")}
	fl.mu.Lock();code,closed:=fl.resetCode,fl.closed;fl.mu.Unlock()
	if !closed||code!=protocol.ErrorAdminDrain{t.Fatalf("closed=%v code=%q",closed,code)}
}

func TestReplayReservationReleasedByAck(t *testing.T) {
	l:=resources.Limits{Total:128*1024,Receive:64*1024,Replay:64*1024,PerFlowReceive:64*1024,PerFlowReplay:64*1024}
	a,err:=resources.NewAllocator(l);if err!=nil{t.Fatal(err)}
	f:=newFlow(1,"main","00112233445566778899aabbccddeeff",nil,a);f.openOK=true
	if err:=f.onWindow(32*1024);err!=nil{t.Fatal(err)}
	cap,err:=f.reserveReadCapacity(context.Background(),32*1024);if err!=nil{t.Fatal(err)}
	if cap!=32*1024{t.Fatalf("capacity=%d",cap)}
	off,payload,err:=f.commitSend(make([]byte,cap));if err!=nil{t.Fatal(err)}
	if off!=0||len(payload)!=cap{t.Fatalf("off=%d len=%d",off,len(payload))}
	if s:=a.Snapshot();s.ReplayUsed!=32*1024{t.Fatalf("replay reservation=%d",s.ReplayUsed)}
	if err:=f.onAck(32*1024);err!=nil{t.Fatal(err)}
	if s:=a.Snapshot();s.ReplayUsed!=0{t.Fatalf("ACK did not release replay: %#v",s)}
}

func TestReceiveWindowUsesStableReservation(t *testing.T) {
	l:=resources.Limits{Total:256*1024,Receive:128*1024,Replay:128*1024,PerFlowReceive:128*1024,PerFlowReplay:128*1024}
	a,err:=resources.NewAllocator(l);if err!=nil{t.Fatal(err)}
	var out bytes.Buffer
	p,err:=New(Dialer,Carrier{In:bytes.NewReader(nil),Out:&out},"urn:baft:node:ex-01",nil,Options{NodeID:"ir-01",ExpectedPeerNodeID:"ex-01",Resources:a});if err!=nil{t.Fatal(err)}
	f:=newFlow(1,"main","00112233445566778899aabbccddeeff",nil,a);f.openOK=true
	if _,err:=p.reserveReceiveWindow(f);err!=nil{t.Fatal(err)}
	if s:=a.Snapshot();s.ReceiveUsed!=64*1024{t.Fatalf("initial reservation=%d",s.ReceiveUsed)}
	f.mu.Lock();f.rxWritten=32*1024;f.mu.Unlock()
	if _,err:=p.reserveReceiveWindow(f);err!=nil{t.Fatal(err)}
	if s:=a.Snapshot();s.ReceiveUsed!=64*1024{t.Fatalf("sliding window grew reservation: %d",s.ReceiveUsed)}
	f.close()
	if s:=a.Snapshot();s.TotalUsed!=0{t.Fatalf("flow close leaked reservation: %#v",s)}
}

func TestTWRLInvariantCreditTracksDeliveredNotAccepted(t *testing.T) {
	l:=resources.Limits{Total:256*1024,Receive:128*1024,Replay:128*1024,PerFlowReceive:128*1024,PerFlowReplay:128*1024}
	a,err:=resources.NewAllocator(l);if err!=nil{t.Fatal(err)}
	var out bytes.Buffer
	p,err:=New(Dialer,Carrier{In:bytes.NewReader(nil),Out:&out},"urn:baft:node:ex-01",nil,Options{NodeID:"ir-01",ExpectedPeerNodeID:"ex-01",Resources:a});if err!=nil{t.Fatal(err)}
	f:=newFlow(1,"main","00112233445566778899aabbccddeeff",nil,a);f.openOK=true
	initial,err:=p.reserveReceiveWindow(f);if err!=nil{t.Fatal(err)}
	if initial!=64*1024{t.Fatalf("initial credit=%d",initial)}
	ack,dup,err:=f.acceptData(0,make([]byte,32*1024));if err!=nil||dup{t.Fatalf("accept ack=%d dup=%v err=%v",ack,dup,err)}
	f.mu.Lock();accepted,delivered,credit:=f.rxNext,f.rxWritten,f.rxMax;f.mu.Unlock()
	if accepted!=32*1024||delivered!=0||credit!=64*1024{t.Fatalf("A=%d D=%d C=%d",accepted,delivered,credit)}
	if credit-delivered>uint64(f.receiveReserved){t.Fatal("credit exceeds reserved receive capacity")}
}

func TestTWRLTargetDrainAdvancesCreditTwiceWithoutCarrierWriteBlocking(t *testing.T) {
	l:=resources.Limits{Total:256*1024,Receive:128*1024,Replay:128*1024,PerFlowReceive:128*1024,PerFlowReplay:128*1024}
	a,err:=resources.NewAllocator(l);if err!=nil{t.Fatal(err)}
	var out bytes.Buffer
	p,err:=New(Dialer,Carrier{In:bytes.NewReader(nil),Out:&out},"urn:baft:node:ex-01",nil,Options{NodeID:"ir-01",ExpectedPeerNodeID:"ex-01",Resources:a});if err!=nil{t.Fatal(err)}
	local,remote:=net.Pipe()
	defer remote.Close()

	f:=newFlow(1,"main","00112233445566778899aabbccddeeff",local,a)
	f.openOK=true
	p.mu.Lock();p.flows[1]=f;p.mu.Unlock()
	if _,err:=p.reserveReceiveWindow(f);err!=nil{t.Fatal(err)}

	ctx,cancel:=context.WithCancel(context.Background())
	defer cancel()
	p.startTargetPump(ctx,f)

	chunk:=make([]byte,32*1024)
	if err:=p.handleData(f,protocol.Frame{Type:protocol.TypeData,StreamID:1,Offset:0,Payload:chunk});err!=nil{t.Fatal(err)}
	if err:=p.handleData(f,protocol.Frame{Type:protocol.TypeData,StreamID:1,Offset:32*1024,Payload:chunk});err!=nil{t.Fatal(err)}

	f.mu.Lock()
	if f.rxNext!=64*1024||f.rxWritten!=0||f.rxMax!=64*1024{
		a0,d0,c0:=f.rxNext,f.rxWritten,f.rxMax
		f.mu.Unlock()
		t.Fatalf("before drain A=%d D=%d C=%d",a0,d0,c0)
	}
	f.mu.Unlock()

	readDone:=make(chan error,1)
	go func(){
		buf:=make([]byte,64*1024)
		_,err:=io.ReadFull(remote,buf)
		readDone<-err
	}()

	deadline:=time.Now().Add(time.Second)
	for {
		f.mu.Lock()
		accepted,delivered,credit,reserved:=f.rxNext,f.rxWritten,f.rxMax,f.receiveReserved
		f.mu.Unlock()
		if delivered==64*1024 && credit==128*1024 {
			if accepted!=64*1024{t.Fatalf("accepted changed unexpectedly: %d",accepted)}
			if credit-delivered>uint64(reserved){t.Fatalf("credit invariant violated C=%d D=%d R=%d",credit,delivered,reserved)}
			break
		}
		if time.Now().After(deadline){t.Fatalf("target drain did not replenish two chunks: A=%d D=%d C=%d",accepted,delivered,credit)}
		time.Sleep(time.Millisecond)
	}
	if err:=<-readDone;err!=nil{t.Fatal(err)}
	cancel();f.close();p.removeFlow(1);p.wg.Wait()
}

func TestLateTerminalControlForKnownClosedFlowIsAbsorbed(t *testing.T) {
	var out bytes.Buffer
	p,err:=New(Dialer,Carrier{In:bytes.NewReader(nil),Out:&out},"urn:baft:node:ex-01",nil,Options{NodeID:"ir-01",ExpectedPeerNodeID:"ex-01"});if err!=nil{t.Fatal(err)}
	local,remote:=net.Pipe();defer remote.Close()
	f:=newFlow(1,"main","00112233445566778899aabbccddeeff",local,p.allocator);f.openOK=true
	p.mu.Lock();p.flows[1]=f;p.localReady=true;p.peerReady=true;p.markReadyLocked();p.mu.Unlock()
	f.close();p.removeFlow(1)

	resetPayload,err:=protocol.EncodeControl(protocol.Reset{Code:protocol.ErrorAdminDrain});if err!=nil{t.Fatal(err)}
	for _,fr:=range []protocol.Frame{
		{Type:protocol.TypeAck,StreamID:1,Offset:0},
		{Type:protocol.TypeWindow,StreamID:1,Offset:0},
		{Type:protocol.TypeFinAck,StreamID:1,Offset:0},
		{Type:protocol.TypeReset,StreamID:1,Payload:resetPayload},
	}{
		if err:=p.handleFrame(context.Background(),fr);err!=nil{t.Fatalf("late %v was not absorbed: %v",fr.Type,err)}
	}
	if err:=p.handleFrame(context.Background(),protocol.Frame{Type:protocol.TypeReset,StreamID:3,Payload:resetPayload});err==nil{
		t.Fatal("truly unknown stream must remain a protocol error")
	}
}

func TestInFlightDataAndFinAfterLocalResetAreAbsorbed(t *testing.T) {
	var out bytes.Buffer
	p,err:=New(Dialer,Carrier{In:bytes.NewReader(nil),Out:&out},"urn:baft:node:ex-01",nil,Options{NodeID:"ir-01",ExpectedPeerNodeID:"ex-01"});if err!=nil{t.Fatal(err)}
	local,remote:=net.Pipe();defer remote.Close()
	f:=newFlow(1,"main","00112233445566778899aabbccddeeff",local,p.allocator);f.openOK=true
	p.mu.Lock();p.flows[1]=f;p.localReady=true;p.peerReady=true;p.markReadyLocked();p.mu.Unlock()
	// e.g. the local application closed its socket mid-download; the peer keeps
	// sending until our RESET reaches it.
	if err:=p.sendReset(f,protocol.ErrorTargetUnreachable);err!=nil{t.Fatal(err)}

	for _,fr:=range []protocol.Frame{
		{Type:protocol.TypeData,StreamID:1,Offset:0,Payload:[]byte("in-flight")},
		{Type:protocol.TypeFin,StreamID:1,Offset:9},
	}{
		if err:=p.handleFrame(context.Background(),fr);err!=nil{t.Fatalf("in-flight %v after local RESET tore down the session: %v",fr.Type,err)}
	}
	if err:=p.handleFrame(context.Background(),protocol.Frame{Type:protocol.TypeData,StreamID:3,Payload:[]byte("x")});err==nil{
		t.Fatal("DATA for a truly unknown stream must remain a protocol error")
	}
}

func TestClosedFlowTombstonesAreBounded(t *testing.T) {
	var out bytes.Buffer
	p,err:=New(Dialer,Carrier{In:bytes.NewReader(nil),Out:&out},"urn:baft:node:ex-01",nil,Options{NodeID:"ir-01",ExpectedPeerNodeID:"ex-01"});if err!=nil{t.Fatal(err)}
	for i:=0;i<maxClosedFlowTombstones+32;i++{
		id:=uint64(i*2+1)
		f:=newFlow(id,"main","00112233445566778899aabbccddeeff",nil,p.allocator);f.openOK=true
		p.mu.Lock();p.flows[id]=f;p.mu.Unlock()
		f.close();p.removeFlow(id)
	}
	p.mu.Lock();n:=len(p.closedFlows);order:=len(p.closedOrder);p.mu.Unlock()
	if n!=maxClosedFlowTombstones||order!=maxClosedFlowTombstones{t.Fatalf("tombstones map=%d order=%d",n,order)}
	if p.isClosedFlow(1){t.Fatal("oldest tombstone should have been evicted")}
}
