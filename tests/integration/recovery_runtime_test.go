package integration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
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
	"github.com/zarkmakerburg/baft/internal/recordshape"
	"github.com/zarkmakerburg/baft/internal/securityinternal"
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
	go func(){pair.exDone<-node.NewRuntime().Run(ctx,ex)}()
	waitTCP(t,ex.Server.Listen,time.Now().Add(6*time.Second))
	go func(){pair.irDone<-node.NewRuntime().Run(ctx,ir)}()
	waitTCP(t,ir.Routes[0].Listen,time.Now().Add(8*time.Second))
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
	if n:=p.targetAccepts.Load();n!=1{t.Fatalf("target TCP socket was reopened: accepts=%d",n)}
	t.Logf("PASS active flow survived carrier replacement bytes=%d hash=%x",len(got),have)
}

func TestMultiFlowCarrierReplacementNoDuplicateOrLoss(t *testing.T){
	p:=startRecoveryRuntimePair(t,1);defer p.close(t)
	const flows=8
	var wg sync.WaitGroup
	errCh:=make(chan error,flows)
	start:=make(chan struct{})
	for f:=0;f<flows;f++{
		f:=f;wg.Add(1)
		go func(){defer wg.Done();<-start
			payload:=make([]byte,512*1024+f*97);for i:=range payload{payload[i]=byte((i*13+f*19)%251)}
			c,err:=net.DialTimeout("tcp",p.ir.Routes[0].Listen,time.Second);if err!=nil{errCh<-err;return}
			defer c.Close();_ = c.SetDeadline(time.Now().Add(20*time.Second))
			got:=make([]byte,len(payload));rd:=make(chan error,1);go func(){_,e:=io.ReadFull(c,got);rd<-e}()
			for off:=0;off<len(payload);{
				n:=8192;if len(payload)-off<n{n=len(payload)-off}
				nw,e:=c.Write(payload[off:off+n]);if e!=nil{errCh<-e;return};off+=nw;time.Sleep(250*time.Microsecond)
			}
			if e:=<-rd;e!=nil{errCh<-e;return}
			if !bytes.Equal(got,payload){errCh<-io.ErrUnexpectedEOF}
		}()
	}
	close(start);time.Sleep(40*time.Millisecond);p.proxy.CutAll()
	wg.Wait();close(errCh);for e:=range errCh{if e!=nil{t.Fatal(e)}}
	if n:=p.targetAccepts.Load();n!=flows{t.Fatalf("target flow identity/socket count=%d want=%d",n,flows)}
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
	if n:=p.targetAccepts.Load();n!=6{t.Fatalf("route recovery reopened/mixed target flows accepts=%d",n)}
	t.Log("PASS six route/session identities remained isolated across replacement")
}
