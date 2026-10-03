package session

// WindowUpdateDiagnosticForTest identifies a WINDOW update observed at the
// Session boundary. It exists only to make COR-01 liveness stalls explainable.
type WindowUpdateDiagnosticForTest struct {
	Direction string
	StreamID  uint64
	Offset    uint64
}

// FlowDebugSnapshotForTest is a point-in-time, read-only snapshot used by
// correctness watchdogs. It must not influence Session behavior.
type FlowDebugSnapshotForTest struct {
	StreamID  uint64
	TxNext    uint64
	TxAcked   uint64
	PeerMax   uint64
	RxNext    uint64
	RxWritten uint64
	RxMax     uint64
	Closed    bool
}

// SessionDebugSnapshotForTest exposes only liveness/accounting state needed by
// diagnostics. Queue sizes are sampled from the current outbound sender.
type SessionDebugSnapshotForTest struct {
	Flows             []FlowDebugSnapshotForTest
	SenderStarted     bool
	SenderStopped     bool
	ControlQueueBytes int
	DataQueueItems    int
}

func (p *Peer) SetWindowUpdateObserverForTest(fn func(WindowUpdateDiagnosticForTest)) {
	if p == nil {
		return
	}
	p.windowObserverMu.Lock()
	p.windowObserverForTest = fn
	p.windowObserverMu.Unlock()
}

func (p *Peer) notifyWindowUpdateForTest(direction string, streamID, offset uint64) {
	if p == nil {
		return
	}
	p.windowObserverMu.RLock()
	fn := p.windowObserverForTest
	p.windowObserverMu.RUnlock()
	if fn != nil {
		fn(WindowUpdateDiagnosticForTest{Direction: direction, StreamID: streamID, Offset: offset})
	}
}

func (p *Peer) DebugSnapshotForTest() SessionDebugSnapshotForTest {
	if p == nil {
		return SessionDebugSnapshotForTest{}
	}
	p.mu.Lock()
	flows := make([]*flow, 0, len(p.flows))
	for _, fl := range p.flows {
		flows = append(flows, fl)
	}
	p.mu.Unlock()

	out := SessionDebugSnapshotForTest{Flows: make([]FlowDebugSnapshotForTest, 0, len(flows))}
	for _, fl := range flows {
		if fl == nil {
			continue
		}
		fl.mu.Lock()
		out.Flows = append(out.Flows, FlowDebugSnapshotForTest{
			StreamID: fl.id, TxNext: fl.txNext, TxAcked: fl.txAcked,
			PeerMax: fl.peerMax, RxNext: fl.rxNext, RxWritten: fl.rxWritten,
			RxMax: fl.rxMax, Closed: fl.closed,
		})
		fl.mu.Unlock()
	}

	s := p.senderNow()
	if s != nil {
		s.mu.Lock()
		out.SenderStarted = s.started
		out.SenderStopped = s.stopped
		if s.control != nil {
			out.ControlQueueBytes, _ = s.control.LenBytes()
		}
		if s.data != nil {
			out.DataQueueItems = s.data.Len()
		}
		s.mu.Unlock()
	}
	return out
}
