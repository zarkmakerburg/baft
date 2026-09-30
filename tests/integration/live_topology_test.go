package integration_test

import (
    "bytes"
    "context"
    "crypto/ecdh"
    "crypto/ed25519"
    "crypto/x509"
    "encoding/pem"
    "errors"
    "fmt"
    "io"
    "net"
    "os"
    "path/filepath"
    "sync"
    "sync/atomic"
    "testing"
    "time"

    "github.com/zarkmakerburg/baft/internal/cluster"
    "github.com/zarkmakerburg/baft/internal/clustersync"
    "github.com/zarkmakerburg/baft/internal/config"
    "github.com/zarkmakerburg/baft/internal/node"
    "github.com/zarkmakerburg/baft/internal/recordshape"
    "github.com/zarkmakerburg/baft/internal/securityinternal"
)

type liveEchoTarget struct {
    ln net.Listener
    accepts atomic.Int64
    mu sync.Mutex
    conns map[net.Conn]struct{}
    done chan struct{}
}

func newLiveEchoTarget(t *testing.T)*liveEchoTarget{
    t.Helper()
    ln,err:=net.Listen("tcp","127.0.0.1:0");if err!=nil{t.Fatal(err)}
    e:=&liveEchoTarget{ln:ln,conns:map[net.Conn]struct{}{},done:make(chan struct{})}
    go func(){
        defer close(e.done)
        for{
            c,err:=ln.Accept();if err!=nil{return}
            e.accepts.Add(1)
            e.mu.Lock();e.conns[c]=struct{}{};e.mu.Unlock()
            go func(x net.Conn){
                defer func(){e.mu.Lock();delete(e.conns,x);e.mu.Unlock();_ = x.Close()}()
                buf:=make([]byte,16*1024)
                for{
                    n,err:=x.Read(buf)
                    if n>0{if _,werr:=x.Write(buf[:n]);werr!=nil{return}}
                    if err!=nil{return}
                }
            }(c)
        }
    }()
    return e
}

func (e *liveEchoTarget) addr()string{return e.ln.Addr().String()}
func (e *liveEchoTarget) close(){
    _=e.ln.Close()
    e.mu.Lock();cs:=make([]net.Conn,0,len(e.conns));for c:=range e.conns{cs=append(cs,c)};e.mu.Unlock()
    for _,c:=range cs{_ = c.Close()}
    select{case <-e.done:case <-time.After(2*time.Second):}
}

type liveTopologyRemote struct {
    id string
    cfg config.Config
    runtime *node.Runtime
    done chan error
    target *liveEchoTarget
}

type liveTopologyHarness struct {
    ctx context.Context
    cancel context.CancelFunc
    controller *cluster.WorkerController
    tokenWorkerPub *ecdh.PublicKey
    signPriv ed25519.PrivateKey
    now time.Time
    generation uint64
    all []config.Config
    remotes []*liveTopologyRemote
}

func liveNodeID(letter byte)string{return fmt.Sprintf("urn:baft:node:%c",letter)}

func newLiveTopologyHarness(t *testing.T)*liveTopologyHarness{
    t.Helper()
    certs:=testPKI(t);dir:=t.TempDir()
    write:=func(name string,b []byte)string{
        p:=filepath.Join(dir,name);if err:=os.WriteFile(p,b,0600);err!=nil{t.Fatal(err)};return p
    }
    ca:=write("ca.pem",pem.EncodeToMemory(&pem.Block{Type:"CERTIFICATE",Bytes:certs.caDER}))
    cert:=write("server.pem",pem.EncodeToMemory(&pem.Block{Type:"CERTIFICATE",Bytes:certs.server.Certificate[0]}))
    der,err:=x509.MarshalPKCS8PrivateKey(certs.server.PrivateKey);if err!=nil{t.Fatal(err)}
    tlsKey:=write("server.key",pem.EncodeToMemory(&pem.Block{Type:"PRIVATE KEY",Bytes:der}))
    tlsCfg:=config.TLS{MinVersion:"1.3",CAFile:ca,CertFile:cert,KeyFile:tlsKey}

    workerNoise,err:=securityinternal.GenerateKeyPair();if err!=nil{t.Fatal(err)}
    workerNoisePath:=filepath.Join(dir,"worker-noise.json")
    if err:=securityinternal.SaveKeyPair(workerNoisePath,workerNoise);err!=nil{t.Fatal(err)}
    workerNoisePub,err:=securityinternal.EncodePublicKey(workerNoise.Public);if err!=nil{t.Fatal(err)}

    tokenPriv,tokenPub,err:=clustersync.GenerateWorkerKeyPair();if err!=nil{t.Fatal(err)}
    signPub,signPriv,err:=clustersync.GenerateSigningKeyPair();if err!=nil{t.Fatal(err)}
    engine,err:=clustersync.NewEngine(tokenPriv,signPub,"goldapp-baft");if err!=nil{t.Fatal(err)}

    ctx,cancel:=context.WithTimeout(context.Background(),5*time.Minute)
    h:=&liveTopologyHarness{ctx:ctx,cancel:cancel,tokenWorkerPub:tokenPub,signPriv:signPriv,now:time.Unix(1700000000,0)}

    used:=map[string]struct{}{}
    reserveUnique:=func()string{
        for{
            a:=reserveAddress(t)
            if _,ok:=used[a];ok{continue}
            used[a]=struct{}{}
            return a
        }
    }

    workerIdentity:="urn:baft:node:worker"\n    workerProtocolNodeID:="worker"
    for i:=0;i<4;i++{
        letter:=byte('A'+i);id:=liveNodeID(letter)
        target:=newLiveEchoTarget(t)
        exKey,err:=securityinternal.GenerateKeyPair();if err!=nil{t.Fatal(err)}
        exPath:=filepath.Join(dir,fmt.Sprintf("ex-%c-noise.json",letter))
        if err:=securityinternal.SaveKeyPair(exPath,exKey);err!=nil{t.Fatal(err)}
        exPub,err:=securityinternal.EncodePublicKey(exKey.Public);if err!=nil{t.Fatal(err)}

        ex,err:=config.LoadFile("../../configs/example-ex.yaml");if err!=nil{t.Fatal(err)}
        ex.Node.ID=fmt.Sprintf("%c",letter)
        ex.Server.Listen=reserveUnique();ex.Server.ServerName="ex.test"
        ex.Server.AllowedPeerIdentities=[]string{workerIdentity}
        ex.Management.UnixSocket=filepath.Join(dir,fmt.Sprintf("ex-%c.sock",letter))
        ex.Management.MetricsListen=reserveUnique()
        ex.Transport.Shards=1;ex.TLS=tlsCfg
        ex.Noise=&config.Noise{KeyFile:exPath,PeerPublicKey:workerNoisePub,RecordShaping:recordshape.Config{}}
        ex.Recovery=config.Recovery{Enabled:true,RetentionSeconds:10,Mode:"same_process"}
        ex.Routes=[]config.Route{{ID:"service-main",Direction:"inbound",Target:target.addr(),AllowedPeers:[]string{workerIdentity}}}
        if err:=config.Validate(ex);err!=nil{t.Fatalf("remote %c: %v",letter,err)}

        rt:=node.NewRuntime();done:=make(chan error,1)
        go func(){done<-rt.Run(ctx,ex)}()
        select{
        case <-rt.ListenerReadyForTest():
            actual,startErr:=rt.ListenerReadinessForTest()
            if startErr!=nil{t.Fatalf("remote %c readiness: %v",letter,startErr)}
            if actual!=ex.Server.Listen{t.Fatalf("remote %c address got=%s want=%s",letter,actual,ex.Server.Listen)}
        case <-time.After(6*time.Second):t.Fatalf("remote %c readiness timeout",letter)
        }
        h.remotes=append(h.remotes,&liveTopologyRemote{id:id,cfg:ex,runtime:rt,done:done,target:target})

        src,err:=config.LoadFile("../../configs/example-ir.yaml");if err!=nil{t.Fatal(err)}
        src.Node.ID=workerProtocolNodeID
        src.Peer.Address=ex.Server.Listen;src.Peer.ServerName="ex.test";src.Peer.AllowedIdentity=id
        src.Management.UnixSocket=filepath.Join(dir,fmt.Sprintf("source-%c.sock",letter))
        src.Management.MetricsListen=reserveUnique()
        src.Transport.Shards=1;src.TLS=tlsCfg
        src.Noise=&config.Noise{KeyFile:workerNoisePath,PeerPublicKey:exPub,RecordShaping:recordshape.Config{}}
        src.Recovery=config.Recovery{Enabled:true,RetentionSeconds:10,Mode:"same_process"}
        src.Routes=[]config.Route{{ID:"service-main",Direction:"outbound",Listen:reserveUnique(),RemoteRoute:"service-main"}}
        if err:=config.Validate(src);err!=nil{t.Fatalf("source %c: %v",letter,err)}
        h.all=append(h.all,src)
    }

    tmpl:=clustersync.WorkerTemplate{
        NodeID:workerProtocolNodeID,NoiseKeyFile:workerNoisePath,TLS:tlsCfg,
        Limits:config.Limits{MaxFlows:128,DataMemoryMiB:128,ReceiveInitialKiB:64,ReceiveMaxMiB:8,ReplayMaxMiB:8},
        Recovery:config.Recovery{Enabled:true,RetentionSeconds:10,Mode:"same_process"},
        RouteBasePort:16000,MetricsBasePort:15000,StateDir:dir,
    }
    ctrl,err:=cluster.NewWorkerController(ctx,engine,tmpl);if err!=nil{t.Fatal(err)}
    h.controller=ctrl
    return h
}

func (h *liveTopologyHarness) apply(t *testing.T,indexes ...int){
    t.Helper();h.generation++
    cfgs:=make([]config.Config,0,len(indexes));for _,i:=range indexes{cfgs=append(cfgs,h.all[i])}
    m,err:=clustersync.ManifestFromConfigs("goldapp-baft",h.generation,15*time.Minute,h.now,cfgs);if err!=nil{t.Fatal(err)}
    tok,err:=clustersync.Seal(m,h.tokenWorkerPub,h.signPriv);if err!=nil{t.Fatal(err)}
    snap,changed,err:=h.controller.ApplyToken(tok,h.now.Add(time.Second));if err!=nil{t.Fatalf("generation %d apply: %v",h.generation,err)}
    if !changed{t.Fatalf("generation %d unexpectedly no-op",h.generation)}
    if snap.Generation!=h.generation||h.controller.CurrentGeneration()!=h.generation{t.Fatalf("generation commit mismatch snap=%d controller=%d want=%d",snap.Generation,h.controller.CurrentGeneration(),h.generation)}
}

func (h *liveTopologyHarness) close(t *testing.T){
    t.Helper()
    if h.controller!=nil{if err:=h.controller.Close();err!=nil{t.Errorf("controller close: %v",err)}}
    h.cancel()
    for _,r:=range h.remotes{
        select{case err:=<-r.done:if err!=nil&&!errors.Is(err,context.Canceled){t.Errorf("remote %s: %v",r.id,err)}
        case <-time.After(5*time.Second):t.Errorf("remote %s shutdown timeout",r.id)}
        r.target.close()
    }
}

func liveOpenFlow(t *testing.T,h *liveTopologyHarness,nodeID string)net.Conn{
    t.Helper();cfg,ok:=h.controller.ConfigForTest(nodeID);if !ok{t.Fatalf("missing config %s",nodeID)}
    c,err:=net.DialTimeout("tcp",cfg.Routes[0].Listen,2*time.Second);if err!=nil{t.Fatal(err)}
    _=c.SetDeadline(time.Now().Add(30*time.Second))
    liveEcho(t,c,[]byte("topology-flow-open"))
    return c
}

func liveEcho(t *testing.T,c net.Conn,payload []byte){
    t.Helper()
    if _,err:=c.Write(payload);err!=nil{t.Fatal(err)}
    got:=make([]byte,len(payload));if _,err:=io.ReadFull(c,got);err!=nil{t.Fatal(err)}
    if !bytes.Equal(got,payload){t.Fatalf("payload mismatch got=%d want=%d",len(got),len(payload))}
}

func waitLiveEpoch(t *testing.T,h *liveTopologyHarness,nodeID string)uint64{
    t.Helper();deadline:=time.Now().Add(5*time.Second)
    for{
        e:=h.controller.RecoveryEpochsForTest(nodeID)
        if len(e)==1&&e[0]>0{return e[0]}
        if time.Now().After(deadline){t.Fatalf("epoch unavailable node=%s epochs=%v",nodeID,e)}
        time.Sleep(10*time.Millisecond)
    }
}

func assertAddrClosed(t *testing.T,addr string){
    t.Helper();deadline:=time.Now().Add(2*time.Second)
    for{
        c,err:=net.DialTimeout("tcp",addr,50*time.Millisecond)
        if err!=nil{return}
        _=c.Close()
        if time.Now().After(deadline){t.Fatalf("address still open after planned removal: %s",addr)}
        time.Sleep(10*time.Millisecond)
    }
}

func topologyPayload(iter int)[]byte{
    p:=make([]byte,4096+iter%127)
    for i:=range p{p[i]=byte((i*31+iter*17)%251)}
    return p
}

func TestLiveTopologyRemoveUnrelatedNodePreservesActiveFlow(t *testing.T){
    h:=newLiveTopologyHarness(t);defer h.close(t)
    h.apply(t,0,1,2,3)
    cID:=liveNodeID('C');dID:=liveNodeID('D')
    c:=liveOpenFlow(t,h,cID);defer c.Close()
    cInstance,_:=h.controller.InstanceTokenForTest(cID)
    epoch:=waitLiveEpoch(t,h,cID)
    targetBefore:=h.remotes[2].target.accepts.Load()

    for i:=0;i<50;i++{
        dCfg,ok:=h.controller.ConfigForTest(dID);if !ok{t.Fatalf("iteration %d D missing before remove",i)}
        h.apply(t,0,1,2)
        if got,_:=h.controller.InstanceTokenForTest(cID);got!=cInstance{t.Fatalf("iteration %d C instance changed %d -> %d",i,cInstance,got)}
        liveEcho(t,c,topologyPayload(i))
        if got:=h.remotes[2].target.accepts.Load();got!=targetBefore{t.Fatalf("iteration %d C target reopened before=%d after=%d",i,targetBefore,got)}
        if got:=waitLiveEpoch(t,h,cID);got!=epoch{t.Fatalf("iteration %d C epoch changed %d -> %d",i,epoch,got)}
        if !h.controller.StoppedDoneForTest(dID){t.Fatalf("iteration %d D runtime not done",i)}
        assertAddrClosed(t,dCfg.Routes[0].Listen);assertAddrClosed(t,dCfg.Management.MetricsListen)
        if i<49{h.apply(t,0,1,2,3)}
    }
}

func TestLiveTopologyAddUnrelatedNodePreservesActiveFlow(t *testing.T){
    h:=newLiveTopologyHarness(t);defer h.close(t)
    h.apply(t,0,1,2)
    cID:=liveNodeID('C');dID:=liveNodeID('D')
    c:=liveOpenFlow(t,h,cID);defer c.Close()
    cInstance,_:=h.controller.InstanceTokenForTest(cID)
    epoch:=waitLiveEpoch(t,h,cID)
    targetBefore:=h.remotes[2].target.accepts.Load()

    for i:=0;i<50;i++{
        h.apply(t,0,1,2,3)
        if got,_:=h.controller.InstanceTokenForTest(cID);got!=cInstance{t.Fatalf("iteration %d C instance changed %d -> %d",i,cInstance,got)}
        if _,ok:=h.controller.InstanceTokenForTest(dID);!ok{t.Fatalf("iteration %d D not active after add",i)}
        liveEcho(t,c,topologyPayload(100+i))
        if got:=h.remotes[2].target.accepts.Load();got!=targetBefore{t.Fatalf("iteration %d C target reopened before=%d after=%d",i,targetBefore,got)}
        if got:=waitLiveEpoch(t,h,cID);got!=epoch{t.Fatalf("iteration %d C epoch changed %d -> %d",i,epoch,got)}
        if i<49{h.apply(t,0,1,2)}
    }
}

func TestTopologyGenerationDoesNotAdvanceUnrelatedSessionEpoch(t *testing.T){
    h:=newLiveTopologyHarness(t);defer h.close(t)
    h.apply(t,0,1,2)
    cID:=liveNodeID('C')
    c:=liveOpenFlow(t,h,cID);defer c.Close()
    instance,_:=h.controller.InstanceTokenForTest(cID)
    epoch:=waitLiveEpoch(t,h,cID)
    targetBefore:=h.remotes[2].target.accepts.Load()

    dPresent:=false
    for i:=0;i<50;i++{
        if dPresent{h.apply(t,0,1,2)}else{h.apply(t,0,1,2,3)}
        dPresent=!dPresent
        if got,_:=h.controller.InstanceTokenForTest(cID);got!=instance{t.Fatalf("iteration %d C instance changed",i)}
        if got:=waitLiveEpoch(t,h,cID);got!=epoch{t.Fatalf("iteration %d topology generation advanced SessionEpoch %d -> %d",i,epoch,got)}
        liveEcho(t,c,[]byte{byte(i),0x7f,0x31})
        if got:=h.remotes[2].target.accepts.Load();got!=targetBefore{t.Fatalf("iteration %d target reopen before=%d after=%d",i,targetBefore,got)}
    }
}
