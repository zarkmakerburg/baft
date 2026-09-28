package integration_test

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/bcc"
	"github.com/zarkmakerburg/baft/internal/config"
	"github.com/zarkmakerburg/baft/internal/node"
	"github.com/zarkmakerburg/baft/internal/recordshape"
	"github.com/zarkmakerburg/baft/internal/securityinternal"
	"github.com/zarkmakerburg/baft/internal/telemetry"
)

func TestRecoveryTelemetryFinanceRemainExact(t *testing.T){
	certs:=testPKI(t);dir:=t.TempDir()
	write:=func(name string,b []byte)string{p:=filepath.Join(dir,name);if err:=os.WriteFile(p,b,0600);err!=nil{t.Fatal(err)};return p}
	ca:=write("ca.pem",pem.EncodeToMemory(&pem.Block{Type:"CERTIFICATE",Bytes:certs.caDER}))
	cert:=write("server.pem",pem.EncodeToMemory(&pem.Block{Type:"CERTIFICATE",Bytes:certs.server.Certificate[0]}))
	der,err:=x509.MarshalPKCS8PrivateKey(certs.server.PrivateKey);if err!=nil{t.Fatal(err)}
	key:=write("server.key",pem.EncodeToMemory(&pem.Block{Type:"PRIVATE KEY",Bytes:der}))

	store,err:=bcc.OpenStore(filepath.Join(dir,"bcc.json"));if err!=nil{t.Fatal(err)}
	const nodeID="ir-recovery-finance"
	const token="recovery-finance-token"
	if _,err:=store.UpsertNode(bcc.Node{ID:nodeID,Alias:"IR Recovery Finance",Address:"127.0.0.1:29970",Role:"worker"},token);err!=nil{t.Fatal(err)}
	const costRate int64=1<<28
	const revenueRate int64=1<<29
	if err:=store.SetFinancePolicy(nodeID,costRate,revenueRate);err!=nil{t.Fatal(err)}
	app,err:=bcc.NewServer(store,"admin");if err!=nil{t.Fatal(err)}
	bccHTTP:=httptest.NewServer(app.Handler());defer bccHTTP.Close()
	t.Setenv("BAFT_AGENT_TOKEN",token)

	targetLn,err:=net.Listen("tcp","127.0.0.1:0");if err!=nil{t.Fatal(err)}
	defer targetLn.Close()
	var targetAccepts atomic.Int64
	go func(){
		for{
			c,e:=targetLn.Accept();if e!=nil{return}
			targetAccepts.Add(1)
			go func(x net.Conn){defer x.Close();_,_=io.Copy(x,x)}(c)
		}
	}()

	exKey,err:=securityinternal.GenerateKeyPair();if err!=nil{t.Fatal(err)}
	irKey,err:=securityinternal.GenerateKeyPair();if err!=nil{t.Fatal(err)}
	exPath:=filepath.Join(dir,"ex-noise.json");irPath:=filepath.Join(dir,"ir-noise.json")
	if err:=securityinternal.SaveKeyPair(exPath,exKey);err!=nil{t.Fatal(err)}
	if err:=securityinternal.SaveKeyPair(irPath,irKey);err!=nil{t.Fatal(err)}
	exPub,_:=securityinternal.EncodePublicKey(exKey.Public)
	irPub,_:=securityinternal.EncodePublicKey(irKey.Public)

	ex,err:=config.LoadFile("../../configs/example-ex.yaml");if err!=nil{t.Fatal(err)}
	ex.Node.ID="ex-recovery-finance"
	ex.Server.Listen=reserveAddress(t);ex.Server.ServerName="ex.test"
	ex.Server.AllowedPeerIdentities=[]string{"urn:baft:node:"+nodeID}
	ex.Management.UnixSocket=filepath.Join(dir,"ex.sock");ex.Management.MetricsListen=reserveAddress(t)
	ex.Transport.Shards=1
	ex.TLS=config.TLS{MinVersion:"1.3",CAFile:ca,CertFile:cert,KeyFile:key}
	ex.Noise=&config.Noise{KeyFile:exPath,PeerPublicKey:irPub,RecordShaping:recordshape.Config{}}
	ex.Recovery=config.Recovery{Enabled:true,RetentionSeconds:10,Mode:"same_process"}
	ex.Routes=[]config.Route{{ID:"finance-route",Direction:"inbound",Target:targetLn.Addr().String(),AllowedPeers:[]string{"urn:baft:node:"+nodeID}}}

	proxy:=newCutProxy(t,ex.Server.Listen);defer proxy.Close()
	ir,err:=config.LoadFile("../../configs/example-ir.yaml");if err!=nil{t.Fatal(err)}
	ir.Node.ID=nodeID
	ir.Peer.Address=proxy.Addr();ir.Peer.ServerName="ex.test";ir.Peer.AllowedIdentity="urn:baft:node:ex-recovery-finance"
	ir.Management.UnixSocket=filepath.Join(dir,"ir.sock");ir.Management.MetricsListen=reserveAddress(t)
	ir.Transport.Shards=1;ir.TLS=ex.TLS
	ir.Noise=&config.Noise{KeyFile:irPath,PeerPublicKey:exPub,RecordShaping:recordshape.Config{}}
	ir.Recovery=ex.Recovery
	ir.Routes=[]config.Route{{ID:"local-finance",Direction:"outbound",Listen:reserveAddress(t),RemoteRoute:"finance-route"}}
	spoolPath:=filepath.Join(dir,"telemetry.spool")
	ir.Telemetry=config.Telemetry{
		Enabled:true,BCCURL:bccHTTP.URL,AgentTokenEnv:"BAFT_AGENT_TOKEN",
		IntervalSeconds:1,RouteProbeIntervalSeconds:1,SpoolPath:spoolPath,SpoolMaxPending:64,
	}
	if err:=config.Validate(ex);err!=nil{t.Fatalf("EX: %v",err)}
	if err:=config.Validate(ir);err!=nil{t.Fatalf("IR: %v",err)}

	ctx,cancel:=context.WithTimeout(context.Background(),30*time.Second);defer cancel()
	exDone:=make(chan error,1);irDone:=make(chan error,1)
	go func(){exDone<-node.NewRuntime().Run(ctx,ex)}()
	waitTCP(t,ex.Server.Listen,time.Now().Add(6*time.Second))
	go func(){irDone<-node.NewRuntime().Run(ctx,ir)}()
	waitTCP(t,ir.Routes[0].Listen,time.Now().Add(8*time.Second))
	time.Sleep(100*time.Millisecond)
	baselineAccepts:=targetAccepts.Load()

	c,err:=net.DialTimeout("tcp",ir.Routes[0].Listen,time.Second);if err!=nil{t.Fatal(err)}
	defer c.Close();_ = c.SetDeadline(time.Now().Add(20*time.Second))

	sendEcho:=func(payload []byte){
		t.Helper()
		if _,err:=c.Write(payload);err!=nil{t.Fatal(err)}
		got:=make([]byte,len(payload))
		if _,err:=io.ReadFull(c,got);err!=nil{t.Fatal(err)}
		if !bytes.Equal(got,payload){t.Fatal("echo payload mismatch")}
	}
	first:=bytes.Repeat([]byte{0x31},384*1024+17)
	second:=bytes.Repeat([]byte{0xa7},512*1024+29)
	sendEcho(first)

	var telemetryBoot string
	deadline:=time.Now().Add(5*time.Second)
	for{
		sp,err:=telemetry.OpenSpool(spoolPath,nodeID,64)
		if err==nil&&sp.BootID()!=""{telemetryBoot=sp.BootID();break}
		if time.Now().After(deadline){t.Fatalf("telemetry spool boot unavailable: %v",err)}
		time.Sleep(50*time.Millisecond)
	}

	proxy.CutAll()
	time.Sleep(350*time.Millisecond)
	sendEcho(second)

	wantBytes:=uint64(len(first)+len(second))
	deadline=time.Now().Add(8*time.Second)
	for{
		fs:=store.FinanceSnapshot()
		cur,ok:=store.TelemetrySnapshot(nodeID)
		sp,spErr:=telemetry.OpenSpool(spoolPath,nodeID,64)
		if len(fs)==1&&ok&&spErr==nil&&fs[0].IngressBytes==wantBytes&&fs[0].EgressBytes==wantBytes{
			if cur.BootID!=telemetryBoot||sp.BootID()!=telemetryBoot{
				t.Fatalf("telemetry BootID changed during same-process ECRL recovery cursor=%s spool=%s want=%s",cur.BootID,sp.BootID(),telemetryBoot)
			}
			total:=wantBytes*2
			wantCost:=int64((total*uint64(costRate))/(1<<30))
			wantRevenue:=int64((total*uint64(revenueRate))/(1<<30))
			if fs[0].CostMicros!=wantCost||fs[0].RevenueMicros!=wantRevenue||fs[0].ProfitMicros!=wantRevenue-wantCost{
				t.Fatalf("finance mismatch got=%+v want cost=%d revenue=%d",fs[0],wantCost,wantRevenue)
			}
			time.Sleep(1200*time.Millisecond)
			again:=store.FinanceSnapshot()[0]
			if again.IngressBytes!=wantBytes||again.EgressBytes!=wantBytes||again.CostMicros!=wantCost||again.RevenueMicros!=wantRevenue{
				t.Fatalf("post-replacement telemetry double-counted: %+v",again)
			}
			if targetAccepts.Load()-baselineAccepts!=1{
				t.Fatalf("target socket reopened during recovery test_accepts=%d baseline=%d total=%d",targetAccepts.Load()-baselineAccepts,baselineAccepts,targetAccepts.Load())
			}
			t.Logf("PASS recovery telemetry/finance exact boot=%s ingress=%d egress=%d seq=%d",telemetryBoot,again.IngressBytes,again.EgressBytes,cur.Sequence)
			break
		}
		if time.Now().After(deadline){t.Fatalf("finance recovery deadline finance=%+v cursor=%+v ok=%v spoolErr=%v",fs,cur,ok,spErr)}
		time.Sleep(100*time.Millisecond)
	}

	cancel()
	for name,ch:=range map[string]<-chan error{"ex":exDone,"ir":irDone}{
		select{
		case e:=<-ch:if e!=nil&&!errors.Is(e,context.Canceled){t.Fatalf("%s runtime: %v",name,e)}
		case <-time.After(6*time.Second):t.Fatalf("%s shutdown timeout",name)
		}
	}
}
