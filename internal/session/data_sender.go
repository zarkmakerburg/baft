package session

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/zarkmakerburg/baft/internal/protocol"
	"github.com/zarkmakerburg/baft/internal/resources"
	"github.com/zarkmakerburg/baft/internal/scheduler"
)

const maxControlBurst = 32

type outboundRequest struct {
	flow    *flow
	frame   protocol.Frame
	done    chan error
	control bool
}

type outboundSender struct {
	mu           sync.Mutex
	data         *scheduler.PADL
	control      *resources.ControlQueue
	writer       *frameWriter
	wake         chan struct{}
	done         chan struct{}
	started      bool
	stopped      bool
	stopErr      error
	controlBurst int
	recoverable bool
}

func newOutboundSender(writer *frameWriter, recoverable ...bool) *outboundSender {
	r:=false;if len(recoverable)>0{r=recoverable[0]}
	return &outboundSender{
		data:    scheduler.NewPADL(scheduler.DefaultPADLMaxSkips),
		control: resources.NewDefaultControlQueue(),
		writer:  writer,
		wake:    make(chan struct{}, 1),
		done:    make(chan struct{}),
		recoverable:r,
	}
}

func (s *outboundSender) addFlow(flowID uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return s.stopErrorLocked()
	}
	return s.data.AddFlow(flowID, dataChunk)
}

func (s *outboundSender) removeFlow(flowID uint64, cause error) {
	if cause == nil {
		cause = errors.New("flow removed")
	}
	s.mu.Lock()
	pending := s.data.RemoveFlow(flowID)
	s.mu.Unlock()
	for _, item := range pending {
		req, ok := item.Value.(*outboundRequest)
		if !ok || req == nil {
			continue
		}
		select {
		case req.done <- cause:
		default:
		}
	}
}

func (s *outboundSender) sendControl(frame protocol.Frame) error {
	if frame.Type == protocol.TypeData {
		return errors.New("DATA must use PADL data path")
	}
	req := &outboundRequest{frame: frame, done: make(chan error, 1), control: true}

	s.mu.Lock()
	if !s.started && !s.stopped {
		s.mu.Unlock()
		return s.writer.send(frame)
	}
	if s.stopped {
		err := s.stopErrorLocked()
		s.mu.Unlock()
		return err
	}
	err := s.control.Enqueue(resources.ControlItem{
		WireBytes: protocol.HeaderSize + len(frame.Payload),
		Value:     req,
	})
	s.mu.Unlock()
	if err != nil {
		return err
	}
	s.signal()

	select {
	case err := <-req.done:
		return err
	case <-s.done:
		s.mu.Lock()
		err := s.stopErrorLocked()
		s.mu.Unlock()
		return err
	}
}

func (s *outboundSender) sendData(ctx context.Context, fl *flow, frame protocol.Frame) error {
	if fl == nil || frame.Type != protocol.TypeData || frame.StreamID != fl.id || len(frame.Payload) == 0 {
		return errors.New("invalid scheduled DATA frame")
	}
	fl.mu.Lock()
	closed := fl.closed
	fl.mu.Unlock()
	if closed {
		return errors.New("flow closed")
	}

	req := &outboundRequest{flow: fl, frame: frame, done: make(chan error, 1)}
	s.mu.Lock()
	if s.stopped {
		err := s.stopErrorLocked()
		s.mu.Unlock()
		return err
	}
	if !s.started {
		s.mu.Unlock()
		return s.writer.send(frame)
	}
	pressure := fl.replayPressure()
	_ = s.data.UpdatePressure(fl.id, pressure)
	err := s.data.Enqueue(scheduler.Item{FlowID: fl.id, Bytes: len(frame.Payload), Value: req})
	s.mu.Unlock()
	if err != nil {
		return err
	}
	s.signal()

	select {
	case err := <-req.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		s.mu.Lock()
		err := s.stopErrorLocked()
		s.mu.Unlock()
		return err
	}
}

func (s *outboundSender) run(ctx context.Context) {
	s.mu.Lock()
	if s.started || s.stopped {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()

	for {
		if err := ctx.Err(); err != nil {
			s.stop(err)
			return
		}

		s.mu.Lock()
		req, ok := s.nextLocked()
		s.mu.Unlock()
		if !ok {
			select {
			case <-ctx.Done():
				s.stop(ctx.Err())
				return
			case <-s.wake:
			}
			continue
		}

		if req.flow != nil {
			req.flow.mu.Lock()
			closed := req.flow.closed
			req.flow.mu.Unlock()
			if closed {
				req.done <- errors.New("flow closed")
				continue
			}
		}

		err := s.writer.send(req.frame)
		if err != nil && s.recoverable {
			err = fmt.Errorf("%w: %v",ErrCarrierUnavailable,err)
		}
		req.done <- err
		if err != nil {
			s.stop(err)
			return
		}
	}
}

func (s *outboundSender) nextLocked() (*outboundRequest, bool) {
	controlN, _ := s.control.LenBytes()
	dataN := s.data.Len()

	if controlN > 0 && (s.controlBurst < maxControlBurst || dataN == 0) {
		item, ok := s.control.Dequeue()
		if !ok {
			return nil, false
		}
		req, ok := item.Value.(*outboundRequest)
		if !ok || req == nil {
			return nil, false
		}
		s.controlBurst++
		return req, true
	}

	if dataN > 0 {
		item, ok := s.data.Next()
		if !ok {
			return nil, false
		}
		req, ok := item.Value.(*outboundRequest)
		if !ok || req == nil {
			return nil, false
		}
		s.controlBurst = 0
		return req, true
	}

	if controlN > 0 {
		item, ok := s.control.Dequeue()
		if !ok {
			return nil, false
		}
		req, ok := item.Value.(*outboundRequest)
		if !ok || req == nil {
			return nil, false
		}
		s.controlBurst++
		return req, true
	}
	return nil, false
}

func (s *outboundSender) stop(err error) {
	if err == nil {
		err = errors.New("outbound sender stopped")
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	s.stopErr = err

	var pending []*outboundRequest
	for {
		item, ok := s.control.Dequeue()
		if !ok {
			break
		}
		if req, ok := item.Value.(*outboundRequest); ok && req != nil {
			pending = append(pending, req)
		}
	}
	for s.data.Len() > 0 {
		item, ok := s.data.Next()
		if !ok {
			break
		}
		if req, ok := item.Value.(*outboundRequest); ok && req != nil {
			pending = append(pending, req)
		}
	}
	close(s.done)
	s.mu.Unlock()

	for _, req := range pending {
		select {
		case req.done <- err:
		default:
		}
	}
}

func (s *outboundSender) stopErrorLocked() error {
	if s.stopErr != nil {
		return s.stopErr
	}
	return errors.New("outbound sender stopped")
}

func (s *outboundSender) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *outboundSender) updatePressure(flowID uint64, replayBytes uint64) {
	s.mu.Lock()
	if !s.stopped {
		_ = s.data.UpdatePressure(flowID, replayBytes)
	}
	s.mu.Unlock()
}


func (s *outboundSender) isStopped() bool {
	if s==nil{return true}
	s.mu.Lock();defer s.mu.Unlock()
	return s.stopped
}
