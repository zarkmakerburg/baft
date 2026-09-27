package resources

import (
	"errors"
	"testing"
)

func TestDefaultAllocatorDoesNotBorrowBetweenPools(t *testing.T) {
	a,err:=NewAllocator(DefaultLimits())
	if err!=nil { t.Fatal(err) }
	for i:=uint64(1); i<=8; i++ {
		if err:=a.Reserve(i,Receive,16*MiB);err!=nil { t.Fatal(err) }
	}
	if err:=a.Reserve(9,Receive,1);!errors.Is(err,ErrResourceExhausted) {
		t.Fatalf("expected receive pool exhaustion, got %v",err)
	}
	if err:=a.Reserve(9,Replay,16*MiB);err!=nil {
		t.Fatalf("replay pool must remain independently available: %v",err)
	}
	s:=a.Snapshot()
	if s.ReceiveUsed!=128*MiB || s.ReplayUsed!=16*MiB || s.TotalUsed!=144*MiB {
		t.Fatalf("unexpected snapshot: %#v",s)
	}
}

func TestAllocatorEnforcesPerFlowCapAndReleasesAtomically(t *testing.T) {
	a,err:=NewAllocator(DefaultLimits())
	if err!=nil { t.Fatal(err) }
	if err:=a.Reserve(1,Receive,16*MiB);err!=nil { t.Fatal(err) }
	if err:=a.Reserve(1,Receive,1);!errors.Is(err,ErrResourceExhausted) {
		t.Fatalf("expected per-flow cap, got %v",err)
	}
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
