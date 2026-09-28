package integration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/config"
	"github.com/zarkmakerburg/baft/internal/node"
	"github.com/zarkmakerburg/baft/internal/protocol"
	"github.com/zarkmakerburg/baft/internal/recordshape"
	"github.com/zarkmakerburg/baft/internal/securityinternal"
	"github.com/zarkmakerburg/baft/internal/session"
)

type cutProxy struct {
	ln net.Listener
	target string
	mu sync.Mutex
	conns map[net.Conn]struct{}
}

func newCutProxy(t *testing.T,target string)*cutProxy{
	t.Helper()
	ln,err:=net.Listen("tcp","127.0.0.1:0");if err!=nil{t.Fatal(err)}
	p:=&cutProxy{ln:ln,target:target,conns:map[net.Conn]struct{}{}}
	go p.serve()
	return p
}
func (p *cutProxy) Addr()string{return p.ln.Addr().String()}
func (p *cutProxy) Close(){_ = p.ln.Close();p.CutAll()}
func (p *cutProxy) track(c net.Conn,add bool){p.mu.Lock();if add{p.conns[c]=struct{}{}}else{delete(p.conns,c)};p.mu.Unlock()}
func (p *cutProxy) CutAll(){
	p.mu.Lock();cs:=make([]net.Conn,0,len(p.conns));for c:=range p.conns{cs=append(cs,c)};p.mu.Unlock()
	for _,c:=range cs{_ = c.Close()}
}
func (p *cutProxy) serve(){
	for{
		a,err:=p.ln.Accept();if err!=nil{return}
		b,err:=net.Dial("tcp",p.target);if err!=nil{_ = a.Close();continue}
		p.track(a,true);p.track(b,true)
		go func(x,y net.Conn){
			defer func(){p.track(x,false);p.track(y,false);_ = x.Close();_ = y.Close()}()
			done:=make(chan struct{},2)
			go func(){_,_=io.Copy(x,y);done<-struct{}{}}()
			go func(){_,_=io.Copy(y,x);done<-struct{}{}}()
			<-done
		}(a,b)
	}
}

type recoveryRuntimePair struct{
	ctx context.Context
	cancel context.CancelFunc
	exDone chan error
	irDone chan error
	ex config.Config
	ir config.Config
	proxy *cutProxy
	targetLn net.Listener
	targetAccepts atomic.Int64
	targetBaseline int64
	exRuntime *node.Runtime
	irRuntime *node.Runtime
}

func startRecoveryRuntimePair(t *testing.T,routeCount int)*recoveryRuntimePair{
	t.Helper()
	certs:=testPKI(t);dir:=t.TempDir()
	write:=func(name string,b []byte)string{p:=filepath.Join(dir,name);if err:=os.WriteFile(p,b,0600);err!=nil{t.Fatal(err)};return p}
	ca:=write("ca.pem",pem.EncodeToMemory(&pem.Block{Type:"CERTIFICATE",Bytes:certs.caDER}))
	cert:=write("server.pem",pem.EncodeToMemory(&pem.Block{Type:"CERTIFICATE",Bytes:certs.server.Certificate[0]}))
	der,err:=x509.MarshalPKCS8PrivateKey(certs.server.PrivateKey);if err!=nil{t.Fatal(err)}
	key:=write("server.key",pem.EncodeToMemory(&pem.Block{Type:"PRIVATE KEY",Bytes:der}))

	targetLn,err:=net.Listen("tcp","127.0.0.1:0");if err!=nil{t.Fatal(err)}
	pair:=&recoveryRuntimePair{targetLn:targetLn}
	go func(){
		for{
			c,e:=targetLn.Accept();if e!=nil{return}
			pair.targetAccepts.Add(1)
			go func(x net.Conn){
				defer x.Close()
				buf:=make([]byte,16*1024)
				for{
					n,e:=x.Read(buf)
					if n>0{
						time.Sleep(300*time.Microsecond)
						if _,werr:=x.Write(buf[:n]);werr!=nil{return}
					}
					if e!=nil{return}
				}
			}(c)
		}
	}()

	exKey,err:=securityinternal.GenerateKeyPair();if err!=nil{t.Fatal(err)}
	irKey,err:=securityinternal.GenerateKeyPair();if err!=nil{t.Fatal(err)}
	exPath:=filepath.Join(dir,"ex-noise.json");irPath:=filepath.Join(dir,"ir-noise.json")
	if err:=securityinternal.SaveKeyPair(exPath,exKey);err!=nil{t.Fatal(err)}
	if err:=securityinternal.SaveKeyPair(irPath,irKey);err!=nil{t.Fatal(err)}
	exPub,_:=securityinternal.EncodePublicKey(exKey.Public);irPub,_:=securityinternal.EncodePublicKey(irKey.Public)

	ex,err:=config.LoadFile("../../configs/example-ex.yaml");if err!=nil{t.Fatal(err)}
	ex.Node.ID="ex-recovery";ex.Server.Listen=reserveAddress(t);ex.Server.ServerName="ex.test"
	ex.Server.AllowedPeerIdentities=[]string{"urn:baft:node:ir-recovery"}
	ex.Management.UnixSocket=filepath.Join(dir,"ex.sock");ex.Management.MetricsListen=reserveAddress(t)
	ex.Transport.Shards=1;ex.TLS=config.TLS{MinVersion:"1.3",CAFile:ca,CertFile:cert,KeyFile:key}
	ex.Noise=&config.Noise{KeyFile:exPath,PeerPublicKey:irPub,RecordShaping:recordshape.Config{}}
	ex.Recovery=config.Recovery{Enabled:true,RetentionSeconds:10,Mode:"same_process"}
	ex.Routes=nil
	for i:=0;i<routeCount;i++{
		id:=routeName(i)
		ex.Routes=append(ex.Routes,config.Route{ID:id,Direction:"inbound",Target:targetLn.Addr().String(),AllowedPeers:[]string{"urn:baft:node:ir-recovery"}})
	}

	proxy:=newCutProxy(t,ex.Server.Listen);pair.proxy=proxy
	ir,err:=config.LoadFile("../../configs/example-ir.yaml");if err!=nil{t.Fatal(err)}
	ir.Node.ID="ir-recovery";ir.Peer.Address=proxy.Addr();ir.Peer.ServerName="ex.test";ir.Peer.AllowedIdentity="urn:baft:node:ex-recovery"
	ir.Management.UnixSocket=filepath.Join(dir,"ir.sock");ir.Management.MetricsListen=reserveAddress(t)
	ir.Transport.Shards=1;ir.TLS=ex.TLS
	ir.Noise=&config.Noise{KeyFile:irPath,PeerPublicKey:exPub,RecordShaping:recordshape.Config{}}
	ir.Recovery=ex.Recovery;ir.Routes=nil
	for i:=0;i<routeCount;i++{
		id:=routeName(i)
		ir.Routes=append(ir.Routes,config.Route{ID:"local-"+id,Direction:"outbound",Listen:reserveAddress(t),RemoteRoute:id})
	}
	if err:=config.Validate(ex);err!=nil{t.Fatalf("EX: %v",err)}
	if err:=config.Validate(ir);err!=nil{t.Fatalf("IR: %v",err)}

	ctx,cancel:=context.WithTimeout(context.Background(),30*time.Second)
	pair.ctx=ctx;pair.cancel=cancel;pair.ex=ex;pair.ir=ir;pair.exDone=make(chan error,1);pair.irDone=make(chan error,1)
	pair.exRuntime=node.NewRuntime()
	pair.irRuntime=node.NewRuntime()
	go func(){pair.exDone<-pair.exRuntime.Run(ctx,ex)}()
	waitTCP(t,ex.Server.Listen,time.Now().Add(6*time.Second))
	go func(){pair.irDone<-pair.irRuntime.Run(ctx,ir)}()
	waitTCP(t,ir.Routes[0].Listen,time.Now().Add(8*time.Second))
	time.Sleep(50*time.Millisecond)
	pair.targetBaseline=pair.targetAccepts.Load()
	return pair
}

func routeName(i int)string{return "recovery-route-"+string(rune('a'+i))}
func (p *recoveryRuntimePair) close(t *testing.T){
	t.Helper();p.cancel();p.proxy.Close();_ = p.targetLn.Close()
	for name,ch:=range map[string]<-chan error{"ex":p.exDone,"ir":p.irDone}{
		select{case err:=<-ch:if err!=nil&&!errors.Is(err,context.Canceled){t.Fatalf("%s runtime: %v",name,err)}
		case <-time.After(5*time.Second):t.Fatalf("%s runtime shutdown timeout",name)}
	}
}

func (p *recoveryRuntimePair) closeAllowErrors(){
	p.cancel();p.proxy.Close();_ = p.targetLn.Close()
	for _,ch:=range []<-chan error{p.exDone,p.irDone}{
		select{case <-ch:case <-time.After(5*time.Second):}
	}
}

func openRecoveryFlow(t *testing.T,p *recoveryRuntimePair) net.Conn {
	t.Helper()
	c,err:=net.DialTimeout("tcp",p.ir.Routes[0].Listen,time.Second);if err!=nil{t.Fatal(err)}
	_ = c.SetDeadline(time.Now().Add(10*time.Second))
	probe:=[]byte("commit-safety-probe")
	if _,err:=c.Write(probe);err!=nil{c.Close();t.Fatal(err)}
	got:=make([]byte,len(probe))
	if _,err:=io.ReadFull(c,got);err!=nil{c.Close();t.Fatal(err)}
	if !bytes.Equal(got,probe){c.Close();t.Fatal("probe echo mismatch")}
	return c
}

func assertRuntimeOldAuthority(t *testing.T,name string,r *node.Runtime){
	t.Helper()
	deadline:=time.Now().Add(2*time.Second)
	for{
		s:=r.RecoveryAuthoritiesForTest()
		if len(s)==1{
			if s[0].Epoch!=1||s[0].Owner!="shard-0-carrier-1"{t.Fatalf("%s authority=%+v",name,s[0])}
			return
		}
		if time.Now().After(deadline){t.Fatalf("%s authority snapshots=%+v",name,s)}
		time.Sleep(5*time.Millisecond)
	}
}

func transferAcrossCut(t *testing.T,p *recoveryRuntimePair,route int,payload []byte,cutAt int)[]byte{
	t.Helper()
	c,err:=net.DialTimeout("tcp",p.ir.Routes[route].Listen,time.Second);if err!=nil{t.Fatal(err)}
	defer c.Close();_ = c.SetDeadline(time.Now().Add(20*time.Second))
	got:=make([]byte,len(payload));readErr:=make(chan error,1)
	go func(){_,e:=io.ReadFull(c,got);readErr<-e}()
	written:=0;cut:=false
	for written<len(payload){
		n:=16*1024;if len(payload)-written<n{n=len(payload)-written}
		nw,e:=c.Write(payload[written:written+n]);if e!=nil{t.Fatalf("client write after %d: %v",written,e)}
		written+=nw
		if !cut&&written>=cutAt{p.proxy.CutAll();cut=true}
		time.Sleep(200*time.Microsecond)
	}
	select{case e:=<-readErr:if e!=nil{t.Fatal(e)};case <-time.After(20*time.Second):t.Fatal("payload read timeout")}
	return got
}

func TestRuntimeCarrierReplacementPreservesActiveFlow(t *testing.T){
	p:=startRecoveryRuntimePair(t,1);defer p.close(t)
	payload:=make([]byte,2*1024*1024+731)
	for i:=range payload{payload[i]=byte((i*37+11)%251)}
	want:=sha256.Sum256(payload)
	got:=transferAcrossCut(t,p,0,payload,256*1024)
	have:=sha256.Sum256(got)
	if !bytes.Equal(got,payload)||have!=want{t.Fatalf("payload mismatch got_hash=%x want_hash=%x",have,want)}
	if n:=p.targetAccepts.Load()-p.targetBaseline;n!=1{t.Fatalf("target TCP socket was reopened: test_accepts=%d baseline=%d total=%d",n,p.targetBaseline,p.targetAccepts.Load())}
	t.Logf("PASS active flow survived carrier replacement bytes=%d hash=%x",len(got),have)
}

func TestMultiFlowCarrierReplacementNoDuplicateOrLoss(t *testing.T){
	p:=startRecoveryRuntimePair(t,1);defer p.close(t)
	const flows=8
	var wg sync.WaitGroup
	errCh:=make(chan error,flows)
	startBulk:=make(chan struct{})
	mid:=make(chan struct{},flows)

	for f:=0;f<flows;f++{
		f:=f;wg.Add(1)
		go func(){
			defer wg.Done()
			payload:=make([]byte,512*1024+f*97)
			for i:=range payload{payload[i]=byte((i*13+f*19)%251)}
			c,err:=net.DialTimeout("tcp",p.ir.Routes[0].Listen,time.Second);if err!=nil{errCh<-err;return}
			defer c.Close();_ = c.SetDeadline(time.Now().Add(25*time.Second))

			// Prove the flow is fully OPEN and bound to its existing target
			// socket before the coordinated mid-transfer carrier cut.
			prelude:=[]byte{0x70,byte(f),0x5a}
			if _,err:=c.Write(prelude);err!=nil{errCh<-err;return}
			preGot:=make([]byte,len(prelude))
			if _,err:=io.ReadFull(c,preGot);err!=nil{errCh<-err;return}
			if !bytes.Equal(preGot,prelude){errCh<-io.ErrUnexpectedEOF;return}

			<-startBulk
			got:=make([]byte,len(payload));rd:=make(chan error,1)
			go func(){_,e:=io.ReadFull(c,got);rd<-e}()
			signaled:=false
			for off:=0;off<len(payload);{
				n:=8192;if len(payload)-off<n{n=len(payload)-off}
				nw,e:=c.Write(payload[off:off+n]);if e!=nil{errCh<-e;return};off+=nw
				if !signaled&&off>=64*1024{mid<-struct{}{};signaled=true}
				time.Sleep(250*time.Microsecond)
			}
			if e:=<-rd;e!=nil{errCh<-e;return}
			if !bytes.Equal(got,payload){errCh<-io.ErrUnexpectedEOF;return}
		}()
	}
	// Give all goroutines time to establish and verify their OPENs, then begin
	// bulk transfer together. The cut occurs only after every flow has crossed
	// 64 KiB, so all eight are genuinely active at failure time.
	time.Sleep(150*time.Millisecond)
	close(startBulk)
	for i:=0;i<flows;i++{
		select{case <-mid:case <-time.After(8*time.Second):t.Fatal("not all flows reached mid-transfer cut barrier")}
	}
	p.proxy.CutAll()
	wg.Wait();close(errCh);for e:=range errCh{if e!=nil{t.Fatal(e)}}
	if n:=p.targetAccepts.Load()-p.targetBaseline;n!=flows{t.Fatalf("target flow identity/socket count=%d want=%d baseline=%d total=%d",n,flows,p.targetBaseline,p.targetAccepts.Load())}
	t.Log("PASS 8 active TCP flows survived one carrier replacement without byte loss/duplication")
}

func TestRuntimeRecoveryRouteIdentityIsolation(t *testing.T){
	p:=startRecoveryRuntimePair(t,6);defer p.close(t)
	// Open one live flow on every route before the same carrier is cut. Route
	// identity is part of the frozen recovery offer and all six must reconcile.
	conns:=make([]net.Conn,0,6)
	for i:=0;i<6;i++{c,err:=net.DialTimeout("tcp",p.ir.Routes[i].Listen,time.Second);if err!=nil{t.Fatal(err)};conns=append(conns,c);_ = c.SetDeadline(time.Now().Add(15*time.Second));if _,err:=c.Write([]byte{byte(i),0x7f});err!=nil{t.Fatal(err)};got:=make([]byte,2);if _,err:=io.ReadFull(c,got);err!=nil{t.Fatal(err)};if !bytes.Equal(got,[]byte{byte(i),0x7f}){t.Fatal("pre-cut route mismatch")}}
	p.proxy.CutAll();time.Sleep(500*time.Millisecond)
	for i,c:=range conns{want:=[]byte{0xa5,byte(i)};if _,err:=c.Write(want);err!=nil{t.Fatal(err)};got:=make([]byte,2);if _,err:=io.ReadFull(c,got);err!=nil{t.Fatal(err)};if !bytes.Equal(got,want){t.Fatalf("route %d identity mixed got=%v want=%v",i,got,want)};_ = c.Close()}
	if n:=p.targetAccepts.Load()-p.targetBaseline;n!=6{t.Fatalf("route recovery reopened/mixed target flows test_accepts=%d baseline=%d total=%d",n,p.targetBaseline,p.targetAccepts.Load())}
	t.Log("PASS six route/session identities remained isolated across replacement")
}



func waitRecoveryFaultAndSettled(t *testing.T, fired *atomic.Bool, p *recoveryRuntimePair) {
	t.Helper()
	deadline:=time.Now().Add(5*time.Second)
	for time.Now().Before(deadline) {
		if fired.Load() {
			ir:=p.irRuntime.RecoveryAuthoritiesForTest()
			ex:=p.exRuntime.RecoveryAuthoritiesForTest()
			if len(ir)>0&&len(ex)>0 {
				allOld:=true
				for _,s:=range append(append([]node.RecoveryAuthoritySnapshot{},ir...),ex...) {
					if s.Epoch!=1||s.Frozen { allOld=false;break }
				}
				if allOld{return}
			}
		}
		time.Sleep(20*time.Millisecond)
	}
	t.Fatalf("pre-commit fault did not settle with both runtimes on old epoch: fired=%v ir=%+v ex=%+v",
		fired.Load(),p.irRuntime.RecoveryAuthoritiesForTest(),p.exRuntime.RecoveryAuthoritiesForTest())
}

func assertBothRuntimeOldEpoch(t *testing.T,p *recoveryRuntimePair) {
	t.Helper()
	for name,states:=range map[string][]node.RecoveryAuthoritySnapshot{
		"dialer":p.irRuntime.RecoveryAuthoritiesForTest(),
		"listener":p.exRuntime.RecoveryAuthoritiesForTest(),
	} {
		if len(states)==0 { t.Fatalf("%s has no live recovery authority",name) }
		for _,s:=range states {
			if s.Epoch!=1||s.Owner!="shard-0-carrier-1" { t.Fatalf("%s old authority changed before distributed commit: %+v",name,s) }
			if s.Frozen { t.Fatalf("%s remained frozen after pre-commit abort: %+v",name,s) }
		}
	}
}

func TestTwoRuntimeDialerPreCommitFailureKeepsBothOldEpoch(t *testing.T) {
	p:=startRecoveryRuntimePair(t,1);defer p.close(t)
	c,err:=net.DialTimeout("tcp",p.ir.Routes[0].Listen,time.Second);if err!=nil{t.Fatal(err)}
	defer c.Close();_ = c.SetDeadline(time.Now().Add(10*time.Second))
	if _,err:=c.Write([]byte("precommit-dialer"));err!=nil{t.Fatal(err)}
	echo:=make([]byte,len("precommit-dialer"));if _,err:=io.ReadFull(c,echo);err!=nil{t.Fatal(err)}
	var fired atomic.Bool
	p.irRuntime.SetRecoveryFaultHookForTest(func(stage string)error{
		if stage=="before_commit" { fired.Store(true);return errors.New("dialer precommit injected") }
		return nil
	})
	p.proxy.CutAll()
	waitRecoveryFaultAndSettled(t,&fired,p)
	assertBothRuntimeOldEpoch(t,p)
	t.Log("PASS two real runtimes kept old epoch after dialer pre-commit failure")
}

func TestTwoRuntimeListenerPreCommitFailureKeepsBothOldEpoch(t *testing.T) {
	p:=startRecoveryRuntimePair(t,1);defer p.close(t)
	c,err:=net.DialTimeout("tcp",p.ir.Routes[0].Listen,time.Second);if err!=nil{t.Fatal(err)}
	defer c.Close();_ = c.SetDeadline(time.Now().Add(10*time.Second))
	if _,err:=c.Write([]byte("precommit-listener"));err!=nil{t.Fatal(err)}
	echo:=make([]byte,len("precommit-listener"));if _,err:=io.ReadFull(c,echo);err!=nil{t.Fatal(err)}
	var fired atomic.Bool
	p.exRuntime.SetRecoveryFaultHookForTest(func(stage string)error{
		if stage=="listener_before_commit" { fired.Store(true);return errors.New("listener precommit injected") }
		return nil
	})
	p.proxy.CutAll()
	waitRecoveryFaultAndSettled(t,&fired,p)
	assertBothRuntimeOldEpoch(t,p)
	t.Log("PASS two real runtimes kept old epoch after listener pre-commit failure")
}


func oneRecoveryAuthority(t *testing.T,r *node.Runtime) node.RecoveryAuthoritySnapshot {
	t.Helper()
	s:=r.RecoveryAuthoritiesForTest()
	if len(s)!=1{t.Fatalf("authority count=%d states=%+v",len(s),s)}
	return s[0]
}

func waitAuthorityPair(t *testing.T,p *recoveryRuntimePair,epoch uint64,finalized bool)(node.RecoveryAuthoritySnapshot,node.RecoveryAuthoritySnapshot){
	t.Helper()
	deadline:=time.Now().Add(8*time.Second)
	for{
		ir:=p.irRuntime.RecoveryAuthoritiesForTest()
		ex:=p.exRuntime.RecoveryAuthoritiesForTest()
		if len(ir)==1&&len(ex)==1&&ir[0].Epoch==epoch&&ex[0].Epoch==epoch{
			if !finalized||(!ir[0].Frozen&&!ex[0].Frozen){
				return ir[0],ex[0]
			}
		}
		if time.Now().After(deadline){t.Fatalf("authority convergence timeout ir=%+v ex=%+v",ir,ex)}
		time.Sleep(5*time.Millisecond)
	}
}

func assertEchoHashOnExistingFlow(t *testing.T,c net.Conn,payload []byte) [32]byte {
	t.Helper()
	want:=sha256.Sum256(payload)
	got:=make([]byte,len(payload))
	rd:=make(chan error,1)
	go func(){_,err:=io.ReadFull(c,got);rd<-err}()
	for off:=0;off<len(payload);{
		n:=32*1024;if len(payload)-off<n{n=len(payload)-off}
		w,err:=c.Write(payload[off:off+n]);if err!=nil{t.Fatalf("existing flow write: %v",err)}
		off+=w
	}
	select{case err:=<-rd:if err!=nil{t.Fatalf("existing flow read: %v",err)};case <-time.After(10*time.Second):t.Fatal("existing flow hash payload timeout")}
	have:=sha256.Sum256(got)
	if !bytes.Equal(got,payload)||have!=want{t.Fatalf("payload integrity got=%x want=%x",have,want)}
	return have
}

func TestDistributedCommitACKLossResolvesWithoutAuthorityDivergence(t *testing.T){
	p:=startRecoveryRuntimePair(t,1);defer p.close(t)
	c:=openRecoveryFlow(t,p);defer c.Close()
	targetBefore:=p.targetAccepts.Load()

	listenerPublished:=make(chan struct{})
	releaseListener:=make(chan struct{})
	var publishOnce sync.Once
	p.exRuntime.SetRecoveryFaultHookForTest(func(stage string)error{
		if stage=="after_listener_publish_before_commit_ack"{
			publishOnce.Do(func(){close(listenerPublished)})
			<-releaseListener
			return errors.New("deterministic ACK-loss after listener publish")
		}
		return nil
	})

	statusSent:=make(chan struct{})
	releaseStatus:=make(chan struct{})
	var statusOnce sync.Once
	p.irRuntime.SetRecoveryFaultHookForTest(func(stage string)error{
		if stage=="status_query_after_send"{
			statusOnce.Do(func(){close(statusSent)})
			<-releaseStatus
		}
		return nil
	})

	// First cut kills old carrier and starts the candidate transaction.
	p.proxy.CutAll()
	select{case <-listenerPublished:case <-time.After(8*time.Second):t.Fatal("listener never reached post-publish barrier")}

	exAtPublish:=oneRecoveryAuthority(t,p.exRuntime)
	irAtPublish:=oneRecoveryAuthority(t,p.irRuntime)
	if exAtPublish.Epoch!=2{t.Fatalf("listener not committed at barrier: %+v",exAtPublish)}
	if irAtPublish.Epoch!=1{t.Fatalf("dialer committed before valid ACK: %+v",irAtPublish)}
	if exAtPublish.CandidateID==""||exAtPublish.CandidateID!=irAtPublish.CandidateID||exAtPublish.PlanDigest==""||exAtPublish.PlanDigest!=irAtPublish.PlanDigest{
		t.Fatalf("transaction identity diverged at publish ex=%+v ir=%+v",exAtPublish,irAtPublish)
	}

	// Kill the physical candidate before a valid COMMIT_ACK can reach Dialer.
	p.proxy.CutAll()
	close(releaseListener)

	// The next authenticated carrier may only resolve the same transaction.
	select{case <-statusSent:case <-time.After(8*time.Second):t.Fatalf("dialer never entered status resolution ir=%+v ex=%+v",p.irRuntime.RecoveryAuthoritiesForTest(),p.exRuntime.RecoveryAuthoritiesForTest())}
	irUncertain:=oneRecoveryAuthority(t,p.irRuntime)
	exUncertain:=oneRecoveryAuthority(t,p.exRuntime)
	if irUncertain.Epoch!=1||irUncertain.TxnState!=session.RecoveryTxnUncertain||!irUncertain.Frozen{
		t.Fatalf("dialer uncertainty state=%+v",irUncertain)
	}
	if exUncertain.Epoch!=2||!exUncertain.Frozen{t.Fatalf("listener committed uncertainty state=%+v",exUncertain)}
	if irUncertain.CandidateID!=exUncertain.CandidateID||irUncertain.PlanDigest!=exUncertain.PlanDigest||irUncertain.NextEpoch!=2{
		t.Fatalf("uncertain identity changed ir=%+v ex=%+v",irUncertain,exUncertain)
	}
	close(releaseStatus)

	irDone,exDone:=waitAuthorityPair(t,p,2,true)
	if irDone.Owner!=irUncertain.CandidateID||exDone.Owner!=irUncertain.CandidateID{
		t.Fatalf("resolved owner changed ir=%+v ex=%+v",irDone,exDone)
	}
	if irDone.PlanDigest!=irUncertain.PlanDigest||exDone.PlanDigest!=irUncertain.PlanDigest{
		t.Fatalf("resolved plan digest changed ir=%+v ex=%+v",irDone,exDone)
	}
	payload:=make([]byte,640*1024+137)
	for i:=range payload{payload[i]=byte((i*29+17)%251)}
	hash:=assertEchoHashOnExistingFlow(t,c,payload)
	if p.targetAccepts.Load()!=targetBefore{t.Fatalf("target TCP reopened before=%d after=%d",targetBefore,p.targetAccepts.Load())}
	t.Logf("PASS authority Dialer 1->COMMIT_UNCERTAIN->2 Listener 1->2->2 candidate=%s digest=%s payload_hash=%x",irDone.CandidateID,irDone.PlanDigest,hash)
}

func TestDistributedCommitDisconnectBeforePublishProvesAbortSafe(t *testing.T){
	p:=startRecoveryRuntimePair(t,1);defer p.close(t)
	c:=openRecoveryFlow(t,p);defer c.Close()
	targetBefore:=p.targetAccepts.Load()

	beforePublish:=make(chan struct{})
	releasePublish:=make(chan struct{})
	var publishOnce sync.Once
	p.exRuntime.SetRecoveryFaultHookForTest(func(stage string)error{
		if stage=="before_listener_publish"{
			first:=false
			publishOnce.Do(func(){first=true;close(beforePublish)})
			if first{
				<-releasePublish
				return errors.New("deterministic disconnect before listener publish")
			}
		}
		return nil
	})

	proofReached:=make(chan struct{})
	releaseFresh:=make(chan struct{})
	var proofOnce sync.Once
	p.irRuntime.SetRecoveryFaultHookForTest(func(stage string)error{
		if stage=="after_not_committed_resolution"{
			proofOnce.Do(func(){close(proofReached)})
			<-releaseFresh
		}
		return nil
	})

	p.proxy.CutAll()
	select{case <-beforePublish:case <-time.After(8*time.Second):t.Fatal("listener never reached pre-publish barrier")}
	irPending:=oneRecoveryAuthority(t,p.irRuntime)
	exPending:=oneRecoveryAuthority(t,p.exRuntime)
	if irPending.Epoch!=1||exPending.Epoch!=1{t.Fatalf("authority changed before publish ir=%+v ex=%+v",irPending,exPending)}
	if irPending.CandidateID==""||irPending.CandidateID!=exPending.CandidateID||irPending.PlanDigest!=exPending.PlanDigest{
		t.Fatalf("pending transaction identity differs ir=%+v ex=%+v",irPending,exPending)
	}

	p.proxy.CutAll()
	close(releasePublish)
	select{case <-proofReached:case <-time.After(8*time.Second):t.Fatalf("NOT_COMMITTED proof was not resolved ir=%+v ex=%+v",p.irRuntime.RecoveryAuthoritiesForTest(),p.exRuntime.RecoveryAuthoritiesForTest())}
	irProof:=oneRecoveryAuthority(t,p.irRuntime)
	exProof:=oneRecoveryAuthority(t,p.exRuntime)
	if irProof.Epoch!=1||exProof.Epoch!=1||irProof.Frozen||exProof.Frozen{
		t.Fatalf("proof did not restore safe old authority ir=%+v ex=%+v",irProof,exProof)
	}
	if irProof.Owner!="shard-0-carrier-1"||exProof.Owner!="shard-0-carrier-1"{t.Fatalf("old owner lost after NOT_COMMITTED proof ir=%+v ex=%+v",irProof,exProof)}
	close(releaseFresh)

	irDone,exDone:=waitAuthorityPair(t,p,2,true)
	if irDone.Owner==""||irDone.Owner!=exDone.Owner{t.Fatalf("fresh recovery did not converge ir=%+v ex=%+v",irDone,exDone)}
	payload:=make([]byte,384*1024+91)
	for i:=range payload{payload[i]=byte((i*41+3)%251)}
	hash:=assertEchoHashOnExistingFlow(t,c,payload)
	if p.targetAccepts.Load()!=targetBefore{t.Fatalf("target TCP reopened before=%d after=%d",targetBefore,p.targetAccepts.Load())}
	t.Logf("PASS authority Dialer 1->pending/uncertain->proven NOT_COMMITTED->1->2 Listener 1->1->2 payload_hash=%x",hash)
}


func waitDialerCommitUncertain(t *testing.T,p *recoveryRuntimePair) (node.RecoveryAuthoritySnapshot,node.RecoveryAuthoritySnapshot) {
	t.Helper()
	deadline:=time.Now().Add(8*time.Second)
	for {
		ir:=p.irRuntime.RecoveryAuthoritiesForTest()
		ex:=p.exRuntime.RecoveryAuthoritiesForTest()
		if len(ir)==1&&len(ex)==1&&ir[0].Epoch==1&&ir[0].TxnState==session.RecoveryTxnUncertain&&ir[0].Frozen&&ex[0].Epoch==2 {
			return ir[0],ex[0]
		}
		if time.Now().After(deadline){t.Fatalf("commit uncertainty not reached ir=%+v ex=%+v",ir,ex)}
		time.Sleep(5*time.Millisecond)
	}
}

func assertResolvedTransactionAndFlow(t *testing.T,p *recoveryRuntimePair,c net.Conn,targetBefore int64,seed int)(node.RecoveryAuthoritySnapshot,node.RecoveryAuthoritySnapshot,[32]byte){
	t.Helper()
	ir,ex:=waitAuthorityPair(t,p,2,true)
	if ir.Owner==""||ir.Owner!=ex.Owner{t.Fatalf("authority owners differ ir=%+v ex=%+v",ir,ex)}
	if ir.PlanDigest==""||ir.PlanDigest!=ex.PlanDigest{t.Fatalf("authority digests differ ir=%+v ex=%+v",ir,ex)}
	payload:=make([]byte,256*1024+seed*17+73)
	for i:=range payload{payload[i]=byte((i*(seed+17)+seed*11+7)%251)}
	h:=assertEchoHashOnExistingFlow(t,c,payload)
	if p.targetAccepts.Load()!=targetBefore{t.Fatalf("target TCP reopened before=%d after=%d",targetBefore,p.targetAccepts.Load())}
	return ir,ex,h
}

func installOneShotPostPublishUncertainty(p *recoveryRuntimePair) {
	var fired atomic.Bool
	p.exRuntime.SetRecoveryFaultHookForTest(func(stage string)error{
		if stage=="after_listener_publish_before_commit_ack"&&fired.CompareAndSwap(false,true){
			return errors.New("deterministic post-publish ACK loss")
		}
		return nil
	})
}

func TestDistributedCommitFaultCCommitACKWriteCutResolves(t *testing.T){
	p:=startRecoveryRuntimePair(t,1);defer p.close(t)
	c:=openRecoveryFlow(t,p);defer c.Close();targetBefore:=p.targetAccepts.Load()
	entered:=make(chan struct{});release:=make(chan struct{})
	var once sync.Once
	p.exRuntime.SetRecoveryFaultHookForTest(func(stage string)error{
		if stage=="before_commit_ack_write"{
			first:=false
			once.Do(func(){first=true;close(entered)})
			if first{<-release}
		}
		return nil
	})
	p.proxy.CutAll()
	select{case <-entered:case <-time.After(8*time.Second):t.Fatal("listener never reached COMMIT_ACK write barrier")}
	ex:=oneRecoveryAuthority(t,p.exRuntime);ir:=oneRecoveryAuthority(t,p.irRuntime)
	if ex.Epoch!=2||ir.Epoch!=1{t.Fatalf("authority before ACK-write cut ir=%+v ex=%+v",ir,ex)}
	p.proxy.CutAll();close(release)
	irDone,_,h:=assertResolvedTransactionAndFlow(t,p,c,targetBefore,3)
	t.Logf("PASS matrix C ACK-write cut resolved exact txn epoch=%d digest=%s hash=%x",irDone.Epoch,irDone.PlanDigest,h)
}

func TestDistributedCommitFaultDACKWrittenButNotDecodedResolves(t *testing.T){
	p:=startRecoveryRuntimePair(t,1);defer p.close(t)
	c:=openRecoveryFlow(t,p);defer c.Close();targetBefore:=p.targetAccepts.Load()
	ackWritten:=make(chan struct{});releaseListener:=make(chan struct{});dialerSkipped:=make(chan struct{})
	var ackOnce sync.Once
	p.exRuntime.SetRecoveryFaultHookForTest(func(stage string)error{
		if stage=="after_commit_ack_write"{
			first:=false
			ackOnce.Do(func(){first=true;close(ackWritten)})
			if first{<-releaseListener}
		}
		return nil
	})
	var skipOnce atomic.Bool
	p.irRuntime.SetRecoveryFaultHookForTest(func(stage string)error{
		if stage=="after_commit_sent_before_ack"&&skipOnce.CompareAndSwap(false,true){
			<-ackWritten
			close(dialerSkipped)
			return errors.New("ACK intentionally not decoded")
		}
		return nil
	})
	p.proxy.CutAll()
	select{case <-dialerSkipped:case <-time.After(8*time.Second):t.Fatal("dialer did not skip written ACK")}
	close(releaseListener)
	irDone,_,h:=assertResolvedTransactionAndFlow(t,p,c,targetBefore,4)
	t.Logf("PASS matrix D ACK written but not decoded resolved epoch=%d digest=%s hash=%x",irDone.Epoch,irDone.PlanDigest,h)
}

func TestDistributedCommitFaultEDuplicateCOMMITIdempotent(t *testing.T){
	p:=startRecoveryRuntimePair(t,1);defer p.close(t)
	c:=openRecoveryFlow(t,p);defer c.Close();targetBefore:=p.targetAccepts.Load()
	p.irRuntime.SetRecoveryControlHookForTest(func(stage string,ctl *session.RecoveryControl)int{
		if stage=="dialer_commit_send"{return 2}
		return 1
	})
	p.proxy.CutAll()
	ir,_,h:=assertResolvedTransactionAndFlow(t,p,c,targetBefore,5)
	if ir.Epoch!=2{t.Fatalf("duplicate COMMIT advanced epoch more than once: %+v",ir)}
	t.Logf("PASS matrix E duplicate COMMIT idempotent epoch=%d hash=%x",ir.Epoch,h)
}

func TestDistributedCommitFaultFDuplicateCOMMITACKIdempotent(t *testing.T){
	p:=startRecoveryRuntimePair(t,1);defer p.close(t)
	c:=openRecoveryFlow(t,p);defer c.Close();targetBefore:=p.targetAccepts.Load()
	p.exRuntime.SetRecoveryControlHookForTest(func(stage string,ctl *session.RecoveryControl)int{
		if stage=="listener_commit_ack_send"{return 2}
		return 1
	})
	p.irRuntime.SetRecoveryControlHookForTest(func(stage string,ctl *session.RecoveryControl)int{
		if stage=="dialer_commit_ack_reads"{return 2}
		return 1
	})
	p.proxy.CutAll()
	ir,_,h:=assertResolvedTransactionAndFlow(t,p,c,targetBefore,6)
	if ir.Epoch!=2{t.Fatalf("duplicate COMMIT_ACK changed epoch: %+v",ir)}
	t.Logf("PASS matrix F duplicate COMMIT_ACK idempotent epoch=%d hash=%x",ir.Epoch,h)
}

func TestDistributedCommitFaultGCandidateDiesBeforeFinalizeResolves(t *testing.T){
	p:=startRecoveryRuntimePair(t,1);defer p.close(t)
	c:=openRecoveryFlow(t,p);defer c.Close();targetBefore:=p.targetAccepts.Load()
	var fired atomic.Bool
	p.irRuntime.SetRecoveryFaultHookForTest(func(stage string)error{
		if stage=="after_final_ack_send"&&fired.CompareAndSwap(false,true){
			p.proxy.CutAll()
			return errors.New("candidate died before dialer finalize")
		}
		return nil
	})
	p.proxy.CutAll()
	ir,_,h:=assertResolvedTransactionAndFlow(t,p,c,targetBefore,7)
	if !fired.Load(){t.Fatal("pre-finalize candidate failure hook did not fire")}
	t.Logf("PASS matrix G candidate failure before finalize resolved committed epoch=%d hash=%x",ir.Epoch,h)
}

func TestDistributedCommitFaultHCandidateDiesAfterFinalizeStartedResolves(t *testing.T){
	p:=startRecoveryRuntimePair(t,1);defer p.close(t)
	c:=openRecoveryFlow(t,p);defer c.Close();targetBefore:=p.targetAccepts.Load()
	var fired atomic.Bool
	p.irRuntime.SetRecoveryPostCommitFaultForTest(func(stage string)error{
		if stage=="after_authority_commit"&&fired.CompareAndSwap(false,true){
			p.proxy.CutAll()
			return errors.New("candidate died after finalize started")
		}
		return nil
	})
	p.proxy.CutAll()
	ir,_,h:=assertResolvedTransactionAndFlow(t,p,c,targetBefore,8)
	if !fired.Load(){t.Fatal("post-finalize-start fault did not fire")}
	t.Logf("PASS matrix H finalize-start failure retried idempotently epoch=%d hash=%x",ir.Epoch,h)
}

func TestDistributedCommitFaultIUncertainBlocksImmediateFreshRecovery(t *testing.T){
	p:=startRecoveryRuntimePair(t,1);defer p.closeAllowErrors()
	c:=openRecoveryFlow(t,p);defer c.Close();targetBefore:=p.targetAccepts.Load()
	installOneShotPostPublishUncertainty(p)
	statusEntered:=make(chan struct{});releaseStatus:=make(chan struct{})
	var once sync.Once
	p.irRuntime.SetRecoveryFaultHookForTest(func(stage string)error{
		if stage=="status_query_after_send"{
			first:=false
			once.Do(func(){first=true;close(statusEntered)})
			if first{<-releaseStatus}
		}
		return nil
	})
	p.proxy.CutAll()
	select{case <-statusEntered:case <-time.After(8*time.Second):t.Fatal("status resolution never began")}
	ir,ex:=waitDialerCommitUncertain(t,p)
	if _,ok:=interface{}(session.ErrCommitUncertain).(error);!ok{t.Fatal("invalid uncertainty error")}
	if err:=p.irRuntime.BeginRecoveryForTest("fresh-bypass");!errors.Is(err,session.ErrCommitUncertain){
		t.Fatalf("fresh transaction bypassed unresolved uncertainty err=%v ir=%+v ex=%+v",err,ir,ex)
	}
	close(releaseStatus)
	irDone,_,h:=assertResolvedTransactionAndFlow(t,p,c,targetBefore,9)
	t.Logf("PASS matrix I unresolved COMMIT_UNCERTAIN blocked fresh recovery then converged epoch=%d hash=%x",irDone.Epoch,h)
}

func TestDistributedCommitFaultJStatusReplyLossRetriesExactTransaction(t *testing.T){
	p:=startRecoveryRuntimePair(t,1);defer p.close(t)
	c:=openRecoveryFlow(t,p);defer c.Close();targetBefore:=p.targetAccepts.Load()
	var publishFail atomic.Bool
	replyWritten:=make(chan struct{});releaseReply:=make(chan struct{})
	var replyOnce sync.Once
	p.exRuntime.SetRecoveryFaultHookForTest(func(stage string)error{
		if stage=="after_listener_publish_before_commit_ack"&&publishFail.CompareAndSwap(false,true){
			return errors.New("force initial uncertainty")
		}
		if stage=="after_status_reply_write"{
			first:=false
			replyOnce.Do(func(){first=true;close(replyWritten)})
			if first{<-releaseReply}
		}
		return nil
	})
	dropped:=make(chan struct{})
	var dropOnce atomic.Bool
	p.irRuntime.SetRecoveryFaultHookForTest(func(stage string)error{
		if stage=="status_query_after_send"&&dropOnce.CompareAndSwap(false,true){
			<-replyWritten
			close(dropped)
			return errors.New("status reply deliberately not decoded")
		}
		return nil
	})
	p.proxy.CutAll()
	select{case <-dropped:case <-time.After(8*time.Second):t.Fatal("status reply loss interleaving not reached")}
	close(releaseReply)
	ir,_,h:=assertResolvedTransactionAndFlow(t,p,c,targetBefore,10)
	t.Logf("PASS matrix J lost status reply retried exact transaction epoch=%d digest=%s hash=%x",ir.Epoch,ir.PlanDigest,h)
}

func TestDistributedCommitFaultKDuplicateStatusQueryReplyIdempotent(t *testing.T){
	p:=startRecoveryRuntimePair(t,1);defer p.close(t)
	c:=openRecoveryFlow(t,p);defer c.Close();targetBefore:=p.targetAccepts.Load()
	installOneShotPostPublishUncertainty(p)
	p.irRuntime.SetRecoveryControlHookForTest(func(stage string,ctl *session.RecoveryControl)int{
		if stage=="dialer_status_query_send"{return 2}
		return 1
	})
	p.exRuntime.SetRecoveryControlHookForTest(func(stage string,ctl *session.RecoveryControl)int{
		if stage=="listener_status_reply_send"{return 2}
		return 1
	})
	p.proxy.CutAll()
	ir,_,h:=assertResolvedTransactionAndFlow(t,p,c,targetBefore,11)
	t.Logf("PASS matrix K duplicate STATUS query/reply idempotent epoch=%d digest=%s hash=%x",ir.Epoch,ir.PlanDigest,h)
}

func TestDistributedCommitFaultLResolutionDigestMismatchFailsClosedThenRetries(t *testing.T){
	p:=startRecoveryRuntimePair(t,1);defer p.close(t)
	c:=openRecoveryFlow(t,p);defer c.Close();targetBefore:=p.targetAccepts.Load()
	installOneShotPostPublishUncertainty(p)
	var mutated atomic.Bool
	p.exRuntime.SetRecoveryControlHookForTest(func(stage string,ctl *session.RecoveryControl)int{
		if stage=="listener_status_reply_send"&&mutated.CompareAndSwap(false,true){
			if len(ctl.PlanDigest)>2{ctl.PlanDigest="00"+ctl.PlanDigest[2:]}else{ctl.PlanDigest="00"}
		}
		return 1
	})
	var queryCount atomic.Int32
	retrySeen:=make(chan struct{});releaseRetry:=make(chan struct{})
	p.irRuntime.SetRecoveryControlHookForTest(func(stage string,ctl *session.RecoveryControl)int{
		if stage=="dialer_status_query_send"{
			n:=queryCount.Add(1)
			if n==2{close(retrySeen);<-releaseRetry}
		}
		return 1
	})
	p.proxy.CutAll()
	select{case <-retrySeen:case <-time.After(8*time.Second):t.Fatalf("mismatched status digest did not force exact retry ir=%+v ex=%+v",p.irRuntime.RecoveryAuthoritiesForTest(),p.exRuntime.RecoveryAuthoritiesForTest())}
	ir,ex:=waitDialerCommitUncertain(t,p)
	if !mutated.Load()||ir.Epoch!=1||ex.Epoch!=2||ir.CandidateID!=ex.CandidateID{
		t.Fatalf("digest mismatch did not fail closed ir=%+v ex=%+v mutated=%v",ir,ex,mutated.Load())
	}
	close(releaseRetry)
	irDone,_,h:=assertResolvedTransactionAndFlow(t,p,c,targetBefore,12)
	t.Logf("PASS matrix L mismatched resolution digest failed closed then exact retry converged epoch=%d hash=%x",irDone.Epoch,h)
}


func waitSingleFlowFrontier(t *testing.T,r *node.Runtime,pred func(session.RecoveryFlowFrontier)bool) session.RecoveryFlowFrontier {
	t.Helper()
	deadline:=time.Now().Add(8*time.Second)
	for {
		states:=r.RecoveryAuthoritiesForTest()
		if len(states)==1&&len(states[0].Flows)==1&&pred(states[0].Flows[0]){return states[0].Flows[0]}
		if time.Now().After(deadline){t.Fatalf("frontier wait timeout states=%+v",states)}
		time.Sleep(2*time.Millisecond)
	}
}

func TestFinalACKLossAfterDialerPublishConvergesBothFinalized(t *testing.T){
	p:=startRecoveryRuntimePair(t,1);defer p.close(t)
	c:=openRecoveryFlow(t,p);defer c.Close()
	targetBefore:=p.targetAccepts.Load()

	beforeProcess:=make(chan struct{})
	release:=make(chan struct{})
	var once sync.Once
	var fired atomic.Bool
	p.exRuntime.SetRecoveryFaultHookForTest(func(stage string)error{
		if stage=="before_listener_finalize_process"&&!fired.Swap(true){
			once.Do(func(){close(beforeProcess)})
			<-release
			return errors.New("drop FINALIZE after wire delivery before listener acceptance")
		}
		return nil
	})

	// Cut carrier N to start the N+1 replacement.
	p.proxy.CutAll()
	select{case <-beforeProcess:case <-time.After(8*time.Second):t.Fatal("listener never reached deterministic pre-FINALIZE-accept barrier")}
	irMid:=oneRecoveryAuthority(t,p.irRuntime)
	exMid:=oneRecoveryAuthority(t,p.exRuntime)
	if irMid.Epoch!=2||exMid.Epoch!=2{t.Fatalf("authority not published before finalization cut ir=%+v ex=%+v",irMid,exMid)}
	if irMid.CandidateID==""||irMid.CandidateID!=exMid.CandidateID||irMid.PlanDigest==""||irMid.PlanDigest!=exMid.PlanDigest||irMid.SessionID!=exMid.SessionID{
		t.Fatalf("transaction identity diverged before finalization cut ir=%+v ex=%+v",irMid,exMid)
	}
	if irMid.Owner=="shard-0-carrier-1"||exMid.Owner=="shard-0-carrier-1"{t.Fatalf("old epoch/owner re-authorized ir=%+v ex=%+v",irMid,exMid)}

	// FINALIZE has been decoded by Listener but is deliberately not accepted.
	// Kill that physical generation, then let both endpoints enter resolution.
	p.proxy.CutAll()
	close(release)

	irFinal,exFinal:=waitAuthorityPair(t,p,2,true)
	if irFinal.TxnState!=session.RecoveryTxnFinalized||exFinal.TxnState!=session.RecoveryTxnFinalized{
		t.Fatalf("distributed finalization did not converge ir=%+v ex=%+v",irFinal,exFinal)
	}
	if irFinal.CandidateID!=irMid.CandidateID||exFinal.CandidateID!=irMid.CandidateID||
		irFinal.PlanDigest!=irMid.PlanDigest||exFinal.PlanDigest!=irMid.PlanDigest||
		irFinal.SessionID!=irMid.SessionID||exFinal.SessionID!=irMid.SessionID{
		t.Fatalf("resolution changed transaction identity mid_ir=%+v final_ir=%+v final_ex=%+v",irMid,irFinal,exFinal)
	}
	if irFinal.CarrierGeneration<=irMid.CarrierGeneration||exFinal.CarrierGeneration<=exMid.CarrierGeneration{
		t.Fatalf("resolution did not bind a new physical carrier mid_ir=%+v final_ir=%+v mid_ex=%+v final_ex=%+v",irMid,irFinal,exMid,exFinal)
	}
	if n:=p.targetAccepts.Load()-targetBefore;n!=0{t.Fatalf("target TCP reopened during finalization resolution accepts_delta=%d",n)}

	payload:=make([]byte,384*1024+113)
	for i:=range payload{payload[i]=byte((i*41+17)%251)}
	h:=assertEchoHashOnExistingFlow(t,c,payload)
	if n:=p.targetAccepts.Load()-targetBefore;n!=0{t.Fatalf("target TCP reopened after finalization convergence accepts_delta=%d",n)}
	t.Logf("PASS FINALIZE write/accept asymmetry converged exact txn candidate=%s digest=%s generation=%d->%d hash=%x",irFinal.CandidateID,irFinal.PlanDigest,irMid.CarrierGeneration,irFinal.CarrierGeneration,h)
}

func TestReplayWriteSuccessWithoutPeerAcceptanceIsRetriedSafely(t *testing.T){
	p:=startRecoveryRuntimePair(t,1);defer p.close(t)
	c:=openRecoveryFlow(t,p);defer c.Close()
	targetBefore:=p.targetAccepts.Load()

	var mode atomic.Int32
	mode.Store(1)
	oldBlocked:=make(chan struct{})
	oldRelease:=make(chan struct{})
	replayBlocked:=make(chan struct{})
	replayRelease:=make(chan struct{})
	var oldOnce,replayOnce sync.Once
	p.exRuntime.SetRecoveryFrameHookForTest(func(stage string,fr protocol.Frame)bool{
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

	payload:=make([]byte,3*protocol.MaxPayloadSize+777)
	for i:=range payload{payload[i]=byte((i*29+7)%251)}
	wantHash:=sha256.Sum256(payload)
	got:=make([]byte,len(payload))
	readDone:=make(chan error,1)
	go func(){_,err:=io.ReadFull(c,got);readDone<-err}()
	writeDone:=make(chan error,1)
	go func(){_,err:=c.Write(payload);writeDone<-err}()

	select{case <-oldBlocked:case <-time.After(8*time.Second):t.Fatal("old carrier DATA never reached pre-accept barrier")}
	// Ensure at least two DATA frames have been produced locally while the
	// peer-accepted frontier remains behind the blocked first frame.
	before:=waitSingleFlowFrontier(t,p.irRuntime,func(f session.RecoveryFlowFrontier)bool{
		return f.TxNext>=f.PeerAccepted+protocol.MaxPayloadSize
	})
	if before.PeerAccepted>=before.TxNext{t.Fatalf("expected unaccepted old-carrier bytes frontier=%+v",before)}

	var writeOnce sync.Once
	replayWritten:=make(chan struct{})
	releaseWriter:=make(chan struct{})
	p.irRuntime.SetRecoveryPostCommitFaultForTest(func(stage string)error{
		if stage=="after_replay_write"{
			fired:=false
			writeOnce.Do(func(){fired=true;close(replayWritten)})
			if fired{
				<-releaseWriter
				return errors.New("cut after replay write success before peer acceptance")
			}
		}
		return nil
	})

	mode.Store(2)
	p.proxy.CutAll()
	close(oldRelease)

	select{case <-replayBlocked:case <-time.After(8*time.Second):t.Fatal("replacement replay never reached receiver pre-accept barrier")}
	select{case <-replayWritten:case <-time.After(8*time.Second):t.Fatal("sender never reported successful replay write")}
	mid:=waitSingleFlowFrontier(t,p.irRuntime,func(f session.RecoveryFlowFrontier)bool{return true})
	if mid.PeerAccepted>=mid.TxNext{t.Fatalf("local replay write incorrectly advanced peer-accepted frontier: %+v",mid)}
	if mid.PeerAccepted>before.PeerAccepted{t.Fatalf("peer-accepted frontier advanced without receiver acceptance before=%+v mid=%+v",before,mid)}

	// Kill generation 2 while the receiver has decoded but not accepted frame
	// #1. The exact finalized transaction must rebind and conservatively resend
	// from the unchanged peer-accepted offset.
	p.proxy.CutAll()
	mode.Store(3)
	close(replayRelease)
	close(releaseWriter)

	select{case err:=<-writeDone:if err!=nil{t.Fatalf("client payload write: %v",err)};case <-time.After(12*time.Second):t.Fatal("client write timeout")}
	select{case err:=<-readDone:if err!=nil{t.Fatalf("client payload read: %v",err)};case <-time.After(20*time.Second):t.Fatal("client payload read timeout")}
	have:=sha256.Sum256(got)
	if !bytes.Equal(got,payload)||have!=wantHash{t.Fatalf("replay delivery mismatch got_hash=%x want_hash=%x",have,wantHash)}
	irFinal,exFinal:=waitAuthorityPair(t,p,2,true)
	if irFinal.CandidateID!=exFinal.CandidateID||irFinal.PlanDigest!=exFinal.PlanDigest{t.Fatalf("same transaction not preserved ir=%+v ex=%+v",irFinal,exFinal)}
	if n:=p.targetAccepts.Load()-targetBefore;n!=0{t.Fatalf("target TCP reopened during replay retry accepts_delta=%d",n)}
	t.Logf("PASS local write != peer acceptance; conservative replay deduped application bytes hash=%x accepted_before=%d accepted_mid=%d",have,before.PeerAccepted,mid.PeerAccepted)
}


func closeWriteRecoveryConn(t *testing.T,c net.Conn){
	t.Helper()
	cw,ok:=c.(interface{CloseWrite() error})
	if !ok{t.Fatalf("connection %T does not support CloseWrite",c)}
	if err:=cw.CloseWrite();err!=nil{t.Fatal(err)}
}

func waitRecoveryFlowsGone(t *testing.T,p *recoveryRuntimePair){
	t.Helper()
	deadline:=time.Now().Add(10*time.Second)
	for{
		ir:=p.irRuntime.RecoveryAuthoritiesForTest()
		ex:=p.exRuntime.RecoveryAuthoritiesForTest()
		if len(ir)==1&&len(ex)==1&&len(ir[0].Flows)==0&&len(ex[0].Flows)==0{return}
		if time.Now().After(deadline){t.Fatalf("flow termination stuck ir=%+v ex=%+v",ir,ex)}
		time.Sleep(2*time.Millisecond)
	}
}

func TestReplayPeerAcceptanceFrontierAdvancesMonotonically(t *testing.T){
	p:=startRecoveryRuntimePair(t,1);defer p.close(t)
	c:=openRecoveryFlow(t,p);defer c.Close()
	_ = c.SetDeadline(time.Now().Add(20*time.Second))
	targetBefore:=p.targetAccepts.Load()

	payload:=make([]byte,48*1024+37)
	for i:=range payload{payload[i]=byte((i*17+3)%251)}
	if _,err:=c.Write(payload);err!=nil{t.Fatal(err)}
	got:=make([]byte,len(payload))
	if _,err:=io.ReadFull(c,got);err!=nil{t.Fatal(err)}
	if !bytes.Equal(got,payload){t.Fatal("pre-cut accepted payload mismatch")}

	before:=waitSingleFlowFrontier(t,p.irRuntime,func(f session.RecoveryFlowFrontier)bool{
		return f.TxNext>uint64(len("commit-safety-probe"))&&f.PeerAccepted==f.TxNext
	})
	p.proxy.CutAll()
	irFinal,exFinal:=waitAuthorityPair(t,p,2,true)
	if irFinal.TxnState!=session.RecoveryTxnFinalized||exFinal.TxnState!=session.RecoveryTxnFinalized{
		t.Fatalf("recovery did not finalize ir=%+v ex=%+v",irFinal,exFinal)
	}
	after:=waitSingleFlowFrontier(t,p.irRuntime,func(f session.RecoveryFlowFrontier)bool{
		return f.PeerAccepted>=before.PeerAccepted
	})
	if after.PeerAccepted<before.PeerAccepted{t.Fatalf("peer acceptance frontier regressed before=%+v after=%+v",before,after)}
	if after.ReplaySource<before.PeerAccepted{t.Fatalf("proven accepted bytes remained below replay source before=%+v after=%+v",before,after)}

	probe:=[]byte("accepted-frontier-still-live")
	if _,err:=c.Write(probe);err!=nil{t.Fatal(err)}
	echo:=make([]byte,len(probe));if _,err:=io.ReadFull(c,echo);err!=nil{t.Fatal(err)}
	if !bytes.Equal(echo,probe){t.Fatal("post-recovery echo mismatch")}
	if n:=p.targetAccepts.Load()-targetBefore;n!=0{t.Fatalf("target TCP reopened accepts_delta=%d",n)}
	t.Logf("PASS peer-accepted frontier monotonic accepted=%d replay_source=%d",after.PeerAccepted,after.ReplaySource)
}

func TestFINWriteSuccessWithoutPeerAcceptanceRetriesIdempotently(t *testing.T){
	p:=startRecoveryRuntimePair(t,1);defer p.close(t)
	c:=openRecoveryFlow(t,p);defer c.Close()
	_ = c.SetDeadline(time.Now().Add(20*time.Second))
	targetBefore:=p.targetAccepts.Load()

	blocked:=make(chan struct{})
	release:=make(chan struct{})
	var once sync.Once
	p.exRuntime.SetRecoveryFrameHookForTest(func(stage string,fr protocol.Frame)bool{
		if stage!="before_fin_accept"{return false}
		first:=false
		once.Do(func(){first=true;close(blocked)})
		if first{
			<-release
			return true
		}
		return false
	})

	closeWriteRecoveryConn(t,c)
	select{case <-blocked:case <-time.After(8*time.Second):t.Fatal("FIN never reached deterministic pre-accept barrier")}
	mid:=waitSingleFlowFrontier(t,p.irRuntime,func(f session.RecoveryFlowFrontier)bool{return f.FinSent&&!f.FinAcked})
	if mid.FinAcked{t.Fatalf("FIN acceptance advanced before proof: %+v",mid)}

	p.proxy.CutAll()
	close(release)
	irFinal,exFinal:=waitAuthorityPair(t,p,2,true)
	if irFinal.Epoch!=2||exFinal.Epoch!=2{t.Fatalf("FIN recovery authority mismatch ir=%+v ex=%+v",irFinal,exFinal)}

	var one [1]byte
	n,err:=c.Read(one[:])
	if n!=0||!errors.Is(err,io.EOF){t.Fatalf("expected one application EOF after FIN retry n=%d err=%v",n,err)}
	waitRecoveryFlowsGone(t,p)
	if n:=p.targetAccepts.Load()-targetBefore;n!=0{t.Fatalf("target TCP reopened during FIN retry accepts_delta=%d",n)}
	t.Log("PASS FIN local write without peer acceptance retried idempotently; one EOF and no target reopen")
}

func TestFINACKWriteSuccessWithoutPeerAcceptanceRetriesIdempotently(t *testing.T){
	p:=startRecoveryRuntimePair(t,1);defer p.close(t)
	c:=openRecoveryFlow(t,p);defer c.Close()
	_ = c.SetDeadline(time.Now().Add(20*time.Second))
	targetBefore:=p.targetAccepts.Load()

	blocked:=make(chan struct{})
	release:=make(chan struct{})
	var once sync.Once
	p.irRuntime.SetRecoveryFrameHookForTest(func(stage string,fr protocol.Frame)bool{
		if stage!="before_fin_ack_accept"{return false}
		first:=false
		once.Do(func(){first=true;close(blocked)})
		if first{
			<-release
			return true
		}
		return false
	})

	closeWriteRecoveryConn(t,c)
	select{case <-blocked:case <-time.After(8*time.Second):t.Fatal("FIN_ACK never reached deterministic pre-accept barrier")}
	irMid:=waitSingleFlowFrontier(t,p.irRuntime,func(f session.RecoveryFlowFrontier)bool{return f.FinSent&&!f.FinAcked})
	exMid:=waitSingleFlowFrontier(t,p.exRuntime,func(f session.RecoveryFlowFrontier)bool{return f.FinAckSent&&!f.FinAckConfirmed})
	if irMid.FinAcked{t.Fatalf("FIN_ACK write incorrectly became local acceptance: %+v",irMid)}
	if exMid.FinAckConfirmed{t.Fatalf("FIN_ACK confirmation appeared before peer acceptance: %+v",exMid)}

	p.proxy.CutAll()
	close(release)
	irFinal,exFinal:=waitAuthorityPair(t,p,2,true)
	if irFinal.Epoch!=2||exFinal.Epoch!=2{t.Fatalf("FIN_ACK recovery authority mismatch ir=%+v ex=%+v",irFinal,exFinal)}

	var one [1]byte
	n,err:=c.Read(one[:])
	if n!=0||!errors.Is(err,io.EOF){t.Fatalf("expected clean EOF after FIN_ACK retry n=%d err=%v",n,err)}
	waitRecoveryFlowsGone(t,p)
	if n:=p.targetAccepts.Load()-targetBefore;n!=0{t.Fatalf("target TCP reopened during FIN_ACK retry accepts_delta=%d",n)}
	t.Log("PASS FIN_ACK local write without peer acceptance retried until confirmation; no duplicate transition")
}
