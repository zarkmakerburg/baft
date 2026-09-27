package session

import (
	"bytes"
	"errors"
	"testing"

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
