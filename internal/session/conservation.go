package session

import (
	"sort"

	"github.com/zarkmakerburg/baft/internal/resources"
)

type FlowConservationSnapshot struct {
	StreamID         uint64
	Accepted         uint64
	Delivered        uint64
	Credit           uint64
	ReceiveReserved  int64
	RingBytes        int
	ReplayOutstanding uint64
	TWRLValid        bool
}

type ConservationSnapshot struct {
	Resources  resources.Snapshot
	Flows      []FlowConservationSnapshot
	Violations int
}

// ConservationSnapshot captures state as invariants rather than isolated
// counters. It is intended as the internal source for Stage-C diagnostics and
// later metrics export.
func (p *Peer) ConservationSnapshot() ConservationSnapshot {
	p.mu.Lock()
	flows := make([]*flow, 0, len(p.flows))
	for _, fl := range p.flows {
		flows = append(flows, fl)
	}
	alloc := p.allocator
	p.mu.Unlock()

	sort.Slice(flows, func(i, j int) bool { return flows[i].id < flows[j].id })
	out := ConservationSnapshot{}
	if alloc != nil {
		out.Resources = alloc.Snapshot()
	}
	out.Flows = make([]FlowConservationSnapshot, 0, len(flows))
	for _, fl := range flows {
		fl.mu.Lock()
		s := FlowConservationSnapshot{
			StreamID: fl.id,
			Accepted: fl.rxNext,
			Delivered: fl.rxWritten,
			Credit: fl.rxMax,
			ReceiveReserved: fl.receiveReserved,
		}
		if fl.txNext >= fl.txAcked {
			s.ReplayOutstanding = fl.txNext - fl.txAcked
		}
		if fl.rxRing != nil {
			s.RingBytes = fl.rxRing.Len()
		}
		valid := s.Delivered <= s.Accepted &&
			s.Accepted <= s.Credit &&
			s.Credit-s.Delivered <= uint64(max64(s.ReceiveReserved, 0)) &&
			uint64(s.RingBytes) == s.Accepted-s.Delivered
		s.TWRLValid = valid
		fl.mu.Unlock()
		if !valid {
			out.Violations++
		}
		out.Flows = append(out.Flows, s)
	}
	return out
}

func max64(v int64, floor int64) int64 {
	if v < floor {
		return floor
	}
	return v
}
