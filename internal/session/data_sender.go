package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/zarkmakerburg/baft/internal/protocol"
	"github.com/zarkmakerburg/baft/internal/resources"
	"github.com/zarkmakerburg/baft/internal/scheduler"
)

const maxControlBurst = 32

type SenderStopSource string

const (
	SenderStopUnknown SenderStopSource = "unknown"
	SenderStopWriterError SenderStopSource = "writer_error"
	SenderStopContextDone SenderStopSource = "sender_context_done"
	SenderStopExplicitReplace SenderStopSource = "sender_explicit_replace"
	SenderStopCarrierReaderDecode SenderStopSource = "carrier_reader_decode_error"
	SenderStopFrameProcessing SenderStopSource = "frame_processing_carrier_failure"
	SenderStopRecoveryOwnerFence SenderStopSource = "recovery_owner_fence"
	SenderStopRecoveryGenerationFailure SenderStopSource = "recovery_generation_failure"
	SenderStopRuntimeShutdown SenderStopSource = "runtime_shutdown"
	SenderStopFlowClosed SenderStopSource = "flow_closed"
)

type DataProducerKind string

const (
	ProducerOther DataProducerKind = "OTHER"
	ProducerReplay DataProducerKind = "REPLAY"
	ProducerLivePump DataProducerKind = "LIVE_APPLICATION_PUMP"
	ProducerRecoveredPump DataProducerKind = "RECOVERED_PUMP"
)

type DataWriteDiagnostic struct {
	Event string
	ProducerKind DataProducerKind
	StreamID uint64
	Offset uint64
	End uint64
	SenderID uint64
	PreparedIncarnation uint64
	CarrierGeneration uint64
	PhysicalCarrierInstanceID uint64
	WriteSequence uint64
}

type SenderStopEvent struct {
	SenderID uint64
	Source SenderStopSource
	Error string
	SessionID string
	Epoch uint64
	CandidateID string
	PlanDigest string
	PreparedIncarnation uint64
	CarrierGeneration uint64
	PhysicalCarrierInstanceID uint64
}

type senderDiagnosticBinding struct {
	SessionID string
	Epoch uint64
	CandidateID string
	PlanDigest string
	PreparedIncarnation uint64
	CarrierGeneration uint64
	PhysicalCarrierInstanceID uint64
}

var outboundSenderSequence atomic.Uint64

type outboundRequest struct {
	flow    *flow
	frame   protocol.Frame
	done    chan error
	control bool
	producer DataProducerKind
	writeSequence uint64
}

type outboundSender struct {
	mu           sync.Mutex
	id           uint64
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
	diag senderDiagnosticBinding
	stopEvent SenderStopEvent
	hasStopEvent bool
	onStop func(SenderStopEvent)
	onDataWrite func(DataWriteDiagnostic)
	// writeFault is a test-only injection at the physical write boundary.
	writeFault func(frame protocol.Frame,generation uint64) error
	writeSeq uint64
	windowHigh map[uint64]uint64
}

func newOutboundSender(writer *frameWriter, recoverable ...bool) *outboundSender {
	r:=false;if len(recoverable)>0{r=recoverable[0]}
	return &outboundSender{
		id: outboundSenderSequence.Add(1),
		data:    scheduler.NewPADL(scheduler.DefaultPADLMaxSkips),
		control: resources.NewDefaultControlQueue(),
		writer:  writer,
		wake:    make(chan struct{}, 1),
		done:    make(chan struct{}),
		recoverable:r,
		windowHigh: map[uint64]uint64{},
	}
}

// writeFrame is the generation-local write admission boundary.  The sender
// mutex stays held until the exact writer mutex has been acquired, so a stop
// cannot slip between the stopped check and writer ownership.  Consequently
// stopAndFenceWriter can join only this sender's in-flight writer and know that
// no admitted write can begin after its fence returns.
func (s *outboundSender) writeFrame(frame protocol.Frame) error {
	if s==nil{return errors.New("nil outbound sender")}
	s.mu.Lock()
	if s.stopped {
		err:=s.stopErrorLocked()
		s.mu.Unlock()
		return err
	}
	w:=s.writer
	if w==nil {
		s.mu.Unlock()
		return errors.New("outbound sender writer is nil")
	}
	fault,generation:=s.writeFault,s.diag.CarrierGeneration
	w.mu.Lock()
	s.mu.Unlock()
	defer w.mu.Unlock()
	if fault!=nil{
		if err:=fault(frame,generation);err!=nil{return s.carrierError(err)}
	}
	return s.carrierError(protocol.Encode(w.w,frame))
}

// carrierError classifies an error that came from the physical carrier. In a
// recoverable Session every failure of the physical stream (a write on a
// cancelled request, a closed Noise connection, a reset HTTP/2 stream) is a
// carrier failure, whichever path produced it: the queued writer, a direct
// write before the sender started, or the stop error of a stopped sender.
// The original error stays in the chain. A raw carrier error must never reach
// the logical Session actor, which would read it as the Session's own end.
func (s *outboundSender) carrierError(err error) error {
	if err==nil||s==nil||!s.recoverable||errors.Is(err,ErrCarrierUnavailable){return err}
	return fmt.Errorf("%w: %w",ErrCarrierUnavailable,err)
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
	if frame.Type==protocol.TypeWindow {
		if high,ok:=s.windowHigh[frame.StreamID];ok && frame.Offset<high {
			s.mu.Unlock()
			return nil
		}
		if frame.Offset>s.windowHigh[frame.StreamID]{s.windowHigh[frame.StreamID]=frame.Offset}
	}
	if !s.started && !s.stopped {
		s.mu.Unlock()
		return s.writeFrame(frame)
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
	return s.sendDataWithProducer(ctx,fl,frame,ProducerLivePump)
}

func (s *outboundSender) sendDataWithProducer(ctx context.Context, fl *flow, frame protocol.Frame, producer DataProducerKind) error {
	if fl == nil || frame.Type != protocol.TypeData || frame.StreamID != fl.id || len(frame.Payload) == 0 {
		return errors.New("invalid scheduled DATA frame")
	}
	if producer=="" { producer=ProducerOther }
	fl.mu.Lock()
	closed := fl.closed
	fl.mu.Unlock()
	if closed {
		return errors.New("flow closed")
	}

	req := &outboundRequest{flow: fl, frame: frame, done: make(chan error, 1), producer:producer}
	s.mu.Lock()
	if s.stopped {
		err := s.stopErrorLocked()
		s.mu.Unlock()
		return err
	}
	if !s.started {
		s.writeSeq++
		req.writeSequence=s.writeSeq
		begin,observer:=s.dataWriteDiagnosticLocked(req,"DATA_WRITE_BEGIN"),s.onDataWrite
		s.mu.Unlock()
		if observer!=nil{observer(begin)}
		err:=s.writeFrame(frame)
		if err==nil&&observer!=nil{
			s.mu.Lock();done:=s.dataWriteDiagnosticLocked(req,"DATA_WRITE_SUCCESS");s.mu.Unlock()
			observer(done)
		}
		return err
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
			s.stopWithSource(SenderStopContextDone,err)
			return
		}

		s.mu.Lock()
		req, ok := s.nextLocked()
		s.mu.Unlock()
		if !ok {
			select {
			case <-ctx.Done():
				s.stopWithSource(SenderStopContextDone,ctx.Err())
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

		var observer func(DataWriteDiagnostic)
		var begin DataWriteDiagnostic
		if req.frame.Type==protocol.TypeData {
			s.mu.Lock()
			s.writeSeq++
			req.writeSequence=s.writeSeq
			begin=s.dataWriteDiagnosticLocked(req,"DATA_WRITE_BEGIN")
			observer=s.onDataWrite
			s.mu.Unlock()
			if observer!=nil{observer(begin)}
		}
		err := s.carrierError(s.writeFrame(req.frame))
		if err==nil && req.frame.Type==protocol.TypeData && observer!=nil {
			s.mu.Lock();done:=s.dataWriteDiagnosticLocked(req,"DATA_WRITE_SUCCESS");s.mu.Unlock()
			observer(done)
		}
		req.done <- err
		if err != nil {
			s.stopWithSource(SenderStopWriterError,err)
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

func (s *outboundSender) stop(err error) { s.stopWithSource(SenderStopUnknown,err) }

func (s *outboundSender) stopWithSource(source SenderStopSource,err error) {
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
	s.stopEvent=SenderStopEvent{
		SenderID:s.id,Source:source,Error:err.Error(),
		SessionID:s.diag.SessionID,Epoch:s.diag.Epoch,CandidateID:s.diag.CandidateID,
		PlanDigest:s.diag.PlanDigest,PreparedIncarnation:s.diag.PreparedIncarnation,
		CarrierGeneration:s.diag.CarrierGeneration,PhysicalCarrierInstanceID:s.diag.PhysicalCarrierInstanceID,
	}
	s.hasStopEvent=true

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
	ev:=s.stopEvent
	observer:=s.onStop
	s.mu.Unlock()

	if observer!=nil{observer(ev)}
	for _, req := range pending {
		select {
		case req.done <- s.carrierError(err):
		default:
		}
	}
}

func (s *outboundSender) stopAndFenceWriter(source SenderStopSource, err error) {
    if s==nil{return}
    s.stopWithSource(source,err)
    // stopWithSource prevents any new queued/direct write, but an already
    // executing frameWriter.send may still own the HTTP response writer.
    // Acquiring the same writer mutex is the carrier-lifetime fence: after
    // this returns no write from this sender can touch the retiring stream.
    if s.writer!=nil {
        s.writer.mu.Lock()
        s.writer.mu.Unlock()
    }
}

func (s *outboundSender) stopErrorLocked() error {
	if s.stopErr != nil {
		return s.carrierError(s.stopErr)
	}
	return s.carrierError(errors.New("outbound sender stopped"))
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


func (s *outboundSender) isStarted() bool {
	if s==nil{return false}
	s.mu.Lock();defer s.mu.Unlock()
	return s.started&&!s.stopped
}


func (s *outboundSender) senderID() uint64 {
	if s==nil{return 0}
	return s.id
}

func (s *outboundSender) bindRecoveryDiagnostic(b senderDiagnosticBinding) {
	if s==nil{return}
	s.mu.Lock()
	s.diag=b
	s.mu.Unlock()
}

func (s *outboundSender) setDiagnosticCarrierGeneration(g uint64) {
	if s==nil{return}
	s.mu.Lock()
	s.diag.CarrierGeneration=g
	s.mu.Unlock()
}

func (s *outboundSender) stopEventSnapshot()(SenderStopEvent,bool) {
	if s==nil{return SenderStopEvent{},false}
	s.mu.Lock();defer s.mu.Unlock()
	if !s.hasStopEvent{return SenderStopEvent{},false}
	return s.stopEvent,true
}


func (s *outboundSender) setStopObserver(fn func(SenderStopEvent)) {
	if s==nil{return}
	s.mu.Lock()
	s.onStop=fn
	s.mu.Unlock()
}


func (s *outboundSender) dataWriteDiagnosticLocked(req *outboundRequest,event string) DataWriteDiagnostic {
	if req==nil{return DataWriteDiagnostic{Event:event,SenderID:s.id}}
	return DataWriteDiagnostic{
		Event:event,ProducerKind:req.producer,StreamID:req.frame.StreamID,Offset:req.frame.Offset,
		End:req.frame.Offset+uint64(len(req.frame.Payload)),SenderID:s.id,
		PreparedIncarnation:s.diag.PreparedIncarnation,CarrierGeneration:s.diag.CarrierGeneration,
		PhysicalCarrierInstanceID:s.diag.PhysicalCarrierInstanceID,WriteSequence:req.writeSequence,
	}
}

func (s *outboundSender) setDataWriteObserver(fn func(DataWriteDiagnostic)) {
	if s==nil{return}
	s.mu.Lock();s.onDataWrite=fn;s.mu.Unlock()
}
