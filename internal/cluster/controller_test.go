package cluster

import (
    "context"
    "crypto/ecdh"
    "crypto/ed25519"
    "errors"
    "fmt"
    "math/rand"
    "reflect"
    "sort"
    "sync"
    "testing"
    "time"

    "github.com/zarkmakerburg/baft/internal/clustersync"
    "github.com/zarkmakerburg/baft/internal/config"
    "github.com/zarkmakerburg/baft/internal/recordshape"
    "github.com/zarkmakerburg/baft/internal/securityinternal"
)

type fakeRuntimeTracker struct {
    mu sync.Mutex
    starts map[string]int
    stops map[string]int
    failNode string
}

func newFakeRuntimeTracker()*fakeRuntimeTracker{return &fakeRuntimeTracker{starts:map[string]int{},stops:map[string]int{}}}
func (t *fakeRuntimeTracker) snapshot()(map[string]int,map[string]int){
    t.mu.Lock();defer t.mu.Unlock()
    a,b:=map[string]int{},map[string]int{}
    for k,v:=range t.starts{a[k]=v};for k,v:=range t.stops{b[k]=v}
    return a,b
}

type fakeManagedRunner struct{
    tracker *fakeRuntimeTracker
    ready chan struct{}
    readyOnce sync.Once
    mu sync.Mutex
    readyErr error
}
func newFakeManagedRunner(t *fakeRuntimeTracker)*fakeManagedRunner{return &fakeManagedRunner{tracker:t,ready:make(chan struct{})}}
func (r *fakeManagedRunner) Run(ctx context.Context,cfg config.Config) error{
    id:=cfg.Peer.AllowedIdentity
    r.tracker.mu.Lock();r.tracker.starts[id]++;fail:=r.tracker.failNode==id;r.tracker.mu.Unlock()
    if fail{
        err:=errors.New("synthetic startup failure")
        r.mu.Lock();r.readyErr=err;r.mu.Unlock();r.readyOnce.Do(func(){close(r.ready)})
        return err
    }
    r.readyOnce.Do(func(){close(r.ready)})
    <-ctx.Done()
    r.tracker.mu.Lock();r.tracker.stops[id]++;r.tracker.mu.Unlock()
    return nil
}
func (r *fakeManagedRunner) DialerReady()<-chan struct{}{return r.ready}
func (r *fakeManagedRunner) DialerReadiness()error{r.mu.Lock();defer r.mu.Unlock();return r.readyErr}

type controllerFixture struct{
    c *WorkerController
    tokenWorkerPub *ecdh.PublicKey
    signPriv ed25519.PrivateKey
    now time.Time
    tracker *fakeRuntimeTracker
}

func testControllerConfigs(t *testing.T,n int)[]config.Config{
    t.Helper()
    out:=make([]config.Config,0,n)
    for i:=0;i<n;i++{
        kp,err:=securityinternal.GenerateKeyPair();if err!=nil{t.Fatal(err)}
        pub,err:=securityinternal.EncodePublicKey(kp.Public);if err!=nil{t.Fatal(err)}
        id:=fmt.Sprintf("urn:baft:node:%c",'A'+rune(i))
        out=append(out,config.Config{
            SchemaVersion:config.SchemaVersion,
            Noise:&config.Noise{KeyFile:"/tmp/worker-noise",PeerPublicKey:pub,RecordShaping:recordshape.Config{}},
            Node:config.Node{ID:"urn:baft:node:worker",Role:"dialer"},
            Peer:&config.Peer{Address:fmt.Sprintf("192.0.2.%d:443",20+i),ServerName:fmt.Sprintf("ex-%c.test",'A'+rune(i)),AllowedIdentity:id},
            TLS:config.TLS{MinVersion:"1.3",CAFile:"/tmp/ca",CertFile:"/tmp/cert",KeyFile:"/tmp/key"},
            Transport:config.Transport{Primary:"h2",Shards:1,Profile:"secure-fast"},
            Limits:config.Limits{MaxFlows:64,DataMemoryMiB:64,ReceiveInitialKiB:64,ReceiveMaxMiB:8,ReplayMaxMiB:8},
            Recovery:config.Recovery{Enabled:false,RetentionSeconds:30},
            Routes:[]config.Route{{ID:"service-main",Listen:fmt.Sprintf("127.0.0.1:%d",15000+i),RemoteRoute:"service-main",Direction:"outbound"}},
            Management:config.Management{UnixSocket:fmt.Sprintf("/tmp/source-%d.sock",i),MetricsListen:fmt.Sprintf("127.0.0.1:%d",9300+i)},
            Logging:config.Logging{Level:"info"},
        })
    }
    return out
}

func cloneControllerConfigs(in []config.Config)[]config.Config{
    out:=make([]config.Config,len(in))
    for i:=range in{
        out[i]=in[i]
        if in[i].Peer!=nil{x:=*in[i].Peer;out[i].Peer=&x}
        if in[i].Noise!=nil{x:=*in[i].Noise;out[i].Noise=&x}
        out[i].Routes=append([]config.Route(nil),in[i].Routes...)
    }
    return out
}

func newControllerFixture(t *testing.T)*controllerFixture{
    t.Helper()
    workerPriv,workerPub,err:=clustersync.GenerateWorkerKeyPair();if err!=nil{t.Fatal(err)}
    signPub,signPriv,err:=clustersync.GenerateSigningKeyPair();if err!=nil{t.Fatal(err)}
    engine,err:=clustersync.NewEngine(workerPriv,signPub,"goldapp-baft");if err!=nil{t.Fatal(err)}
    tmpl:=clustersync.WorkerTemplate{
        NodeID:"urn:baft:node:worker",NoiseKeyFile:"/tmp/worker-noise",
        TLS:config.TLS{MinVersion:"1.3",CAFile:"/tmp/ca",CertFile:"/tmp/cert",KeyFile:"/tmp/key"},
        Limits:config.Limits{MaxFlows:64,DataMemoryMiB:64,ReceiveInitialKiB:64,ReceiveMaxMiB:8,ReplayMaxMiB:8},
        RouteBasePort:16000,MetricsBasePort:9400,StateDir:"/tmp/baft-worker",
    }
    c,err:=NewWorkerController(context.Background(),engine,tmpl);if err!=nil{t.Fatal(err)}
    tracker:=newFakeRuntimeTracker()
    c.SetRuntimeFactoryForTest(func() managedRuntimeRunner{return newFakeManagedRunner(tracker)})
    return &controllerFixture{c:c,tokenWorkerPub:workerPub,signPriv:signPriv,now:time.Unix(1700000000,0),tracker:tracker}
}

func (f *controllerFixture) token(t *testing.T,g uint64,cfgs []config.Config)string{
    t.Helper()
    m,err:=clustersync.ManifestFromConfigs("goldapp-baft",g,15*time.Minute,f.now,cfgs);if err!=nil{t.Fatal(err)}
    tok,err:=clustersync.Seal(m,f.tokenWorkerPub,f.signPriv);if err!=nil{t.Fatal(err)}
    return tok
}

func instanceMap(t *testing.T,c *WorkerController,ids []string)map[string]uint64{
    t.Helper();out:=map[string]uint64{}
    for _,id:=range ids{v,ok:=c.InstanceTokenForTest(id);if !ok{t.Fatalf("missing instance %s",id)};out[id]=v}
    return out
}
func cfgIDs(cfgs []config.Config)[]string{out:=make([]string,0,len(cfgs));for _,c:=range cfgs{out=append(out,c.Peer.AllowedIdentity)};sort.Strings(out);return out}

func TestLiveTopologyPermutationNoRuntimeChurn(t *testing.T){
    for iter:=0;iter<100;iter++{
        f:=newControllerFixture(t);cfgs:=testControllerConfigs(t,4);ids:=cfgIDs(cfgs)
        if _,_,err:=f.c.ApplyToken(f.token(t,1,cfgs),f.now);err!=nil{t.Fatal(err)}
        before:=instanceMap(t,f.c,ids);starts0,stops0:=f.tracker.snapshot()
        p:=rand.New(rand.NewSource(int64(9000+iter))).Perm(len(cfgs));permuted:=make([]config.Config,len(cfgs));for i,j:=range p{permuted[i]=cfgs[j]}
        if _,changed,err:=f.c.ApplyToken(f.token(t,2,permuted),f.now);err!=nil||!changed{t.Fatalf("iter=%d changed=%v err=%v",iter,changed,err)}
        after:=instanceMap(t,f.c,ids);starts1,stops1:=f.tracker.snapshot()
        if !reflect.DeepEqual(before,after)||!reflect.DeepEqual(starts0,starts1)||!reflect.DeepEqual(stops0,stops1){t.Fatalf("iter=%d churn before=%v after=%v starts=%v/%v stops=%v/%v",iter,before,after,starts0,starts1,stops0,stops1)}
        if f.c.CurrentGeneration()!=2{t.Fatalf("iter=%d generation=%d",iter,f.c.CurrentGeneration())}
        _=f.c.Close()
    }
}

func TestLiveTopologyAddOnlyStartsAddedNode(t *testing.T){
    for iter:=0;iter<100;iter++{
        f:=newControllerFixture(t);all:=testControllerConfigs(t,4);base:=all[:3];ids:=cfgIDs(base)
        if _,_,err:=f.c.ApplyToken(f.token(t,1,base),f.now);err!=nil{t.Fatal(err)}
        before:=instanceMap(t,f.c,ids);starts0,_:=f.tracker.snapshot()
        if _,_,err:=f.c.ApplyToken(f.token(t,2,all),f.now);err!=nil{t.Fatalf("iter=%d %v",iter,err)}
        after:=instanceMap(t,f.c,ids);starts1,_:=f.tracker.snapshot();d:=all[3].Peer.AllowedIdentity
        if !reflect.DeepEqual(before,after){t.Fatalf("iter=%d unrelated instance changed",iter)}
        if starts1[d]-starts0[d]!=1{t.Fatalf("iter=%d D starts delta=%d",iter,starts1[d]-starts0[d])}
        for _,id:=range ids{if starts1[id]!=starts0[id]{t.Fatalf("iter=%d unrelated start %s",iter,id)}}
        _=f.c.Close()
    }
}

func TestLiveTopologyRemoveOnlyStopsRemovedNode(t *testing.T){
    for iter:=0;iter<100;iter++{
        f:=newControllerFixture(t);all:=testControllerConfigs(t,4);keep:=all[:3];ids:=cfgIDs(keep);d:=all[3].Peer.AllowedIdentity
        if _,_,err:=f.c.ApplyToken(f.token(t,1,all),f.now);err!=nil{t.Fatal(err)}
        before:=instanceMap(t,f.c,ids);_,stops0:=f.tracker.snapshot()
        if _,_,err:=f.c.ApplyToken(f.token(t,2,keep),f.now);err!=nil{t.Fatalf("iter=%d %v",iter,err)}
        after:=instanceMap(t,f.c,ids);_,stops1:=f.tracker.snapshot()
        if !reflect.DeepEqual(before,after){t.Fatalf("iter=%d unrelated instance changed",iter)}
        if stops1[d]-stops0[d]!=1{t.Fatalf("iter=%d D stops delta=%d",iter,stops1[d]-stops0[d])}
        for _,id:=range ids{if stops1[id]!=stops0[id]{t.Fatalf("iter=%d unrelated stop %s",iter,id)}}
        _=f.c.Close()
    }
}

func TestLiveTopologyAddFailureRollsBack(t *testing.T){
    for iter:=0;iter<100;iter++{
        f:=newControllerFixture(t);all:=testControllerConfigs(t,4);base:=all[:3];ids:=cfgIDs(base);d:=all[3].Peer.AllowedIdentity
        if _,_,err:=f.c.ApplyToken(f.token(t,1,base),f.now);err!=nil{t.Fatal(err)}
        before:=instanceMap(t,f.c,ids);starts0,stops0:=f.tracker.snapshot()
        f.tracker.mu.Lock();f.tracker.failNode=d;f.tracker.mu.Unlock()
        if _,_,err:=f.c.ApplyToken(f.token(t,2,all),f.now);err==nil{t.Fatalf("iter=%d add failure accepted",iter)}
        after:=instanceMap(t,f.c,ids);starts1,stops1:=f.tracker.snapshot()
        if f.c.CurrentGeneration()!=1||!reflect.DeepEqual(before,after){t.Fatalf("iter=%d rollback generation=%d before=%v after=%v",iter,f.c.CurrentGeneration(),before,after)}
        for _,id:=range ids{if starts1[id]!=starts0[id]||stops1[id]!=stops0[id]{t.Fatalf("iter=%d unrelated churn %s",iter,id)}}
        if starts1[d]-starts0[d]!=1{t.Fatalf("iter=%d expected one failed D start",iter)}
        _=f.c.Close()
    }
}

func TestChangedExistingNodeFailsBeforeMutation(t *testing.T){
    for iter:=0;iter<100;iter++{
        f:=newControllerFixture(t);cfgs:=testControllerConfigs(t,4);ids:=cfgIDs(cfgs)
        if _,_,err:=f.c.ApplyToken(f.token(t,1,cfgs),f.now);err!=nil{t.Fatal(err)}
        before:=instanceMap(t,f.c,ids);starts0,stops0:=f.tracker.snapshot()
        changed:=cloneControllerConfigs(cfgs);changed[2].Peer.Address=fmt.Sprintf("198.51.100.%d:443",10+iter)
        _,_,err:=f.c.ApplyToken(f.token(t,2,changed),f.now)
        if !errors.Is(err,ErrInPlaceNodeMutationUnsupported){t.Fatalf("iter=%d err=%v",iter,err)}
        starts1,stops1:=f.tracker.snapshot()
        if f.c.CurrentGeneration()!=1||!reflect.DeepEqual(before,instanceMap(t,f.c,ids))||!reflect.DeepEqual(starts0,starts1)||!reflect.DeepEqual(stops0,stops1){t.Fatalf("iter=%d mutation occurred",iter)}
        _=f.c.Close()
    }
}

func TestSameGenerationSemanticNoOp(t *testing.T){
    for iter:=0;iter<100;iter++{
        f:=newControllerFixture(t);cfgs:=testControllerConfigs(t,4);ids:=cfgIDs(cfgs)
        if _,_,err:=f.c.ApplyToken(f.token(t,7,cfgs),f.now);err!=nil{t.Fatal(err)}
        before:=instanceMap(t,f.c,ids);starts0,stops0:=f.tracker.snapshot()
        p:=[]config.Config{cfgs[3],cfgs[1],cfgs[0],cfgs[2]}
        if _,changed,err:=f.c.ApplyToken(f.token(t,7,p),f.now);err!=nil||changed{t.Fatalf("iter=%d changed=%v err=%v",iter,changed,err)}
        starts1,stops1:=f.tracker.snapshot()
        if !reflect.DeepEqual(before,instanceMap(t,f.c,ids))||!reflect.DeepEqual(starts0,starts1)||!reflect.DeepEqual(stops0,stops1){t.Fatalf("iter=%d no-op churn",iter)}
        _=f.c.Close()
    }
}

func TestSameGenerationConflictFailsClosed(t *testing.T){
    for iter:=0;iter<100;iter++{
        f:=newControllerFixture(t);cfgs:=testControllerConfigs(t,4);ids:=cfgIDs(cfgs)
        if _,_,err:=f.c.ApplyToken(f.token(t,9,cfgs),f.now);err!=nil{t.Fatal(err)}
        before:=instanceMap(t,f.c,ids);starts0,stops0:=f.tracker.snapshot()
        changed:=cloneControllerConfigs(cfgs);changed[1].Peer.Address=fmt.Sprintf("203.0.113.%d:443",20+iter)
        if _,_,err:=f.c.ApplyToken(f.token(t,9,changed),f.now);err==nil{t.Fatalf("iter=%d conflict accepted",iter)}
        starts1,stops1:=f.tracker.snapshot()
        if !reflect.DeepEqual(before,instanceMap(t,f.c,ids))||!reflect.DeepEqual(starts0,starts1)||!reflect.DeepEqual(stops0,stops1){t.Fatalf("iter=%d conflict mutated runtime",iter)}
        _=f.c.Close()
    }
}

func TestRollbackGenerationDoesNotTouchRuntime(t *testing.T){
    for iter:=0;iter<100;iter++{
        f:=newControllerFixture(t);cfgs:=testControllerConfigs(t,4);ids:=cfgIDs(cfgs)
        if _,_,err:=f.c.ApplyToken(f.token(t,42,cfgs),f.now);err!=nil{t.Fatal(err)}
        before:=instanceMap(t,f.c,ids);starts0,stops0:=f.tracker.snapshot()
        if _,_,err:=f.c.ApplyToken(f.token(t,41,cfgs),f.now);!errors.Is(err,clustersync.ErrRollback){t.Fatalf("iter=%d err=%v",iter,err)}
        starts1,stops1:=f.tracker.snapshot()
        if !reflect.DeepEqual(before,instanceMap(t,f.c,ids))||!reflect.DeepEqual(starts0,starts1)||!reflect.DeepEqual(stops0,stops1){t.Fatalf("iter=%d rollback touched runtime",iter)}
        _=f.c.Close()
    }
}

func TestAllowedIdentityURIReplacementIsRemoveAdd(t *testing.T){
    f:=newControllerFixture(t);cfgs:=testControllerConfigs(t,3);old:=cfgs[1].Peer.AllowedIdentity
    if _,_,err:=f.c.ApplyToken(f.token(t,1,cfgs),f.now);err!=nil{t.Fatal(err)}
    changed:=cloneControllerConfigs(cfgs);changed[1].Peer.AllowedIdentity="urn:baft:node:B2"
    if _,_,err:=f.c.ApplyToken(f.token(t,2,changed),f.now);err!=nil{t.Fatal(err)}
    ids:=f.c.ActiveNodeIDs()
    if containsString(ids,old)||!containsString(ids,"urn:baft:node:B2"){t.Fatalf("active=%v",ids)}
    _=f.c.Close()
}
func containsString(a []string,s string)bool{for _,x:=range a{if x==s{return true}};return false}

func TestWorkerControllerConcurrentApplySerialized(t *testing.T){
    f:=newControllerFixture(t);cfgs:=testControllerConfigs(t,4);tok:=f.token(t,1,cfgs)
    var wg sync.WaitGroup;errCh:=make(chan error,20)
    for i:=0;i<20;i++{wg.Add(1);go func(){defer wg.Done();_,_,err:=f.c.ApplyToken(tok,f.now);errCh<-err}()}
    wg.Wait();close(errCh);for err:=range errCh{if err!=nil{t.Fatal(err)}}
    starts,_:=f.tracker.snapshot();for _,id:=range cfgIDs(cfgs){if starts[id]!=1{t.Fatalf("double start %s=%d",id,starts[id])}}
    if f.c.CurrentGeneration()!=1{t.Fatalf("generation=%d",f.c.CurrentGeneration())}
    _=f.c.Close()
}
