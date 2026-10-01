package resources

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

const MiB int64 = 1024 * 1024

var (
	ErrResourceExhausted = errors.New("RESOURCE_EXHAUSTED")
	ErrReservationTooLarge = errors.New("reservation exceeds configured cap")
)

type Kind uint8

const (
	Receive Kind = iota + 1
	Replay
)

type Limits struct {
	Total          int64
	Receive        int64
	Replay         int64
	PerFlowReceive int64
	PerFlowReplay  int64
}

func DefaultLimits() Limits {
	return Limits{
		Total:          256 * MiB,
		Receive:        128 * MiB,
		Replay:         128 * MiB,
		PerFlowReceive: 16 * MiB,
		PerFlowReplay:  16 * MiB,
	}
}

type flowReservation struct {
	receive int64
	replay  int64
}

type Snapshot struct {
	ReceiveUsed int64
	ReplayUsed  int64
	TotalUsed   int64
	Flows       int
}

type Allocator struct {
	mu      sync.Mutex
	limit   Limits
	flows   map[uint64]flowReservation
	recv    int64
	replay  int64
	changed chan struct{}
}

func NewAllocator(l Limits) (*Allocator, error) {
	if l.Total <= 0 || l.Receive <= 0 || l.Replay <= 0 || l.PerFlowReceive <= 0 || l.PerFlowReplay <= 0 {
		return nil, errors.New("resource limits must be positive")
	}
	if l.Receive+l.Replay > l.Total {
		return nil, errors.New("receive+replay caps exceed total budget")
	}
	if l.PerFlowReceive > l.Receive || l.PerFlowReplay > l.Replay {
		return nil, errors.New("per-flow cap exceeds pool cap")
	}
	return &Allocator{limit:l,flows:map[uint64]flowReservation{},changed:make(chan struct{})}, nil
}

func (a *Allocator) Reserve(flowID uint64, kind Kind, n int64) error {
	if err:=a.validateRequest(flowID,kind,n);err!=nil { return err }
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.reserveLocked(flowID,kind,n)
}

func (a *Allocator) ReserveContext(ctx context.Context, flowID uint64, kind Kind, n int64) error {
	if err:=a.validateRequest(flowID,kind,n);err!=nil { return err }
	for {
		a.mu.Lock()
		err:=a.reserveLocked(flowID,kind,n)
		if err==nil {
			a.mu.Unlock()
			return nil
		}
		if !errors.Is(err,ErrResourceExhausted) {
			a.mu.Unlock()
			return err
		}
		wait:=a.changed
		a.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wait:
		}
	}
}

func (a *Allocator) validateRequest(flowID uint64, kind Kind, n int64) error {
	if flowID==0 || n<=0 { return errors.New("flowID and reservation bytes must be positive") }
	switch kind {
	case Receive:
		if n>a.limit.PerFlowReceive || n>a.limit.Receive || n>a.limit.Total { return ErrReservationTooLarge }
	case Replay:
		if n>a.limit.PerFlowReplay || n>a.limit.Replay || n>a.limit.Total { return ErrReservationTooLarge }
	default:
		return fmt.Errorf("unknown reservation kind %d",kind)
	}
	return nil
}

func (a *Allocator) reserveLocked(flowID uint64, kind Kind, n int64) error {
	fr:=a.flows[flowID]
	if n>a.limit.Total-(a.recv+a.replay) { return ErrResourceExhausted }
	switch kind {
	case Receive:
		if n>a.limit.Receive-a.recv || n>a.limit.PerFlowReceive-fr.receive { return ErrResourceExhausted }
		fr.receive+=n; a.recv+=n
	case Replay:
		if n>a.limit.Replay-a.replay || n>a.limit.PerFlowReplay-fr.replay { return ErrResourceExhausted }
		fr.replay+=n; a.replay+=n
	default:
		return fmt.Errorf("unknown reservation kind %d",kind)
	}
	a.flows[flowID]=fr
	return nil
}

func (a *Allocator) CanRelease(flowID uint64, kind Kind, n int64) error {
	if flowID==0 || n<=0 { return errors.New("flowID and release bytes must be positive") }
	a.mu.Lock()
	defer a.mu.Unlock()
	fr,ok:=a.flows[flowID]
	if !ok { return errors.New("flow has no reservation") }
	switch kind {
	case Receive:
		if n>fr.receive { return errors.New("receive release exceeds reservation") }
	case Replay:
		if n>fr.replay { return errors.New("replay release exceeds reservation") }
	default:
		return fmt.Errorf("unknown reservation kind %d",kind)
	}
	return nil
}

func (a *Allocator) Release(flowID uint64, kind Kind, n int64) error {
	if flowID==0 || n<=0 { return errors.New("flowID and release bytes must be positive") }
	a.mu.Lock()
	defer a.mu.Unlock()
	fr,ok:=a.flows[flowID]
	if !ok { return errors.New("flow has no reservation") }
	switch kind {
	case Receive:
		if n>fr.receive { return errors.New("receive release exceeds reservation") }
		fr.receive-=n; a.recv-=n
	case Replay:
		if n>fr.replay { return errors.New("replay release exceeds reservation") }
		fr.replay-=n; a.replay-=n
	default:
		return fmt.Errorf("unknown reservation kind %d",kind)
	}
	if fr.receive==0 && fr.replay==0 { delete(a.flows,flowID) } else { a.flows[flowID]=fr }
	a.signalLocked()
	return nil
}

func (a *Allocator) ReleaseFlow(flowID uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	fr,ok:=a.flows[flowID]
	if !ok { return }
	a.recv-=fr.receive
	a.replay-=fr.replay
	delete(a.flows,flowID)
	a.signalLocked()
}

func (a *Allocator) Snapshot() Snapshot {
	a.mu.Lock(); defer a.mu.Unlock()
	return Snapshot{ReceiveUsed:a.recv,ReplayUsed:a.replay,TotalUsed:a.recv+a.replay,Flows:len(a.flows)}
}

func (a *Allocator) signalLocked() {
	close(a.changed)
	a.changed=make(chan struct{})
}
