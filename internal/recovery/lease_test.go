package recovery

import (
	"errors"
	"sync"
	"testing"
)

func TestEpochLeaseFencesOldCarrierOnlyAfterCommit(t *testing.T) {
	l,err:=NewEpochLease(7);if err!=nil{t.Fatal(err)}
	if !l.Authorize(7){t.Fatal("current epoch not authorized")}
	if err:=l.Prepare(8,"carrier-b");err!=nil{t.Fatal(err)}
	if !l.Authorize(7){t.Fatal("old carrier must remain authoritative during prepare")}
	if l.Authorize(8){t.Fatal("candidate must not send application frames before commit")}
	if err:=l.Commit(8,"carrier-b");err!=nil{t.Fatal(err)}
	if l.Authorize(7){t.Fatal("old carrier was not fenced")}
	if !l.Authorize(8){t.Fatal("committed carrier not authorized")}
}

func TestEpochLeaseOnlyOneConcurrentCandidateWins(t *testing.T) {
	l,_:=NewEpochLease(1)
	start:=make(chan struct{})
	results:=make(chan error,2)
	var wg sync.WaitGroup
	for _,id:=range []string{"a","b"}{
		id:=id;wg.Add(1)
		go func(){defer wg.Done();<-start;results<-l.Prepare(2,id)}()
	}
	close(start);wg.Wait();close(results)
	var ok,conflict int
	for err:=range results{
		switch {
		case err==nil:ok++
		case errors.Is(err,ErrLeaseConflict):conflict++
		default:t.Fatalf("unexpected error: %v",err)
		}
	}
	if ok!=1||conflict!=1{t.Fatalf("ok=%d conflict=%d",ok,conflict)}
}

func TestEpochLeaseRejectsSkippedOrStaleEpoch(t *testing.T) {
	l,_:=NewEpochLease(3)
	for _,e:=range []uint64{2,3,5}{
		if err:=l.Prepare(e,"x");!errors.Is(err,ErrStaleEpoch){t.Fatalf("epoch %d err=%v",e,err)}
	}
}
