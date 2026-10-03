package session

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/protocol"
	"github.com/zarkmakerburg/baft/internal/resources"
	"github.com/zarkmakerburg/baft/internal/routes"
)

// lockedBuffer collects frames written by background OPEN completions.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(p) }
func (l *lockedBuffer) reset() { l.mu.Lock(); l.b.Reset(); l.mu.Unlock() }

func (l *lockedBuffer) frames(t *testing.T) []protocol.Frame {
	t.Helper()
	l.mu.Lock()
	r := bytes.NewReader(append([]byte(nil), l.b.Bytes()...))
	l.mu.Unlock()
	var out []protocol.Frame
	for r.Len() > 0 {
		fr, err := protocol.Decode(r)
		if err != nil { t.Fatal(err) }
		out = append(out, fr)
	}
	return out
}

func (l *lockedBuffer) waitFrames(t *testing.T, n int) []protocol.Frame {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if frs := l.frames(t); len(frs) >= n || time.Now().After(deadline) {
			if len(frs) < n { t.Fatalf("got %d frames, want %d", len(frs), n) }
			return frs
		}
		time.Sleep(time.Millisecond)
	}
}

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
	out:=&lockedBuffer{}
	p,err:=New(Listener,Carrier{In:bytes.NewReader(nil),Out:out},"urn:baft:node:ir-01",tbl,Options{NodeID:"ex-01",ExpectedPeerNodeID:"ir-01",ProfileID:"secure-fast",ProfileVersion:1,ConfigRevision:"test"});if err!=nil{t.Fatal(err)}
	if !p.beginRunLifecycle(){t.Fatal("fixture could not model an active Session")}
	var dials atomic.Int32;remote:=make(chan net.Conn,1)
	p.dial=func(context.Context,string,string)(net.Conn,error){dials.Add(1);a,b:=net.Pipe();remote<-b;return a,nil}
	req,_:=protocol.EncodeControl(protocol.OpenRequest{RouteID:"main",OpenNonce:"00112233445566778899aabbccddeeff"})
	fr:=protocol.Frame{Type:protocol.TypeOpen,StreamID:1,Payload:req}
	count:=func(typ protocol.FrameType)int{n:=0;for _,f:=range out.frames(t){if f.Type==typ{n++}};return n}
	if err:=p.handleOpen(context.Background(),fr);err!=nil{t.Fatal(err)}
	if err:=p.handleOpen(context.Background(),fr);err!=nil{t.Fatal(err)} // may arrive while the dial is pending
	for deadline:=time.Now().Add(2*time.Second);count(protocol.TypeWindow)==0;time.Sleep(time.Millisecond){
		if time.Now().After(deadline){t.Fatal("OPEN never completed")}
	}
	before:=count(protocol.TypeOpenOK)
	if err:=p.handleOpen(context.Background(),fr);err!=nil{t.Fatal(err)} // after the Flow is open
	if got:=count(protocol.TypeOpenOK);got!=before+1{t.Fatalf("retransmitted OPEN on an open Flow sent %d OPEN_OK, want 1",got-before)}
	if got:=dials.Load();got!=1{t.Fatalf("dial count=%d",got)}
	p.closeAll();p.wg.Wait();(<-remote).Close()
}

func TestSlowTargetDialDoesNotBlockTheFrameLoop(t *testing.T) {
	tbl,err:=routes.New([]routes.Route{{ID:"main",Target:"127.0.0.1:2443",AllowedPeers:map[string]struct{}{"urn:baft:node:ir-01":{}}}});if err!=nil{t.Fatal(err)}
	out:=&lockedBuffer{}
	p,err:=New(Listener,Carrier{In:bytes.NewReader(nil),Out:out},"urn:baft:node:ir-01",tbl,Options{NodeID:"ex-01",ExpectedPeerNodeID:"ir-01",ProfileID:"secure-fast",ProfileVersion:1,ConfigRevision:"test"});if err!=nil{t.Fatal(err)}
	if !p.beginRunLifecycle(){t.Fatal("fixture could not model an active Session")}
	release:=make(chan struct{});var released sync.Once;defer released.Do(func(){close(release)})
	var dials atomic.Int32;remote:=make(chan net.Conn,1)
	p.dial=func(context.Context,string,string)(net.Conn,error){dials.Add(1);<-release;a,b:=net.Pipe();remote<-b;return a,nil}
	req,_:=protocol.EncodeControl(protocol.OpenRequest{RouteID:"main",OpenNonce:"00112233445566778899aabbccddeeff"})
	fr:=protocol.Frame{Type:protocol.TypeOpen,StreamID:1,Payload:req}

	done:=make(chan error,1)
	go func(){done<-p.handleOpen(context.Background(),fr)}()
	select{
	case err:=<-done:if err!=nil{t.Fatal(err)}
	case <-time.After(time.Second):t.Fatal("handleOpen blocked the Shard frame loop on a slow target dial")
	}
	// A retransmitted OPEN while the dial is in flight is answered by that dial.
	if err:=p.handleOpen(context.Background(),fr);err!=nil{t.Fatal(err)}
	if len(out.frames(t))!=0{t.Fatal("OPEN answered before the target dial finished")}

	released.Do(func(){close(release)})
	frs:=out.waitFrames(t,2)
	if frs[0].Type!=protocol.TypeOpenOK||frs[1].Type!=protocol.TypeWindow||frs[0].StreamID!=1{t.Fatalf("frames %v %v, want OPEN_OK then WINDOW on stream 1",frs[0].Type,frs[1].Type)}
	if got:=dials.Load();got!=1{t.Fatalf("dial count=%d",got)}
	p.closeAll();p.wg.Wait();(<-remote).Close()
}

func TestAsyncOpenDialFailureAnswersOpenErrAndReleasesSlot(t *testing.T) {
	tbl,err:=routes.New([]routes.Route{{ID:"main",Target:"127.0.0.1:2443",AllowedPeers:map[string]struct{}{"urn:baft:node:ir-01":{}}}});if err!=nil{t.Fatal(err)}
	slots,err:=resources.NewFlowSlots(1);if err!=nil{t.Fatal(err)}
	out:=&lockedBuffer{}
	p,err:=New(Listener,Carrier{In:bytes.NewReader(nil),Out:out},"urn:baft:node:ir-01",tbl,Options{NodeID:"ex-01",ExpectedPeerNodeID:"ir-01",ProfileID:"secure-fast",ProfileVersion:1,ConfigRevision:"test",FlowSlots:slots});if err!=nil{t.Fatal(err)}
	if !p.beginRunLifecycle(){t.Fatal("fixture could not model an active Session")}
	p.dial=func(context.Context,string,string)(net.Conn,error){return nil,errors.New("connect: connection refused")}
	req,_:=protocol.EncodeControl(protocol.OpenRequest{RouteID:"main",OpenNonce:"00112233445566778899aabbccddeeff"})
	if err:=p.handleOpen(context.Background(),protocol.Frame{Type:protocol.TypeOpen,StreamID:1,Payload:req});err!=nil{t.Fatal(err)}
	frs:=out.waitFrames(t,1)
	oe,err:=protocol.DecodeOpenError(frs[0].Payload)
	if frs[0].Type!=protocol.TypeOpenErr||err!=nil||oe.Code!=protocol.ErrorTargetUnreachable{t.Fatalf("got %v %q %v, want OPEN_ERR TARGET_UNREACHABLE",frs[0].Type,oe.Code,err)}
	p.wg.Wait()
	if slots.Used()!=0{t.Fatalf("failed dial kept %d node slots",slots.Used())}
	p.mu.Lock();pending:=len(p.pendingOpens);p.mu.Unlock()
	if pending!=0{t.Fatalf("failed dial left %d pending OPENs",pending)}
}

func TestOpenBeyondAdvertisedFlowLimitIsRefusedWithoutDial(t *testing.T) {
	tbl,err:=routes.New([]routes.Route{{ID:"main",Target:"127.0.0.1:2443",AllowedPeers:map[string]struct{}{"urn:baft:node:ir-01":{}}}});if err!=nil{t.Fatal(err)}
	var out bytes.Buffer
	p,err:=New(Listener,Carrier{In:bytes.NewReader(nil),Out:&out},"urn:baft:node:ir-01",tbl,Options{NodeID:"ex-01",ExpectedPeerNodeID:"ir-01",ProfileID:"secure-fast",ProfileVersion:1,ConfigRevision:"test"});if err!=nil{t.Fatal(err)}
	var dials atomic.Int32
	p.dial=func(context.Context,string,string)(net.Conn,error){dials.Add(1);return nil,errors.New("must not dial")}
	p.mu.Lock()
	for i:=0;i<maxFlowsPerShard;i++{id:=uint64(i*2+1);f:=newFlow(id,"main","00112233445566778899aabbccddeeff",nil,p.allocator);f.openOK=true;p.flows[id]=f}
	p.mu.Unlock()

	req,_:=protocol.EncodeControl(protocol.OpenRequest{RouteID:"main",OpenNonce:"ffeeddccbbaa99887766554433221100"})
	if err:=p.handleOpen(context.Background(),protocol.Frame{Type:protocol.TypeOpen,StreamID:uint64(maxFlowsPerShard*2+1),Payload:req});err!=nil{t.Fatal(err)}
	if got:=dials.Load();got!=0{t.Fatalf("dialed the target %d times past the advertised limit",got)}
	fr,err:=protocol.Decode(&out);if err!=nil{t.Fatal(err)}
	if fr.Type!=protocol.TypeOpenErr{t.Fatalf("sent %v, want OPEN_ERR",fr.Type)}
	oe,err:=protocol.DecodeOpenError(fr.Payload);if err!=nil{t.Fatal(err)}
	if oe.Code!=protocol.ErrorResourceExhausted{t.Fatalf("OPEN_ERR code=%q",oe.Code)}
	p.closeAll();p.wg.Wait()
}

func TestNodeFlowSlotsBoundListenerOpensAndAreReleasedOnClose(t *testing.T) {
	tbl,err:=routes.New([]routes.Route{{ID:"main",Target:"127.0.0.1:2443",AllowedPeers:map[string]struct{}{"urn:baft:node:ir-01":{}}}});if err!=nil{t.Fatal(err)}
	slots,err:=resources.NewFlowSlots(1);if err!=nil{t.Fatal(err)}
	out:=&lockedBuffer{}
	p,err:=New(Listener,Carrier{In:bytes.NewReader(nil),Out:out},"urn:baft:node:ir-01",tbl,Options{NodeID:"ex-01",ExpectedPeerNodeID:"ir-01",ProfileID:"secure-fast",ProfileVersion:1,ConfigRevision:"test",FlowSlots:slots});if err!=nil{t.Fatal(err)}
	if !p.beginRunLifecycle(){t.Fatal("fixture could not model an active Session")}
	var dials atomic.Int32;remote:=make(chan net.Conn,1)
	p.dial=func(context.Context,string,string)(net.Conn,error){dials.Add(1);a,b:=net.Pipe();remote<-b;return a,nil}
	open:=func(id uint64,nonce string){
		req,_:=protocol.EncodeControl(protocol.OpenRequest{RouteID:"main",OpenNonce:nonce})
		if err:=p.handleOpen(context.Background(),protocol.Frame{Type:protocol.TypeOpen,StreamID:id,Payload:req});err!=nil{t.Fatal(err)}
	}
	open(1,"00112233445566778899aabbccddeeff")
	if slots.Used()!=1{t.Fatalf("admitted OPEN holds %d node slots, want 1",slots.Used())}
	out.waitFrames(t,2) // OPEN_OK + WINDOW
	out.reset()
	open(3,"ffeeddccbbaa99887766554433221100")
	if got:=dials.Load();got!=1{t.Fatalf("dialed %d targets with max_flows=1",got)}
	fr:=out.waitFrames(t,1)[0]
	oe,err:=protocol.DecodeOpenError(fr.Payload)
	if fr.Type!=protocol.TypeOpenErr||err!=nil||oe.Code!=protocol.ErrorResourceExhausted{t.Fatalf("second OPEN got %v %q %v, want OPEN_ERR RESOURCE_EXHAUSTED",fr.Type,oe.Code,err)}

	p.closeAll();p.wg.Wait();(<-remote).Close()
	if slots.Used()!=0{t.Fatalf("closed Flow kept %d node slots",slots.Used())}
}

func TestNodeFlowSlotsBoundDialerOpensWithoutSendingOpen(t *testing.T) {
	slots,err:=resources.NewFlowSlots(1);if err!=nil{t.Fatal(err)}
	if !slots.TryAcquire(){t.Fatal("setup: could not take the only slot")}
	var out bytes.Buffer
	p,err:=New(Dialer,Carrier{In:bytes.NewReader(nil),Out:&out},"urn:baft:node:ex-01",nil,Options{NodeID:"ir-01",ExpectedPeerNodeID:"ex-01",FlowSlots:slots});if err!=nil{t.Fatal(err)}
	p.mu.Lock();p.localReady=true;p.peerReady=true;p.markReadyLocked();p.mu.Unlock()
	local,remote:=net.Pipe();defer remote.Close();defer local.Close()
	if err:=p.OpenFlow(context.Background(),"main",local);!errors.Is(err,resources.ErrResourceExhausted){t.Fatalf("OpenFlow err=%v, want RESOURCE_EXHAUSTED",err)}
	if out.Len()!=0{t.Fatalf("dialer sent %d bytes for a Flow it could not admit",out.Len())}
	if slots.Used()!=1{t.Fatalf("refused OpenFlow changed slot usage to %d",slots.Used())}
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
	if !p.beginRunLifecycle(){t.Fatal("fixture could not model an active Session")}
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

type resetReadConn struct{ net.Conn }

func (resetReadConn) Read([]byte) (int, error) { return 0, errors.New("read: connection reset by peer") }

func TestLocalReadErrorResetsFlowInsteadOfLeavingItOpen(t *testing.T) {
	var out bytes.Buffer
	p,err:=New(Dialer,Carrier{In:bytes.NewReader(nil),Out:&out},"urn:baft:node:ex-01",nil,Options{NodeID:"ir-01",ExpectedPeerNodeID:"ex-01"});if err!=nil{t.Fatal(err)}
	local,remote:=net.Pipe();defer remote.Close()
	f:=newFlow(1,"main","00112233445566778899aabbccddeeff",resetReadConn{local},p.allocator);f.openOK=true
	if err:=f.onWindow(64*1024);err!=nil{t.Fatal(err)}
	p.mu.Lock();p.flows[1]=f;p.localReady=true;p.peerReady=true;p.markReadyLocked();p.mu.Unlock()

	p.pumpLocal(context.Background(),f)

	if _,err:=p.getFlow(1);err==nil{t.Fatal("application read error left the Flow open")}
	fr,err:=protocol.Decode(&out);if err!=nil{t.Fatalf("no frame sent to the peer: %v",err)}
	if fr.Type!=protocol.TypeReset||fr.StreamID!=1{t.Fatalf("sent %v on stream %d, want RESET on 1",fr.Type,fr.StreamID)}
	rst,err:=protocol.DecodeReset(fr.Payload);if err!=nil{t.Fatal(err)}
	if rst.Code!=protocol.ErrorTargetUnreachable{t.Fatalf("RESET code=%q",rst.Code)}
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

// Recovery can finish a Flow from the peer's evidence that it sent FIN_ACK
// without that frame arriving. When the FIN_ACK does arrive later, the peer is
// still waiting for FIN_ACK_CONFIRM; only a graceful finish at that offset is
// confirmed.
func TestFinishedFlowReconfirmsLateFinAck(t *testing.T) {
	var out bytes.Buffer
	p,err:=New(Dialer,Carrier{In:bytes.NewReader(nil),Out:&out},"urn:baft:node:ex-01",nil,Options{NodeID:"ir-01",ExpectedPeerNodeID:"ex-01"});if err!=nil{t.Fatal(err)}
	p.mu.Lock();p.localReady=true;p.peerReady=true;p.markReadyLocked();p.mu.Unlock()
	f:=newFlow(5,"main","00112233445566778899aabbccddeeff",nil,p.allocator);f.openOK=true
	f.txNext=40;f.finSent=true;f.finAcked=true;f.finRecv=true;f.finRecvFinal=7;f.finAckSent=true;f.finAckConfirmed=true
	p.mu.Lock();p.flows[5]=f;p.mu.Unlock()
	p.finishIfComplete(f)
	if _,err:=p.getFlow(5);err==nil{t.Fatal("fixture Flow did not finish")}

	if err:=p.handleFrame(context.Background(),protocol.Frame{Type:protocol.TypeFinAck,StreamID:5,Offset:39});err!=nil{t.Fatal(err)}
	if out.Len()!=0{t.Fatal("confirmed a FIN_ACK for a different final offset")}
	if err:=p.handleFrame(context.Background(),protocol.Frame{Type:protocol.TypeFinAck,StreamID:5,Offset:40});err!=nil{t.Fatal(err)}
	fr,err:=protocol.Decode(&out);if err!=nil{t.Fatalf("no FIN_ACK_CONFIRM sent: %v",err)}
	if fr.Type!=protocol.TypeFinAckConfirm||fr.StreamID!=5||fr.Offset!=40{t.Fatalf("sent %v stream=%d offset=%d",fr.Type,fr.StreamID,fr.Offset)}

	// A Flow closed any other way (here: reset) is never confirmed.
	g:=newFlow(7,"main","00112233445566778899aabbccddeeff",nil,p.allocator);g.openOK=true;g.txNext=9
	p.mu.Lock();p.flows[7]=g;p.mu.Unlock()
	g.close();p.removeFlow(7)
	if err:=p.handleFrame(context.Background(),protocol.Frame{Type:protocol.TypeFinAck,StreamID:7,Offset:9});err!=nil{t.Fatal(err)}
	if out.Len()!=0{t.Fatal("confirmed a FIN_ACK for a Flow that did not finish gracefully")}
}

// A Flow is closed before it is removed from the Session, so a WINDOW can
// still reach it in between. close() has already closed creditWait; closing
// it again panicked the whole process.
func TestWindowAfterFlowCloseDoesNotPanic(t *testing.T) {
	var out bytes.Buffer
	p,err:=New(Dialer,Carrier{In:bytes.NewReader(nil),Out:&out},"urn:baft:node:ex-01",nil,Options{NodeID:"ir-01",ExpectedPeerNodeID:"ex-01"});if err!=nil{t.Fatal(err)}
	f:=newFlow(9,"main","00112233445566778899aabbccddeeff",nil,p.allocator);f.openOK=true
	f.close()
	if err:=f.onWindow(64*1024);err!=nil{t.Fatal(err)}
	if err:=f.restoreRecoveryPeerCredit(128*1024);err!=nil{t.Fatal(err)}
}

// An OPEN still in flight when the listener freezes for recovery must not end
// the Session (it used to return ErrResumeFrozen to the frame loop); only that
// OPEN is refused.
func TestFrozenListenerRefusesOpenWithoutEndingSession(t *testing.T) {
	tbl,err:=routes.New([]routes.Route{{ID:"main",Target:"127.0.0.1:2443",AllowedPeers:map[string]struct{}{"urn:baft:node:ir-01":{}}}});if err!=nil{t.Fatal(err)}
	out:=&lockedBuffer{}
	p,err:=New(Listener,Carrier{In:bytes.NewReader(nil),Out:out},"urn:baft:node:ir-01",tbl,Options{NodeID:"ex-01",ExpectedPeerNodeID:"ir-01",ProfileID:"secure-fast",ProfileVersion:1,ConfigRevision:"test",RecoveryEnabled:true,RecoveryRetention:time.Second,CarrierID:"carrier-1"});if err!=nil{t.Fatal(err)}
	var dials atomic.Int32
	p.dial=func(context.Context,string,string)(net.Conn,error){dials.Add(1);return nil,errors.New("must not dial")}
	req,_:=protocol.EncodeControl(protocol.OpenRequest{RouteID:"main",OpenNonce:"00112233445566778899aabbccddeeff"})

	p.recovery.mu.Lock();p.recovery.frozen=true;p.recovery.mu.Unlock()
	if err:=p.handleOpen(context.Background(),protocol.Frame{Type:protocol.TypeOpen,StreamID:1,Payload:req});err!=nil{t.Fatalf("frozen listener ended the Session on OPEN: %v",err)}
	frs:=out.frames(t)
	if len(frs)!=1||frs[0].Type!=protocol.TypeOpenErr||frs[0].StreamID!=1{t.Fatalf("frames %+v, want one OPEN_ERR on stream 1",frs)}
	if dials.Load()!=0{t.Fatal("refused OPEN dialed the target")}
}


func TestSessionWorkerAdmissionRejectsAfterTeardownGate(t *testing.T){
	p:=&Peer{}
	if !p.beginRunLifecycle(){t.Fatal("initial Run lifecycle admission unexpectedly rejected")}
	if !p.admitWorker(){t.Fatal("initial worker admission unexpectedly rejected")}

	p.beginRunTeardown()
	if p.admitWorker(){t.Fatal("worker admitted after Session teardown gate closed")}
	p.wg.Done()
	p.wg.Wait()
}
