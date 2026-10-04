package resources

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDefaultAllocatorDoesNotBorrowBetweenPools(t *testing.T) {
	a,err:=NewAllocator(DefaultLimits()); if err!=nil { t.Fatal(err) }
	for i:=uint64(1);i<=8;i++ { if err:=a.Reserve(i,Receive,16*MiB);err!=nil { t.Fatal(err) } }
	if err:=a.Reserve(9,Receive,1);!errors.Is(err,ErrResourceExhausted) { t.Fatalf("expected receive pool exhaustion, got %v",err) }
	if err:=a.Reserve(9,Replay,16*MiB);err!=nil { t.Fatalf("replay pool must remain independently available: %v",err) }
	s:=a.Snapshot()
	if s.ReceiveUsed!=128*MiB||s.ReplayUsed!=16*MiB||s.TotalUsed!=144*MiB { t.Fatalf("unexpected snapshot: %#v",s) }
}

func TestAllocatorEnforcesPerFlowCapAndReleasesAtomically(t *testing.T) {
	a,err:=NewAllocator(DefaultLimits()); if err!=nil { t.Fatal(err) }
	if err:=a.Reserve(1,Receive,16*MiB);err!=nil { t.Fatal(err) }
	if err:=a.Reserve(1,Receive,1);!errors.Is(err,ErrResourceExhausted) { t.Fatalf("expected per-flow cap, got %v",err) }
	if err:=a.Reserve(1,Replay,4*MiB);err!=nil { t.Fatal(err) }
	a.ReleaseFlow(1)
	if s:=a.Snapshot();s.TotalUsed!=0||s.Flows!=0 { t.Fatalf("leaked reservation: %#v",s) }
}

func TestAllocatorRejectsOverRelease(t *testing.T) {
	a,_:=NewAllocator(DefaultLimits())
	if err:=a.Reserve(1,Replay,1024);err!=nil { t.Fatal(err) }
	if err:=a.Release(1,Replay,2048);err==nil { t.Fatal("expected over-release rejection") }
	if s:=a.Snapshot();s.ReplayUsed!=1024 { t.Fatalf("state changed on rejected release: %#v",s) }
}

func TestReserveContextBackpressuresUntilRelease(t *testing.T) {
	l:=Limits{Total:2048,Receive:1024,Replay:1024,PerFlowReceive:1024,PerFlowReplay:1024}
	a,err:=NewAllocator(l);if err!=nil { t.Fatal(err) }
	if err:=a.Reserve(1,Replay,1024);err!=nil { t.Fatal(err) }
	ctx,cancel:=context.WithTimeout(context.Background(),time.Second);defer cancel()
	done:=make(chan error,1)
	go func(){done<-a.ReserveContext(ctx,2,Replay,512)}()
	select {
	case err:=<-done: t.Fatalf("reservation should block, got %v",err)
	case <-time.After(25*time.Millisecond):
	}
	if err:=a.Release(1,Replay,1024);err!=nil { t.Fatal(err) }
	select {
	case err:=<-done: if err!=nil { t.Fatal(err) }
	case <-time.After(time.Second): t.Fatal("blocked reservation was not released")
	}
}

func TestReserveIfFreeKeepsHeadroom(t *testing.T) {
	a, err := NewAllocator(Limits{Total: 200, Receive: 100, Replay: 100, PerFlowReceive: 100, PerFlowReplay: 100})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ReserveIfFree(1, Receive, 40, 50); err != nil {
		t.Fatal(err)
	}
	if err := a.ReserveIfFree(1, Receive, 20, 50); !errors.Is(err, ErrResourceExhausted) {
		t.Fatalf("reservation into headroom: %v", err)
	}
	if err := a.Reserve(2, Receive, 60); err != nil {
		t.Fatalf("plain reservation must still use the headroom: %v", err)
	}
	if err := a.ReserveIfFree(1, Receive, 1, -1); err == nil {
		t.Fatal("negative keepFree accepted")
	}
	if s := a.Snapshot(); s.ReceiveUsed != 100 {
		t.Fatalf("receive used=%d", s.ReceiveUsed)
	}
}
