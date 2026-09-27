package session

import (
	"context"
	"errors"
	"sync"

	"github.com/zarkmakerburg/baft/internal/protocol"
	"github.com/zarkmakerburg/baft/internal/scheduler"
)

type scheduledData struct {
	flow  *flow
	frame protocol.Frame
	done  chan error
}

type dataSender struct {
	mu     sync.Mutex
	drr    *scheduler.DRR
	writer *frameWriter
	wake   chan struct{}
}

func newDataSender(writer *frameWriter) *dataSender {
	return &dataSender{
		drr: scheduler.NewDRR(),
		writer: writer,
		wake: make(chan struct{}, 1),
	}
}

func (s *dataSender) addFlow(flowID uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.drr.AddFlow(flowID, dataChunk)
}

func (s *dataSender) send(ctx context.Context, fl *flow, frame protocol.Frame) error {
	if fl == nil || frame.Type != protocol.TypeData || frame.StreamID != fl.id || len(frame.Payload) == 0 {
		return errors.New("invalid scheduled DATA frame")
	}
	fl.mu.Lock()
	closed := fl.closed
	fl.mu.Unlock()
	if closed {
		return errors.New("flow closed")
	}

	req := &scheduledData{flow:fl,frame:frame,done:make(chan error,1)}
	s.mu.Lock()
	err:=s.drr.Enqueue(scheduler.Item{FlowID:fl.id,Bytes:len(frame.Payload),Value:req})
	s.mu.Unlock()
	if err!=nil { return err }
	select { case s.wake<-struct{}{}: default: }

	select {
	case err:=<-req.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *dataSender) run(ctx context.Context) {
	for {
		s.mu.Lock()
		item,ok:=s.drr.Next()
		s.mu.Unlock()
		if ok {
			req,ok:=item.Value.(*scheduledData)
			if !ok || req==nil {
				continue
			}
			req.flow.mu.Lock()
			closed:=req.flow.closed
			req.flow.mu.Unlock()
			if closed {
				req.done<-errors.New("flow closed")
				continue
			}
			req.done<-s.writer.send(req.frame)
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		}
	}
}
