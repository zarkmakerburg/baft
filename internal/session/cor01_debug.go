//go:build cor01debug

package session

type COR01FlowDebugSnapshot struct {
	StreamID         uint64
	PeerMax          uint64
	TxNext           uint64
	TxAcked          uint64
	RxNext           uint64
	RxWritten        uint64
	RxMax            uint64
	ReceiveReserved  int64
	RingBytes        int
	ReplayChunks     int
	ReplayBytes      uint64
	OpenOK           bool
	Closed           bool
	LocalPumpRunning bool
	TargetPumpRunning bool
}

type COR01SenderDebugSnapshot struct {
	SenderID            uint64
	Started             bool
	Stopped             bool
	ControlQueueMessages int
	ControlQueueBytes    int
	DataQueueItems       int
	ControlBurst         int
	WindowHigh           map[uint64]uint64
}

type COR01PeerDebugSnapshot struct {
	Role   Role
	Flows  []COR01FlowDebugSnapshot
	Sender COR01SenderDebugSnapshot
}

func (p *Peer) COR01DebugSnapshot() COR01PeerDebugSnapshot {
	if p == nil {
		return COR01PeerDebugSnapshot{}
	}
	p.mu.Lock()
	role := p.role
	flows := make([]*flow, 0, len(p.flows))
	for _, fl := range p.flows {
		flows = append(flows, fl)
	}
	sender := p.sender
	p.mu.Unlock()

	out := COR01PeerDebugSnapshot{Role: role}
	for _, fl := range flows {
		fl.mu.Lock()
		fs := COR01FlowDebugSnapshot{
			StreamID: fl.id,
			PeerMax: fl.peerMax,
			TxNext: fl.txNext,
			TxAcked: fl.txAcked,
			RxNext: fl.rxNext,
			RxWritten: fl.rxWritten,
			RxMax: fl.rxMax,
			ReceiveReserved: fl.receiveReserved,
			ReplayChunks: len(fl.replay),
			OpenOK: fl.openOK,
			Closed: fl.closed,
			LocalPumpRunning: fl.localPumpRunning,
			TargetPumpRunning: fl.targetPumpRunning,
		}
		for _, ch := range fl.replay {
			if ch.end >= ch.start {
				fs.ReplayBytes += ch.end - ch.start
			}
		}
		if fl.rxRing != nil {
			fs.RingBytes = fl.rxRing.Len()
		}
		fl.mu.Unlock()
		out.Flows = append(out.Flows, fs)
	}
	if sender != nil {
		sender.mu.Lock()
		msgs, bytes := sender.control.LenBytes()
		ss := COR01SenderDebugSnapshot{
			SenderID: sender.id,
			Started: sender.started,
			Stopped: sender.stopped,
			ControlQueueMessages: msgs,
			ControlQueueBytes: bytes,
			DataQueueItems: sender.data.Len(),
			ControlBurst: sender.controlBurst,
			WindowHigh: map[uint64]uint64{},
		}
		for id, high := range sender.windowHigh {
			ss.WindowHigh[id] = high
		}
		sender.mu.Unlock()
		out.Sender = ss
	}
	return out
}
