package session

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/protocol"
	"github.com/zarkmakerburg/baft/internal/resources"
	"github.com/zarkmakerburg/baft/internal/scheduler"
)

func TestOutboundSenderCapsControlBurstWhenDataPending(t *testing.T) {
	var buf bytes.Buffer
	s := newOutboundSender(&frameWriter{w: &buf})
	if err := s.data.AddFlow(1, dataChunk); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxControlBurst+8; i++ {
		req := &outboundRequest{
			frame: protocol.Frame{Type: protocol.TypeAck, StreamID: 1, Offset: uint64(i)},
			done: make(chan error, 1), control: true,
		}
		if err := s.control.Enqueue(resources.ControlItem{WireBytes: protocol.HeaderSize, Value: req}); err != nil {
			t.Fatal(err)
		}
	}
	dataReq := &outboundRequest{
		frame: protocol.Frame{Type: protocol.TypeData, StreamID: 1, Payload: []byte("x")},
		done: make(chan error, 1),
	}
	if err := s.data.Enqueue(scheduler.Item{FlowID: 1, Bytes: 1, Value: dataReq}); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < maxControlBurst; i++ {
		req, ok := s.nextLocked()
		if !ok || !req.control {
			t.Fatalf("selection %d should be control: %#v", i, req)
		}
	}
	req, ok := s.nextLocked()
	if !ok || req.control || req.frame.Type != protocol.TypeData {
		t.Fatalf("DATA must get service after control burst: %#v", req)
	}
}

func TestOutboundSenderControlQueueBoundIsEnforced(t *testing.T) {
	var buf bytes.Buffer
	s := newOutboundSender(&frameWriter{w: &buf})
	s.started = true // force queueing instead of pre-run direct fallback
	for i := 0; i < 256; i++ {
		req := &outboundRequest{
			frame: protocol.Frame{Type: protocol.TypeAck, StreamID: 1},
			done: make(chan error, 1), control: true,
		}
		if err := s.control.Enqueue(resources.ControlItem{WireBytes: protocol.HeaderSize, Value: req}); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}
	req := &outboundRequest{
		frame: protocol.Frame{Type: protocol.TypeAck, StreamID: 1},
		done: make(chan error, 1), control: true,
	}
	if err := s.control.Enqueue(resources.ControlItem{WireBytes: protocol.HeaderSize, Value: req}); err == nil {
		t.Fatal("expected 256-message control queue cap")
	}
}

func TestOutboundSenderRemoveFlowUnblocksQueuedData(t *testing.T) {
	var buf bytes.Buffer
	s:=newOutboundSender(&frameWriter{w:&buf})
	if err:=s.data.AddFlow(1,dataChunk);err!=nil{t.Fatal(err)}
	req:=&outboundRequest{
		frame:protocol.Frame{Type:protocol.TypeData,StreamID:1,Payload:[]byte("queued")},
		done:make(chan error,1),
	}
	if err:=s.data.Enqueue(scheduler.Item{FlowID:1,Bytes:len(req.frame.Payload),Value:req});err!=nil{t.Fatal(err)}
	s.removeFlow(1,errors.New("reset"))
	select{
	case err:=<-req.done:
		if err==nil||err.Error()!="reset"{t.Fatalf("unexpected removal error: %v",err)}
	default:
		t.Fatal("queued DATA waiter was not released")
	}
	if s.data.Len()!=0{t.Fatalf("queued data leaked after flow removal: %d",s.data.Len())}
}

func TestOutboundSenderPADLSeesReplayPressure(t *testing.T) {
	var buf bytes.Buffer
	s:=newOutboundSender(&frameWriter{w:&buf})
	if err:=s.data.AddFlow(1,dataChunk);err!=nil{t.Fatal(err)}
	if err:=s.data.AddFlow(3,dataChunk);err!=nil{t.Fatal(err)}
	f1:=newFlow(1,"a","00112233445566778899aabbccddeeff",nil);f1.openOK=true;f1.txNext=64*1024
	f3:=newFlow(3,"b","ffeeddccbbaa99887766554433221100",nil);f3.openOK=true
	r1:=&outboundRequest{flow:f1,frame:protocol.Frame{Type:protocol.TypeData,StreamID:1,Payload:[]byte("a")},done:make(chan error,1)}
	r3:=&outboundRequest{flow:f3,frame:protocol.Frame{Type:protocol.TypeData,StreamID:3,Payload:[]byte("b")},done:make(chan error,1)}
	_ = s.data.UpdatePressure(1,f1.replayPressure())
	_ = s.data.UpdatePressure(3,f3.replayPressure())
	if err:=s.data.Enqueue(scheduler.Item{FlowID:1,Bytes:1,Value:r1});err!=nil{t.Fatal(err)}
	if err:=s.data.Enqueue(scheduler.Item{FlowID:3,Bytes:1,Value:r3});err!=nil{t.Fatal(err)}
	req,ok:=s.nextLocked();if !ok{t.Fatal("no selection")}
	if req.frame.StreamID!=3{t.Fatalf("expected low-pressure flow 3, got %d",req.frame.StreamID)}
}


type alwaysFailWriter struct{}

func (alwaysFailWriter) Write([]byte)(int,error){return 0,io.ErrClosedPipe}

func TestRecoverableSenderReturnsCarrierUnavailableToCaller(t *testing.T){
	s:=newOutboundSender(&frameWriter{w:alwaysFailWriter{}},true)
	ctx,cancel:=context.WithCancel(context.Background())
	defer cancel()
	go s.run(ctx)
	deadline:=time.Now().Add(time.Second)
	for{
		s.mu.Lock();started:=s.started;s.mu.Unlock()
		if started{break}
		if time.Now().After(deadline){t.Fatal("sender did not start")}
		time.Sleep(time.Millisecond)
	}
	err:=s.sendControl(protocol.Frame{Type:protocol.TypePing,Payload:make([]byte,8)})
	if !errors.Is(err,ErrCarrierUnavailable){t.Fatalf("caller received non-recoverable error: %v",err)}
}

func TestRecoverableDirectWriteBeforeStartClassifiesCarrierError(t *testing.T){
	s:=newOutboundSender(&frameWriter{w:alwaysFailWriter{}},true)
	err:=s.sendControl(protocol.Frame{Type:protocol.TypePing,Payload:make([]byte,8)})
	if !errors.Is(err,ErrCarrierUnavailable){
		t.Fatalf("direct pre-start write leaked raw carrier error: %v",err)
	}
	if !errors.Is(err,io.ErrClosedPipe){
		t.Fatalf("direct pre-start write lost original carrier error chain: %v",err)
	}
}

func TestRecoverablePendingRequestStopClassifiesCarrierError(t *testing.T){
	var buf bytes.Buffer
	s:=newOutboundSender(&frameWriter{w:&buf},true)
	req:=&outboundRequest{
		frame:protocol.Frame{Type:protocol.TypePing,Payload:make([]byte,8)},
		done:make(chan error,1),control:true,
	}
	if err:=s.control.Enqueue(resources.ControlItem{WireBytes:protocol.HeaderSize+8,Value:req});err!=nil{t.Fatal(err)}
	s.stopWithSource(SenderStopContextDone,context.Canceled)
	select{
	case err:=<-req.done:
		if !errors.Is(err,ErrCarrierUnavailable){
			t.Fatalf("pending request leaked raw stop error: %v",err)
		}
		if !errors.Is(err,context.Canceled){
			t.Fatalf("pending request lost original context cancellation: %v",err)
		}
	case <-time.After(time.Second):
		t.Fatal("pending request was not released by sender stop")
	}
}

func TestRecoverableRemoveFlowPhysicalCancellationClassifiesCarrierError(t *testing.T){
	var buf bytes.Buffer
	s:=newOutboundSender(&frameWriter{w:&buf},true)
	fl:=newFlow(1,"route","00112233445566778899aabbccddeeff",nil,defaultAllocator())
	req:=&outboundRequest{
		flow:fl,
		frame:protocol.Frame{Type:protocol.TypeData,StreamID:1,Payload:[]byte("x")},
		done:make(chan error,1),
	}
	if err:=s.data.AddFlow(1,dataChunk);err!=nil{t.Fatal(err)}
	if err:=s.data.Enqueue(scheduler.Item{FlowID:1,Bytes:1,Value:req});err!=nil{t.Fatal(err)}
	s.removeFlow(1,context.Canceled)
	select{
	case err:=<-req.done:
		if !errors.Is(err,ErrCarrierUnavailable){t.Fatalf("removeFlow leaked raw carrier cancellation: %v",err)}
		if !errors.Is(err,context.Canceled){t.Fatalf("removeFlow lost original cancellation chain: %v",err)}
	case <-time.After(time.Second):
		t.Fatal("removeFlow did not release pending request")
	}
}



func TestRecoverableSenderContextCancellationReturnsCarrierUnavailable(t *testing.T){
	var buf bytes.Buffer
	s:=newOutboundSender(&frameWriter{w:&buf},true)
	ctx,cancel:=context.WithCancel(context.Background())
	go s.run(ctx)
	deadline:=time.Now().Add(time.Second)
	for{
		s.mu.Lock();started:=s.started;s.mu.Unlock()
		if started{break}
		if time.Now().After(deadline){t.Fatal("sender did not start")}
		time.Sleep(time.Millisecond)
	}
	cancel()
	deadline=time.Now().Add(time.Second)
	for{
		s.mu.Lock();stopped:=s.stopped;s.mu.Unlock()
		if stopped{break}
		if time.Now().After(deadline){t.Fatal("sender did not stop after context cancellation")}
		time.Sleep(time.Millisecond)
	}
	err:=s.sendControl(protocol.Frame{Type:protocol.TypePing,Payload:make([]byte,8)})
	if !errors.Is(err,ErrCarrierUnavailable){
		t.Fatalf("recoverable physical context cancellation leaked as logical-session error: %v",err)
	}
	if !errors.Is(err,context.Canceled){
		t.Fatalf("stopped sender lost original context cancellation chain: %v",err)
	}
}


type recoveryHandlerBlockingWriter struct {
	once sync.Once
	entered chan struct{}
	release chan struct{}
	mu sync.Mutex
	writes int
}

func newRecoveryHandlerBlockingWriter() *recoveryHandlerBlockingWriter {
	return &recoveryHandlerBlockingWriter{entered:make(chan struct{}),release:make(chan struct{})}
}

func (w *recoveryHandlerBlockingWriter) Write(p []byte)(int,error) {
	w.once.Do(func(){close(w.entered)})
	<-w.release
	w.mu.Lock();w.writes++;w.mu.Unlock()
	return len(p),nil
}

func (w *recoveryHandlerBlockingWriter) writeCount() int {
	w.mu.Lock();defer w.mu.Unlock();return w.writes
}

func TestRecoveryHTTPHandlerDoesNotReturnWhileOwnedSenderCanWrite(t *testing.T) {
	w:=newRecoveryHandlerBlockingWriter()
	s:=newOutboundSender(&frameWriter{w:w},true)
	req:=&outboundRequest{
		frame:protocol.Frame{Type:protocol.TypePing,Payload:make([]byte,8)},
		done:make(chan error,1),control:true,
	}
	if err:=s.control.Enqueue(resources.ControlItem{WireBytes:protocol.HeaderSize+8,Value:req});err!=nil{t.Fatal(err)}
	ctx,cancel:=context.WithCancel(context.Background())
	defer cancel()
	go s.run(ctx)
	select{case <-w.entered:case <-time.After(time.Second):t.Fatal("owned sender never entered writer")}

	fenced:=make(chan struct{})
	go func(){
		s.stopAndFenceWriter(SenderStopRecoveryOwnerFence,ErrCarrierUnavailable)
		close(fenced)
	}()
	select{
	case <-fenced:
		t.Fatal("handler fence returned while generation-owned writer could still write")
	default:
	}

	close(w.release)
	select{case <-req.done:case <-time.After(time.Second):t.Fatal("in-flight writer did not terminate")}
	select{case <-fenced:case <-time.After(time.Second):t.Fatal("generation writer join did not complete")}

	before:=w.writeCount()
	if err:=s.sendControl(protocol.Frame{Type:protocol.TypePing,Payload:make([]byte,8)});err==nil{
		t.Fatal("stopped generation admitted a write after handler fence")
	}
	if after:=w.writeCount();after!=before{
		t.Fatalf("write occurred after generation fence before=%d after=%d",before,after)
	}
}
