package scheduler

import (
	"errors"
	"fmt"
)

type Item struct {
	FlowID uint64
	Bytes  int
	Value  any
}

type flowState struct {
	quantum int
	deficit int
	queue   []Item
}

type DRR struct {
	flows  map[uint64]*flowState
	order  []uint64
	cursor int
	queued int
}

func NewDRR() *DRR {
	return &DRR{flows:map[uint64]*flowState{}}
}

func (d *DRR) AddFlow(flowID uint64, quantumBytes int) error {
	if flowID==0 || quantumBytes<=0 { return errors.New("flowID and quantum must be positive") }
	if _,ok:=d.flows[flowID];ok { return errors.New("flow already exists") }
	d.flows[flowID]=&flowState{quantum:quantumBytes}
	d.order=append(d.order,flowID)
	return nil
}

func (d *DRR) RemoveFlow(flowID uint64) []Item {
	f,ok:=d.flows[flowID]
	if !ok { return nil }
	pending:=append([]Item(nil),f.queue...)
	d.queued-=len(f.queue)
	delete(d.flows,flowID)
	for i,id:=range d.order {
		if id==flowID {
			d.order=append(d.order[:i],d.order[i+1:]...)
			if len(d.order)==0 { d.cursor=0 } else if i<d.cursor { d.cursor-- } else if d.cursor>=len(d.order){d.cursor=0}
			break
		}
	}
	return pending
}

func (d *DRR) Enqueue(item Item) error {
	if item.FlowID==0 || item.Bytes<=0 { return errors.New("invalid data item") }
	f,ok:=d.flows[item.FlowID]
	if !ok { return fmt.Errorf("unknown flow %d",item.FlowID) }
	f.queue=append(f.queue,item)
	d.queued++
	return nil
}

func (d *DRR) Len() int { return d.queued }

func (d *DRR) Next() (Item,bool) {
	if d.queued==0 || len(d.order)==0 { return Item{},false }
	for {
		id:=d.order[d.cursor]
		f:=d.flows[id]
		d.cursor=(d.cursor+1)%len(d.order)
		if len(f.queue)==0 {
			f.deficit=0
			continue
		}
		f.deficit+=f.quantum
		item:=f.queue[0]
		if item.Bytes>f.deficit {
			continue
		}
		f.deficit-=item.Bytes
		copy(f.queue,f.queue[1:])
		f.queue[len(f.queue)-1]=Item{}
		f.queue=f.queue[:len(f.queue)-1]
		d.queued--
		return item,true
	}
}
