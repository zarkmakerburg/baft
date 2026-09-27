package session

import (
	"bytes"
	"context"
	"net"
	"sync/atomic"
	"testing"

	"github.com/zarkmakerburg/baft/internal/protocol"
	"github.com/zarkmakerburg/baft/internal/resources"
	"github.com/zarkmakerburg/baft/internal/routes"
)

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
