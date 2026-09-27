package session

import (
	"bytes"
	"testing"

	"github.com/zarkmakerburg/baft/internal/protocol"
	"github.com/zarkmakerburg/baft/internal/resources"
	"github.com/zarkmakerburg/baft/internal/scheduler"
)

func TestOutboundSenderCapsControlBurstWhenDataPending(t *testing.T) {
	var buf bytes.Buffer
	s := newOutboundSender(&frameWriter{w: &buf})
	if err := s.drr.AddFlow(1, dataChunk); err != nil {
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
	if err := s.drr.Enqueue(scheduler.Item{FlowID: 1, Bytes: 1, Value: dataReq}); err != nil {
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
