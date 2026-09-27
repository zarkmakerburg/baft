package resources

import (
	"errors"
	"sync"
)

type ControlItem struct {
	WireBytes int
	Value     any
}

type ControlQueue struct {
	mu          sync.Mutex
	items       []ControlItem
	bytes       int
	maxMessages int
	maxBytes    int
}

func NewControlQueue(maxMessages, maxBytes int) (*ControlQueue, error) {
	if maxMessages <= 0 || maxBytes <= 0 {
		return nil, errors.New("control queue limits must be positive")
	}
	return &ControlQueue{maxMessages:maxMessages,maxBytes:maxBytes}, nil
}

func NewDefaultControlQueue() *ControlQueue {
	q,_:=NewControlQueue(256,1<<20)
	return q
}

func (q *ControlQueue) Enqueue(item ControlItem) error {
	if item.WireBytes <= 0 {
		return errors.New("control item size must be positive")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) >= q.maxMessages || item.WireBytes > q.maxBytes-q.bytes {
		return ErrResourceExhausted
	}
	q.items=append(q.items,item)
	q.bytes+=item.WireBytes
	return nil
}

func (q *ControlQueue) Dequeue() (ControlItem,bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items)==0 { return ControlItem{},false }
	item:=q.items[0]
	copy(q.items,q.items[1:])
	q.items[len(q.items)-1]=ControlItem{}
	q.items=q.items[:len(q.items)-1]
	q.bytes-=item.WireBytes
	return item,true
}

func (q *ControlQueue) LenBytes() (int,int) {
	q.mu.Lock(); defer q.mu.Unlock()
	return len(q.items),q.bytes
}
