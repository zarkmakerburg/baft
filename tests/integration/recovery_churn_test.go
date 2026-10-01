package integration_test

import (
	"fmt"
	"github.com/zarkmakerburg/baft/internal/node"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// Real traffic opens and closes Flows all the time, so a carrier usually fails
// while some Flow is mid-OPEN or in the last step of closing. Recovery must
// still converge and keep the long-lived Flow intact. Before recovery offers
// carried tombstones, such a Flow left the two snapshots with different Flow
// sets and every attempt failed until retention expired: 10 of 10 runs. A FIN
// written just after a recovery and lost with the next carrier also left the
// listener refusing every fresh epoch until it could ask the dialer to
// resolve that transaction first (RecoveryOffer.ResolvePrevious), and a FIN
// written while the transaction was still unproven was never sent at all.
func TestRecoveryConvergesWhileShortFlowsOpenAndClose(t *testing.T){
	p:=startRecoveryRuntimePair(t,1);defer p.close(t)
	var exitMu sync.Mutex
	var exits []string
	for _,side:=range []struct{name string;rt *node.Runtime}{{"IR",p.irRuntime},{"EX",p.exRuntime}}{
		name:=side.name
		side.rt.SetLogicalSessionLifecycleHookForTest(func(ev node.LogicalSessionLifecycleEvent){
			if ev.Event!="PEER_RUN_EXIT"{return}
			exitMu.Lock();exits=append(exits,fmt.Sprintf("%s session run exited: %s (epoch=%d txn=%s)",name,ev.Reason,ev.SessionEpoch,ev.TxnState));exitMu.Unlock()
		})
	}
	defer func(){
		if !t.Failed(){return}
		logRecoveryAuthorities(t,p)
		exitMu.Lock();for _,e:=range exits{t.Log(e)};exitMu.Unlock()
	}()
	c:=openRecoveryFlow(t,p);defer c.Close()
	_ = c.SetDeadline(time.Now().Add(60*time.Second))

	stop:=make(chan struct{})
	var churn sync.WaitGroup
	for w:=0;w<4;w++{
		churn.Add(1)
		go func(w int){
			defer churn.Done()
			for i:=0;;i++{
				select{case <-stop:return;default:}
				s,err:=net.DialTimeout("tcp",p.ir.Routes[0].Listen,time.Second)
				if err!=nil{time.Sleep(time.Millisecond);continue}
				// Pace the churn so the test does not exhaust ephemeral ports.
				time.Sleep(2*time.Millisecond)
				// Half of the short Flows echo a few bytes, the rest close at
				// once; errors are expected while the carrier is being replaced.
				if (i+w)%2==0{
					_ = s.SetDeadline(time.Now().Add(200*time.Millisecond))
					if _,err:=s.Write([]byte("short"));err==nil{_,_ = io.ReadFull(s,make([]byte,5))}
				}
				_ = s.Close()
			}
		}(w)
	}
	stopped:=false
	stopChurn:=func(){if !stopped{stopped=true;close(stop);churn.Wait()}}
	defer stopChurn()

	for round:=1;round<=4;round++{
		time.Sleep(time.Duration(20+round*7)*time.Millisecond)
		p.proxy.CutAll()
		payload:=make([]byte,64*1024+round*113)
		for i:=range payload{payload[i]=byte((i*29+round*13+5)%251)}
		assertEchoHashOnExistingFlow(t,c,payload)
	}

	// Every short Flow must also finish without another carrier failure. A FIN
	// withheld while the transaction was still proving itself used to leave
	// dozens of Flows half-closed, holding their slots, on both sides.
	stopChurn()
	deadline:=time.Now().Add(10*time.Second)
	for{
		ir,ex:=p.irRuntime.FlowSlots.Used(),p.exRuntime.FlowSlots.Used()
		if ir<=1&&ex<=1{break}
		if time.Now().After(deadline){t.Fatalf("short Flows did not finish after churn stopped: IR holds %d slots, EX %d (only the long Flow should remain)",ir,ex)}
		time.Sleep(20*time.Millisecond)
	}
}

// logRecoveryAuthorities records each side's recovery authority and its Flows'
// terminal state, so a failing run says which obligation was left open.
func logRecoveryAuthorities(t *testing.T,p *recoveryRuntimePair){
	t.Helper()
	for _,side:=range []struct{name string;rt *node.Runtime}{{"IR",p.irRuntime},{"EX",p.exRuntime}}{
		for _,a:=range side.rt.RecoveryAuthoritiesForTest(){
			t.Logf("%s epoch=%d gen=%d txn=%s frozen=%v usable=%v signal=%v appReady=%v stable=%v finStable=%v replayOutstanding=%v flows=%d",side.name,a.Epoch,a.CarrierGeneration,a.TxnState,a.Frozen,a.CurrentCarrierUsable,a.RecoverySignalPending,a.ApplicationReady,a.TransactionStable,a.FinStable,a.ReplayOutstanding,len(a.Flows))
			for _,f:=range a.Flows{
				t.Logf("%s   flow=%d tx=%d/%d hwm=%d finSent=%v finAcked=%v finAckSent=%v finAckConfirmed=%v",side.name,f.StreamID,f.TxAcked,f.TxNext,f.ReplayHighWatermark,f.FinSent,f.FinAcked,f.FinAckSent,f.FinAckConfirmed)
			}
		}
	}
}
