package scheduler

import (
	"errors"
	"fmt"
)

const DefaultPADLMaxSkips = 8

type padlFlowState struct {
	quantum  int
	deficit  int
	pressure uint64
	skipped  int
	queue    []Item
}

// PADL (Pressure-Aged Deficit Leasing) preserves byte-deficit accounting but
// selects among eligible flows using retained replay pressure. Aging bounds
// starvation when a flow remains under sustained pressure.
type PADL struct {
	flows    map[uint64]*padlFlowState
	order    []uint64
	cursor   int
	queued   int
	maxSkips int
}

func NewPADL(maxSkips int) *PADL {
	if maxSkips <= 0 {
		maxSkips = DefaultPADLMaxSkips
	}
	return &PADL{flows: map[uint64]*padlFlowState{}, maxSkips: maxSkips}
}

func (p *PADL) AddFlow(flowID uint64, quantumBytes int) error {
	if flowID == 0 || quantumBytes <= 0 {
		return errors.New("flowID and quantum must be positive")
	}
	if _, ok := p.flows[flowID]; ok {
		return errors.New("flow already exists")
	}
	p.flows[flowID] = &padlFlowState{quantum: quantumBytes}
	p.order = append(p.order, flowID)
	return nil
}

func (p *PADL) RemoveFlow(flowID uint64) []Item {
	f, ok := p.flows[flowID]
	if !ok {
		return nil
	}
	pending := append([]Item(nil), f.queue...)
	p.queued -= len(f.queue)
	delete(p.flows, flowID)
	for i, id := range p.order {
		if id != flowID {
			continue
		}
		p.order = append(p.order[:i], p.order[i+1:]...)
		if len(p.order) == 0 {
			p.cursor = 0
		} else if i < p.cursor {
			p.cursor--
		} else if p.cursor >= len(p.order) {
			p.cursor = 0
		}
		break
	}
	return pending
}

func (p *PADL) UpdatePressure(flowID uint64, replayBytes uint64) error {
	f, ok := p.flows[flowID]
	if !ok {
		return fmt.Errorf("unknown flow %d", flowID)
	}
	f.pressure = replayBytes
	return nil
}

func (p *PADL) Enqueue(item Item) error {
	if item.FlowID == 0 || item.Bytes <= 0 {
		return errors.New("invalid data item")
	}
	f, ok := p.flows[item.FlowID]
	if !ok {
		return fmt.Errorf("unknown flow %d", item.FlowID)
	}
	f.queue = append(f.queue, item)
	p.queued++
	return nil
}

func (p *PADL) Len() int { return p.queued }

func (p *PADL) Next() (Item, bool) {
	if p.queued == 0 || len(p.order) == 0 {
		return Item{}, false
	}

	for {
		type candidate struct {
			index int
			id    uint64
			state *padlFlowState
		}
		candidates := make([]candidate, 0, len(p.order))

		for step := 0; step < len(p.order); step++ {
			idx := (p.cursor + step) % len(p.order)
			id := p.order[idx]
			f := p.flows[id]
			if f == nil || len(f.queue) == 0 {
				if f != nil {
					f.deficit = 0
					f.skipped = 0
				}
				continue
			}
			f.deficit += f.quantum
			if f.queue[0].Bytes <= f.deficit {
				candidates = append(candidates, candidate{index: idx, id: id, state: f})
			}
		}

		if len(candidates) == 0 {
			continue
		}

		chosen := candidates[0]
		for _, c := range candidates[1:] {
			chosenForced := chosen.state.skipped >= p.maxSkips
			cForced := c.state.skipped >= p.maxSkips
			switch {
			case cForced && !chosenForced:
				chosen = c
			case cForced && chosenForced && c.state.skipped > chosen.state.skipped:
				chosen = c
			case !cForced && !chosenForced && c.state.pressure < chosen.state.pressure:
				chosen = c
			}
		}

		for _, id := range p.order {
			f := p.flows[id]
			if f == nil || len(f.queue) == 0 {
				continue
			}
			if id == chosen.id {
				f.skipped = 0
			} else if f.skipped < p.maxSkips {
				f.skipped++
			}
		}

		item := chosen.state.queue[0]
		chosen.state.deficit -= item.Bytes
		copy(chosen.state.queue, chosen.state.queue[1:])
		chosen.state.queue[len(chosen.state.queue)-1] = Item{}
		chosen.state.queue = chosen.state.queue[:len(chosen.state.queue)-1]
		p.queued--
		p.cursor = (chosen.index + 1) % len(p.order)
		return item, true
	}
}
