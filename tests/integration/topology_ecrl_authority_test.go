package integration_test

import (
    "bytes"
    "crypto/sha256"
    "errors"
    "io"
    "net"
    "os"
    "sync"
    "sync/atomic"
    "testing"
    "time"

    "github.com/zarkmakerburg/baft/internal/cluster"
    "github.com/zarkmakerburg/baft/internal/clustersync"
    "github.com/zarkmakerburg/baft/internal/config"
    "github.com/zarkmakerburg/baft/internal/node"
    "github.com/zarkmakerburg/baft/internal/protocol"
    "github.com/zarkmakerburg/baft/internal/session"
)

type topologyApplyResult struct {
    changed bool
    err error
}

func topologyECRLToken(t *testing.T,h *liveTopologyHarness,generation uint64,indexes ...int) string {
    t.Helper()
    cfgs:=make([]config.Config,0,len(indexes))
    for _,i:=range indexes{cfgs=append(cfgs,h.all[i])}
    m,err:=clustersync.ManifestFromConfigs("goldapp-baft",generation,15*time.Minute,h.now,cfgs)
    if err!=nil{t.Fatal(err)}
    tok,err:=clustersync.Seal(m,h.tokenWorkerPub,h.signPriv)
    if err!=nil{t.Fatal(err)}
    return tok
}

func topologyECRLApplyExact(t *testing.T,h *liveTopologyHarness,generation uint64,indexes ...int) {
    t.Helper()
    tok:=topologyECRLToken(t,h,generation,indexes...)
    snap,changed,err:=h.controller.ApplyToken(tok,h.now.Add(time.Second))
    if err!=nil{t.Fatalf("topology generation %d apply: %v",generation,err)}
    if !changed{t.Fatalf("topology generation %d unexpectedly no-op",generation)}
    if snap.Generation!=generation{t.Fatalf("snapshot generation=%d want=%d",snap.Generation,generation)}
    h.generation=generation
}

func topologyECRLC(t *testing.T,h *liveTopologyHarness)(*node.Runtime,*node.Runtime,*cutProxy,string) {
    t.Helper()
    id:=liveNodeID('C')
    wr,ok:=h.controller.RuntimeForNodeForTest(id)
    if !ok{t.Fatalf("worker runtime C unavailable")}
    rr:=h.remotes[2]
    if rr.runtime==nil||rr.proxy==nil{t.Fatalf("remote runtime/proxy C unavailable")}
    return wr,rr.runtime,rr.proxy,id
}

func topologyAuthority(t *testing.T,r *node.Runtime) node.RecoveryAuthoritySnapshot {
    t.Helper()
    deadline:=time.Now().Add(5*time.Second)
    for{
        s:=r.RecoveryAuthoritiesForTest()
        if len(s)==1{return s[0]}
        if time.Now().After(deadline){t.Fatalf("recovery authority unavailable: %+v",s)}
        time.Sleep(time.Millisecond)
    }
}

func waitTopologyRecoveryStable(t *testing.T,worker,remote *node.Runtime,epoch uint64)(node.RecoveryAuthoritySnapshot,node.RecoveryAuthoritySnapshot) {
    t.Helper()
    deadline:=time.Now().Add(10*time.Second)
    for{
        wa:=worker.RecoveryAuthoritiesForTest()
        ra:=remote.RecoveryAuthoritiesForTest()
        if len(wa)==1&&len(ra)==1&&wa[0].Epoch==epoch&&ra[0].Epoch==epoch&&
            wa[0].TxnState==session.RecoveryTxnFinalized&&ra[0].TxnState==session.RecoveryTxnFinalized&&
            wa[0].TransactionStable&&ra[0].TransactionStable&&wa[0].ApplicationReady&&ra[0].ApplicationReady&&
            wa[0].FinalizationStable&&ra[0].FinalizationStable&&
            !wa[0].ReplayOutstanding&&!ra[0].ReplayOutstanding&&
            !wa[0].RecoverySignalPending&&!ra[0].RecoverySignalPending&&
            wa[0].CurrentCarrierUsable&&ra[0].CurrentCarrierUsable&&
            !wa[0].Frozen&&!ra[0].Frozen {
            return wa[0],ra[0]
        }
        if time.Now().After(deadline){t.Fatalf("recovery convergence timeout epoch=%d worker=%+v remote=%+v",epoch,wa,ra)}
        time.Sleep(2*time.Millisecond)
    }
}

func assertRecoveryIdentityEqual(t *testing.T,before,after node.RecoveryAuthoritySnapshot) {
    t.Helper()
    if before.SessionID!=after.SessionID||before.Epoch!=after.Epoch||
        before.CandidateID!=after.CandidateID||before.NextEpoch!=after.NextEpoch||
        before.PlanDigest!=after.PlanDigest||before.PreparedIncarnation!=after.PreparedIncarnation||
        before.CarrierGeneration!=after.CarrierGeneration||before.TxnState!=after.TxnState||
        // The application pump may keep admitting bytes into the exact
        // transaction's replay high-watermark while credit allows, so it can
        // only grow; everything that identifies the transaction is fixed.
        after.ReplayHighWatermark<before.ReplayHighWatermark||
        before.ReplayPeerAccepted!=after.ReplayPeerAccepted||
        before.ReplayOutstanding!=after.ReplayOutstanding||
        before.FinalizationStable!=after.FinalizationStable||
        before.TransactionStable!=after.TransactionStable||
        before.RecoveryAttempts!=after.RecoveryAttempts||
        before.RecoverySignalPending!=after.RecoverySignalPending||
        before.CurrentCarrierUsable!=after.CurrentCarrierUsable {
        t.Fatalf("cross-authority mutation before=%+v after=%+v",before,after)
    }
}

func assertRecoveryTransactionPinned(t *testing.T,before,after node.RecoveryAuthoritySnapshot) {
    t.Helper()
    if before.SessionID!=after.SessionID||before.CandidateID==""||
        before.CandidateID!=after.CandidateID||before.NextEpoch!=after.NextEpoch||
        before.PlanDigest==""||before.PlanDigest!=after.PlanDigest {
        t.Fatalf("exact recovery transaction changed before=%+v after=%+v",before,after)
    }
}

func topologyPayloadHash(t *testing.T,c net.Conn,seed int) [32]byte {
    t.Helper()
    p:=make([]byte,4096+seed%997)
    for i:=range p{p[i]=byte((i*37+seed*11)%251)}
    return assertEchoHashOnExistingFlow(t,c,p)
}

func generationOf(t *testing.T,h *liveTopologyHarness) uint64 {
    t.Helper();s,ok:=h.engine.Current();if !ok{t.Fatal("committed topology unavailable")};return s.Generation
}

func setRecoveryBarrier(r *node.Runtime,stage string,returnErr error)(<-chan struct{},chan struct{}) {
    hit:=make(chan struct{});release:=make(chan struct{});var once sync.Once
    r.SetRecoveryFaultHookForTest(func(s string)error{
        if s==stage{
            first:=false
            once.Do(func(){first=true;close(hit)})
            if first{<-release;return returnErr}
        }
        return nil
    })
    return hit,release
}

func clearRecoveryHooks(worker,remote *node.Runtime) {
    worker.SetRecoveryFaultHookForTest(nil)
    remote.SetRecoveryFaultHookForTest(nil)
    worker.SetRecoveryPostCommitFaultForTest(nil)
    remote.SetRecoveryPostCommitFaultForTest(nil)
    worker.SetRecoveryFrameHookForTest(nil)
    remote.SetRecoveryFrameHookForTest(nil)
}

func assertNoUnexpectedRuntimeFailure(t *testing.T,h *liveTopologyHarness) {
    t.Helper()
    select{
    case f:=<-h.controller.RuntimeFailures():t.Fatalf("unexpected runtime failure node=%s instance=%d err=%v",f.NodeID,f.Instance,f.Err)
    default:
    }
}

func authorityIterations(normal int) int {
    if os.Getenv("BAFT_AUTHORITY_RACE_SAMPLE")=="1" { return 1 }
    return normal
}

func TestTopologyPrepareRecoveryCommitAuthorityIsolation(t *testing.T){
    h:=newLiveTopologyHarness(t);defer h.close(t)
    h.apply(t,0,1,2,3)
    worker,remote,proxy,cID:=topologyECRLC(t,h)
    flow:=liveOpenFlow(t,h,cID);defer flow.Close();_ = flow.SetDeadline(time.Now().Add(10*time.Minute))
    instance,_:=h.controller.InstanceTokenForTest(cID)
    initial:=topologyAuthority(t,worker);sessionID:=initial.SessionID
    targetAccepts:=h.remotes[2].target.accepts.Load()
    dPresent:=true

    for i:=0;i<authorityIterations(100);i++{
        oldGen:=generationOf(t,h);before:=topologyAuthority(t,worker)
        reached:=make(chan struct{});release:=make(chan struct{});var once sync.Once
        h.controller.SetTopologyHookForTest(func(stage string,g uint64){
            if stage==cluster.TopologyAfterPrepare{
                once.Do(func(){close(reached)})
                <-release
            }
        })
        next:=oldGen+1
        idx:=[]int{0,1,2};if !dPresent{idx=[]int{0,1,2,3}}
        tok:=topologyECRLToken(t,h,next,idx...)
        done:=make(chan topologyApplyResult,1)
        go func(){_,changed,err:=h.controller.ApplyToken(tok,h.now.Add(time.Second));done<-topologyApplyResult{changed:changed,err:err}}()
        select{case <-reached:case <-time.After(5*time.Second):t.Fatal("topology prepare barrier not reached")}
        if got:=generationOf(t,h);got!=oldGen{t.Fatalf("generation published before runtime reconciliation got=%d want=%d",got,oldGen)}

        proxy.CutAll()
        recovered,_:=waitTopologyRecoveryStable(t,worker,remote,before.Epoch+1)
        if recovered.SessionID!=sessionID||recovered.Epoch!=before.Epoch+1{t.Fatalf("recovery authority wrong before=%+v after=%+v",before,recovered)}
        if got:=generationOf(t,h);got!=oldGen{t.Fatalf("recovery mutated ClusterGeneration got=%d want=%d",got,oldGen)}

        close(release)
        res:=<-done;if res.err!=nil||!res.changed{t.Fatalf("topology commit err=%v changed=%v",res.err,res.changed)}
        h.generation=next;h.controller.SetTopologyHookForTest(nil)
        after:=topologyAuthority(t,worker)
        if after.Epoch!=recovered.Epoch||after.SessionID!=recovered.SessionID{t.Fatalf("topology double-advanced recovery before=%+v after=%+v",recovered,after)}
        if got,_:=h.controller.InstanceTokenForTest(cID);got!=instance{t.Fatalf("C runtime restarted %d -> %d",instance,got)}
        if generationOf(t,h)!=next{t.Fatalf("generation not committed")}
        if h.remotes[2].target.accepts.Load()!=targetAccepts{t.Fatalf("target reopened")}
        topologyPayloadHash(t,flow,i)
        dPresent=!dPresent
    }
}

func TestTopologyCommitDuringRecoveryPrepared(t *testing.T){
    h:=newLiveTopologyHarness(t);defer h.close(t);h.apply(t,0,1,2,3)
    worker,remote,proxy,cID:=topologyECRLC(t,h)
    flow:=liveOpenFlow(t,h,cID);defer flow.Close();_ = flow.SetDeadline(time.Now().Add(10*time.Minute))
    instance,_:=h.controller.InstanceTokenForTest(cID);target:=h.remotes[2].target.accepts.Load()
    dPresent:=true
    for i:=0;i<authorityIterations(100);i++{
        before:=topologyAuthority(t,worker);hit,release:=setRecoveryBarrier(worker,"before_commit",nil)
        proxy.CutAll();select{case <-hit:case <-time.After(8*time.Second):t.Fatal("PREPARED barrier not reached")}
        mid:=topologyAuthority(t,worker)
        if mid.CandidateID==""||mid.PlanDigest==""{t.Fatalf("prepared identity unavailable %+v",mid)}
        next:=generationOf(t,h)+1;idx:=[]int{0,1,2};if !dPresent{idx=[]int{0,1,2,3}}
        topologyECRLApplyExact(t,h,next,idx...)
        afterTopo:=topologyAuthority(t,worker);assertRecoveryIdentityEqual(t,mid,afterTopo)
        if got,_:=h.controller.InstanceTokenForTest(cID);got!=instance{t.Fatal("C runtime changed during PREPARED overlap")}
        close(release);final,_:=waitTopologyRecoveryStable(t,worker,remote,before.Epoch+1);clearRecoveryHooks(worker,remote)
        assertRecoveryTransactionPinned(t,mid,final)
        if generationOf(t,h)!=next{t.Fatal("recovery changed ClusterGeneration")}
        if h.remotes[2].target.accepts.Load()!=target{t.Fatal("target reopened")}
        topologyPayloadHash(t,flow,1000+i);dPresent=!dPresent
    }
}

func TestTopologyCommitDuringCommitUncertainty(t *testing.T){
    h:=newLiveTopologyHarness(t);defer h.close(t);h.apply(t,0,1,2,3)
    worker,remote,proxy,cID:=topologyECRLC(t,h)
    flow:=liveOpenFlow(t,h,cID);defer flow.Close();_ = flow.SetDeadline(time.Now().Add(10*time.Minute))
    instance,_:=h.controller.InstanceTokenForTest(cID);target:=h.remotes[2].target.accepts.Load();dPresent:=true
    for i:=0;i<authorityIterations(100);i++{
        before:=topologyAuthority(t,worker)
        var failOnce atomic.Bool
        remote.SetRecoveryFaultHookForTest(func(stage string)error{
            if stage=="after_listener_publish_before_commit_ack"&&failOnce.CompareAndSwap(false,true){return errors.New("authority-isolation forced commit uncertainty")}
            return nil
        })
        hit,release:=setRecoveryBarrier(worker,"status_query_after_send",nil)
        proxy.CutAll();select{case <-hit:case <-time.After(8*time.Second):t.Fatal("COMMIT_UNCERTAIN barrier not reached")}
        mid:=topologyAuthority(t,worker)
        if mid.TxnState!=session.RecoveryTxnUncertain{t.Fatalf("want COMMIT_UNCERTAIN got %+v",mid)}
        next:=generationOf(t,h)+1;idx:=[]int{0,1,2};if !dPresent{idx=[]int{0,1,2,3}}
        topologyECRLApplyExact(t,h,next,idx...)
        afterTopo:=topologyAuthority(t,worker);assertRecoveryIdentityEqual(t,mid,afterTopo)
        close(release);final,_:=waitTopologyRecoveryStable(t,worker,remote,before.Epoch+1);clearRecoveryHooks(worker,remote)
        assertRecoveryTransactionPinned(t,mid,final)
        if final.Epoch!=before.Epoch+1{t.Fatalf("fresh recovery escape before=%+v final=%+v",before,final)}
        if generationOf(t,h)!=next{t.Fatal("recovery changed ClusterGeneration")}
        if got,_:=h.controller.InstanceTokenForTest(cID);got!=instance{t.Fatal("C runtime changed")}
        if h.remotes[2].target.accepts.Load()!=target{t.Fatal("target reopened")}
        topologyPayloadHash(t,flow,2000+i);dPresent=!dPresent
    }
}

func TestTopologyCommitDuringFinalizationUncertainty(t *testing.T){
    h:=newLiveTopologyHarness(t);defer h.close(t);h.apply(t,0,1,2,3)
    worker,remote,proxy,cID:=topologyECRLC(t,h)
    flow:=liveOpenFlow(t,h,cID);defer flow.Close();_ = flow.SetDeadline(time.Now().Add(10*time.Minute))
    instance,_:=h.controller.InstanceTokenForTest(cID);target:=h.remotes[2].target.accepts.Load();dPresent:=true
    for i:=0;i<authorityIterations(100);i++{
        before:=topologyAuthority(t,worker)
        finHit,finRelease:=setRecoveryBarrier(remote,"before_listener_finalize_process",errors.New("authority-isolation finalization uncertainty"))
        statusHit,statusRelease:=setRecoveryBarrier(worker,"status_query_after_send",nil)
        proxy.CutAll()
        select{case <-finHit:case <-time.After(8*time.Second):t.Fatal("finalize barrier not reached")}
        proxy.CutAll();close(finRelease)
        select{case <-statusHit:case <-time.After(8*time.Second):t.Fatalf("finalization resolution barrier not reached worker=%+v remote=%+v",worker.RecoveryAuthoritiesForTest(),remote.RecoveryAuthoritiesForTest())}
        mid:=topologyAuthority(t,worker)
        if mid.TxnState!=session.RecoveryTxnFinalizationUncertain && mid.TxnState!=session.RecoveryTxnUncertain{
            t.Fatalf("expected unresolved finalization authority got %+v",mid)
        }
        next:=generationOf(t,h)+1;idx:=[]int{0,1,2};if !dPresent{idx=[]int{0,1,2,3}}
        topologyECRLApplyExact(t,h,next,idx...)
        afterTopo:=topologyAuthority(t,worker);assertRecoveryIdentityEqual(t,mid,afterTopo)
        close(statusRelease);final,_:=waitTopologyRecoveryStable(t,worker,remote,before.Epoch+1);clearRecoveryHooks(worker,remote)
        assertRecoveryTransactionPinned(t,mid,final)
        if generationOf(t,h)!=next{t.Fatal("recovery changed ClusterGeneration")}
        if got,_:=h.controller.InstanceTokenForTest(cID);got!=instance{t.Fatal("C runtime changed")}
        if h.remotes[2].target.accepts.Load()!=target{t.Fatal("target reopened")}
        topologyPayloadHash(t,flow,3000+i);dPresent=!dPresent
    }
}

func TestTopologyMutationDuringReplayOutstanding(t *testing.T){
    h:=newLiveTopologyHarness(t);defer h.close(t);h.apply(t,0,1,2,3)
    worker,remote,proxy,cID:=topologyECRLC(t,h)
    flow:=liveOpenFlow(t,h,cID);defer flow.Close();_ = flow.SetDeadline(time.Now().Add(15*time.Minute))
    instance,_:=h.controller.InstanceTokenForTest(cID);target:=h.remotes[2].target.accepts.Load();dPresent:=true

    for iter:=0;iter<authorityIterations(100);iter++{
        var mode atomic.Int32
        mode.Store(1)
        oldBlocked:=make(chan struct{});oldRelease:=make(chan struct{})
        replayBlocked:=make(chan struct{});replayRelease:=make(chan struct{})
        var oldOnce,replayOnce sync.Once
        remote.SetRecoveryFrameHookForTest(func(stage string,fr protocol.Frame)bool{
            if stage!="before_data_accept"{return false}
            switch mode.Load(){
            case 1:
                oldOnce.Do(func(){close(oldBlocked)})
                <-oldRelease
                return true
            case 2:
                replayOnce.Do(func(){close(replayBlocked)})
                <-replayRelease
                return true
            default:
                return false
            }
        })

        replayWritten:=make(chan struct{});writerRelease:=make(chan struct{});var writeOnce sync.Once
        worker.SetRecoveryPostCommitFaultForTest(func(stage string)error{
            if stage=="after_replay_write"{
                first:=false
                writeOnce.Do(func(){first=true;close(replayWritten)})
                if first{
                    <-writerRelease
                    return errors.New("authority-isolation recut after replay write before peer acceptance")
                }
            }
            return nil
        })

        payload:=make([]byte,3*protocol.MaxPayloadSize+777)
        for i:=range payload{payload[i]=byte((i*19+iter*13)%251)}
        want:=sha256.Sum256(payload);got:=make([]byte,len(payload))
        rd:=make(chan error,1);wr:=make(chan error,1)
        go func(){_,err:=io.ReadFull(flow,got);rd<-err}()
        go func(){
            for off:=0;off<len(payload);{
                n,err:=flow.Write(payload[off:])
                if err!=nil{wr<-err;return}
                if n<=0{wr<-io.ErrShortWrite;return}
                off+=n
            }
            wr<-nil
        }()

        select{case <-oldBlocked:case <-time.After(8*time.Second):t.Fatal("old DATA barrier not reached")}
        beforeFlow:=waitSingleFlowFrontier(t,worker,func(f session.RecoveryFlowFrontier)bool{
            return f.TxNext>=f.PeerAccepted+protocol.MaxPayloadSize
        })
        if beforeFlow.PeerAccepted>=beforeFlow.TxNext{t.Fatalf("expected unaccepted old-carrier bytes frontier=%+v",beforeFlow)}
        before:=topologyAuthority(t,worker)

        mode.Store(2)
        proxy.CutAll()
        close(oldRelease)

        select{case <-replayBlocked:case <-time.After(8*time.Second):t.Fatal("replacement replay accept barrier not reached")}
        select{case <-replayWritten:case <-time.After(8*time.Second):t.Fatal("replacement replay write barrier not reached")}

        mid:=topologyAuthority(t,worker)
        if mid.Epoch!=before.Epoch+1||mid.CandidateID==""||mid.PlanDigest==""{
            t.Fatalf("Class-A recovery authority unavailable before topology commit before=%+v mid=%+v",before,mid)
        }
        if !mid.ReplayOutstanding||mid.TransactionStable||mid.ReplayHighWatermark<=mid.ReplayPeerAccepted{
            t.Fatalf("replay obligation not outstanding %+v",mid)
        }
        if len(mid.Flows)!=1{t.Fatalf("flow frontier count=%d",len(mid.Flows))}
        f:=mid.Flows[0]
        if f.TxAcked>f.ReplayHighWatermark||f.ReplayHighWatermark>f.TxNext{t.Fatalf("frontier invariant violated %+v",f)}
        candidate,digest:=mid.CandidateID,mid.PlanDigest

        next:=generationOf(t,h)+1;idx:=[]int{0,1,2};if !dPresent{idx=[]int{0,1,2,3}}
        topologyECRLApplyExact(t,h,next,idx...)
        afterTopo:=topologyAuthority(t,worker)
        assertRecoveryIdentityEqual(t,mid,afterTopo)
        if afterTopo.TransactionStable{t.Fatal("topology commit made recovery stable")}

        // The replay write has no peer proof. Recut that physical carrier and
        // force the existing exact transaction to rebind; topology must not
        // create a fresh recovery transaction or authorize a second epoch.
        proxy.CutAll()
        mode.Store(3)
        close(replayRelease)
        close(writerRelease)

        select{case err:=<-wr:if err!=nil{t.Fatalf("write: %v",err)};case <-time.After(12*time.Second):t.Fatal("write timeout")}
        select{case err:=<-rd:if err!=nil{t.Fatalf("read: %v",err)};case <-time.After(20*time.Second):t.Fatal("read timeout")}
        have:=sha256.Sum256(got);if !bytes.Equal(got,payload)||have!=want{t.Fatalf("payload gap/dup got=%x want=%x",have,want)}

        final,_:=waitTopologyRecoveryStable(t,worker,remote,before.Epoch+1)
        clearRecoveryHooks(worker,remote)
        if final.SessionID!=before.SessionID||final.Epoch!=before.Epoch+1||
            final.CandidateID!=candidate||final.PlanDigest!=digest{
            t.Fatalf("exact Class-A transaction escaped mid=%+v final=%+v",mid,final)
        }
        if generationOf(t,h)!=next{t.Fatal("recovery changed ClusterGeneration")}
        if gotInst,_:=h.controller.InstanceTokenForTest(cID);gotInst!=instance{t.Fatal("C runtime changed")}
        if h.remotes[2].target.accepts.Load()!=target{t.Fatal("target reopened")}
        dPresent=!dPresent
    }
}

func TestTopologyPermutationDuringRecoveryNoChurn(t *testing.T){
    h:=newLiveTopologyHarness(t);defer h.close(t);h.apply(t,0,1,2,3)
    worker,remote,proxy,cID:=topologyECRLC(t,h)
    flow:=liveOpenFlow(t,h,cID);defer flow.Close();_ = flow.SetDeadline(time.Now().Add(10*time.Minute))
    instance,_:=h.controller.InstanceTokenForTest(cID);target:=h.remotes[2].target.accepts.Load()
    for i:=0;i<authorityIterations(50);i++{
        before:=topologyAuthority(t,worker);hit,release:=setRecoveryBarrier(worker,"before_commit",nil)
        proxy.CutAll();select{case <-hit:case <-time.After(8*time.Second):t.Fatal("recovery PREPARED barrier not reached")}
        mid:=topologyAuthority(t,worker);next:=generationOf(t,h)+1
        topologyECRLApplyExact(t,h,next,3,0,2,1)
        afterTopo:=topologyAuthority(t,worker);assertRecoveryIdentityEqual(t,mid,afterTopo)
        if got,_:=h.controller.InstanceTokenForTest(cID);got!=instance{t.Fatal("permutation churned C runtime")}
        close(release);final,_:=waitTopologyRecoveryStable(t,worker,remote,before.Epoch+1);clearRecoveryHooks(worker,remote)
        assertRecoveryTransactionPinned(t,mid,final)
        if h.remotes[2].target.accepts.Load()!=target{t.Fatal("target reopened")}
        topologyPayloadHash(t,flow,4000+i)
    }
}

func TestTopologyAddUnrelatedNodeDuringRecovery(t *testing.T){
    h:=newLiveTopologyHarness(t);defer h.close(t);h.apply(t,0,1,2)
    worker,remote,proxy,cID:=topologyECRLC(t,h)
    flow:=liveOpenFlow(t,h,cID);defer flow.Close();_ = flow.SetDeadline(time.Now().Add(10*time.Minute))
    instance,_:=h.controller.InstanceTokenForTest(cID);target:=h.remotes[2].target.accepts.Load()
    for i:=0;i<authorityIterations(50);i++{
        before:=topologyAuthority(t,worker);hit,release:=setRecoveryBarrier(worker,"before_commit",nil)
        proxy.CutAll();select{case <-hit:case <-time.After(8*time.Second):t.Fatal("recovery PREPARED barrier not reached")}
        mid:=topologyAuthority(t,worker)
        topologyECRLApplyExact(t,h,generationOf(t,h)+1,0,1,2,3)
        afterTopo:=topologyAuthority(t,worker);assertRecoveryIdentityEqual(t,mid,afterTopo)
        if got,_:=h.controller.InstanceTokenForTest(cID);got!=instance{t.Fatal("C runtime restarted on add")}
        close(release);final,_:=waitTopologyRecoveryStable(t,worker,remote,before.Epoch+1);clearRecoveryHooks(worker,remote)
        assertRecoveryTransactionPinned(t,mid,final)
        topologyPayloadHash(t,flow,5000+i)
        if h.remotes[2].target.accepts.Load()!=target{t.Fatal("target reopened")}
        attempts:=final.RecoveryAttempts
        topologyECRLApplyExact(t,h,generationOf(t,h)+1,0,1,2)
        afterReset:=topologyAuthority(t,worker)
        if afterReset.RecoveryAttempts!=attempts||afterReset.Epoch!=final.Epoch{t.Fatalf("topology-only reset induced recovery before=%+v after=%+v",final,afterReset)}
    }
}

func TestTopologyRemoveUnrelatedNodeDuringRecovery(t *testing.T){
    h:=newLiveTopologyHarness(t);defer h.close(t);h.apply(t,0,1,2,3)
    worker,remote,proxy,cID:=topologyECRLC(t,h)
    flow:=liveOpenFlow(t,h,cID);defer flow.Close();_ = flow.SetDeadline(time.Now().Add(10*time.Minute))
    instance,_:=h.controller.InstanceTokenForTest(cID);target:=h.remotes[2].target.accepts.Load()
    for i:=0;i<authorityIterations(50);i++{
        before:=topologyAuthority(t,worker);hit,release:=setRecoveryBarrier(worker,"before_commit",nil)
        proxy.CutAll();select{case <-hit:case <-time.After(8*time.Second):t.Fatal("recovery PREPARED barrier not reached")}
        mid:=topologyAuthority(t,worker)
        topologyECRLApplyExact(t,h,generationOf(t,h)+1,0,1,2)
        afterTopo:=topologyAuthority(t,worker);assertRecoveryIdentityEqual(t,mid,afterTopo)
        assertNoUnexpectedRuntimeFailure(t,h)
        if got,_:=h.controller.InstanceTokenForTest(cID);got!=instance{t.Fatal("C runtime restarted on remove")}
        close(release);final,_:=waitTopologyRecoveryStable(t,worker,remote,before.Epoch+1);clearRecoveryHooks(worker,remote)
        assertRecoveryTransactionPinned(t,mid,final)
        topologyPayloadHash(t,flow,6000+i)
        if h.remotes[2].target.accepts.Load()!=target{t.Fatal("target reopened")}
        attempts:=final.RecoveryAttempts
        topologyECRLApplyExact(t,h,generationOf(t,h)+1,0,1,2,3)
        afterReset:=topologyAuthority(t,worker)
        if afterReset.RecoveryAttempts!=attempts||afterReset.Epoch!=final.Epoch{t.Fatalf("topology-only reset induced recovery")}
    }
}

func TestStaleTopologyCommitCannotAffectRecovery(t *testing.T){
    h:=newLiveTopologyHarness(t);defer h.close(t);h.apply(t,0,1,2,3)
    worker,_,_,cID:=topologyECRLC(t,h)
    flow:=liveOpenFlow(t,h,cID);defer flow.Close();_ = flow.SetDeadline(time.Now().Add(5*time.Minute))
    instance,_:=h.controller.InstanceTokenForTest(cID)
    for i:=0;i<authorityIterations(100);i++{
        before:=topologyAuthority(t,worker);g:=generationOf(t,h)
        staleTok:=topologyECRLToken(t,h,g+1,0,1,2,3)
        prepared,err:=h.engine.PrepareToken(staleTok,h.now.Add(time.Second));if err!=nil{t.Fatal(err)}
        topologyECRLApplyExact(t,h,g+2,3,2,1,0)
        if _,_,err:=h.engine.CommitPrepared(prepared);!errors.Is(err,clustersync.ErrPreparedStale){t.Fatalf("stale topology commit accepted err=%v",err)}
        after:=topologyAuthority(t,worker);assertRecoveryIdentityEqual(t,before,after)
        if generationOf(t,h)!=g+2{t.Fatal("stale topology changed current generation")}
        if got,_:=h.controller.InstanceTokenForTest(cID);got!=instance{t.Fatal("stale topology changed runtime")}
        topologyPayloadHash(t,flow,7000+i)
    }
}

func TestStaleRecoveryCallbackCannotAffectTopology(t *testing.T){
    h:=newLiveTopologyHarness(t);defer h.close(t);h.apply(t,0,1,2,3)
    worker,remote,proxy,cID:=topologyECRLC(t,h)
    flow:=liveOpenFlow(t,h,cID);defer flow.Close();_ = flow.SetDeadline(time.Now().Add(15*time.Minute))
    instance,_:=h.controller.InstanceTokenForTest(cID);target:=h.remotes[2].target.accepts.Load()
    for i:=0;i<authorityIterations(100);i++{
        a:=topologyAuthority(t,worker)
        proxy.CutAll();stable1,_:=waitTopologyRecoveryStable(t,worker,remote,a.Epoch+1)
        oldPrep,err:=worker.RecoveryPreparedOwnershipForTest();if err!=nil{t.Fatal(err)}
        proxy.CutAll();stable2,_:=waitTopologyRecoveryStable(t,worker,remote,stable1.Epoch+1)
        next:=generationOf(t,h)+1
        topologyECRLApplyExact(t,h,next,3,0,2,1)
        beforeCallback:=topologyAuthority(t,worker)
        err=worker.StaleFinalizeFailureForTest(oldPrep,session.ErrCarrierUnavailable)
        if !errors.Is(err,session.ErrStaleRecoveryIncarnation){t.Fatalf("stale ECRL ownership accepted err=%v old=%+v current=%+v",err,oldPrep,beforeCallback)}
        after:=topologyAuthority(t,worker)
        if after.Epoch!=beforeCallback.Epoch||after.CandidateID!=beforeCallback.CandidateID||
            after.PlanDigest!=beforeCallback.PlanDigest||after.PreparedIncarnation!=beforeCallback.PreparedIncarnation||
            after.CarrierGeneration!=beforeCallback.CarrierGeneration||after.PostCommitFailures!=beforeCallback.PostCommitFailures{
            t.Fatalf("stale callback mutated ECRL authority before=%+v after=%+v",beforeCallback,after)
        }
        if generationOf(t,h)!=next{t.Fatal("stale recovery callback changed ClusterGeneration")}
        if stable2.SessionID!=after.SessionID{t.Fatal("SessionID changed")}
        if got,_:=h.controller.InstanceTokenForTest(cID);got!=instance{t.Fatal("C runtime changed")}
        if h.remotes[2].target.accepts.Load()!=target{t.Fatal("target reopened")}
        topologyPayloadHash(t,flow,8000+i)
    }
}
