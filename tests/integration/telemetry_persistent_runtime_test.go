package integration_test

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"net/http"
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

func TestRuntimePersistentTelemetrySurvivesOutageAndRestart(t *testing.T){
	certs:=testPKI(t)
	dir:=t.TempDir()
	write:=func(name string,b []byte) string{
		p:=filepath.Join(dir,name)
		if err:=os.WriteFile(p,b,0600);err!=nil{t.Fatal(err)}
		return p
	}
	ca:=write("ca.pem",pem.EncodeToMemory(&pem.Block{Type:"CERTIFICATE",Bytes:certs.caDER}))
	cert:=write("server.pem",pem.EncodeToMemory(&pem.Block{Type:"CERTIFICATE",Bytes:certs.server.Certificate[0]}))
	der,err:=x509.MarshalPKCS8PrivateKey(certs.server.PrivateKey);if err!=nil{t.Fatal(err)}
	tlsKey:=write("server.key",pem.EncodeToMemory(&pem.Block{Type:"PRIVATE KEY",Bytes:der}))

	store,err:=bcc.OpenStore(filepath.Join(dir,"bcc-state.json"));if err!=nil{t.Fatal(err)}
	const agentToken="persistent-runtime-token"
	const nodeID="ir-persistent-runtime"
	_,err=store.UpsertNode(bcc.Node{ID:nodeID,Alias:"IR Persistent",Address:"127.0.0.1:29989",Role:"worker"},agentToken)
	if err!=nil{t.Fatal(err)}
	const costRate int64=1<<28
	const revenueRate int64=1<<29
	if err:=store.SetFinancePolicy(nodeID,costRate,revenueRate);err!=nil{t.Fatal(err)}
	app,err:=bcc.NewServer(store,"admin");if err!=nil{t.Fatal(err)}
	var bccAvailable atomic.Bool
	bccHTTP:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		if !bccAvailable.Load(){http.Error(w,"offline",http.StatusServiceUnavailable);return}
		app.Handler().ServeHTTP(w,r)
	}))
	defer bccHTTP.Close()
	t.Setenv("BAFT_AGENT_TOKEN",agentToken)

	target,err:=net.Listen("tcp","127.0.0.1:0");if err!=nil{t.Fatal(err)}
	defer target.Close()
	go func(){for{c,e:=target.Accept();if e!=nil{return};go func(x net.Conn){defer x.Close();_,_=io.Copy(x,x)}(c)}}()

	exKey,err:=securityinternal.GenerateKeyPair();if err!=nil{t.Fatal(err)}
	irKey,err:=securityinternal.GenerateKeyPair();if err!=nil{t.Fatal(err)}
	exPath:=filepath.Join(dir,"ex-noise.json");irPath:=filepath.Join(dir,"ir-noise.json")
	if err:=securityinternal.SaveKeyPair(exPath,exKey);err!=nil{t.Fatal(err)}
	if err:=securityinternal.SaveKeyPair(irPath,irKey);err!=nil{t.Fatal(err)}
	exPub,_:=securityinternal.EncodePublicKey(exKey.Public)
	irPub,_:=securityinternal.EncodePublicKey(irKey.Public)

	ex,err:=config.LoadFile("../../configs/example-ex.yaml");if err!=nil{t.Fatal(err)}
	ex.Node.ID="ex-persistent-runtime"
	ex.Server.Listen=reserveAddress(t)
	ex.Server.ServerName="ex.test"
	ex.Server.AllowedPeerIdentities=[]string{"urn:baft:node:"+nodeID}
	ex.Management.UnixSocket=filepath.Join(dir,"ex.sock")
	ex.Management.MetricsListen=reserveAddress(t)
	ex.Transport.Shards=1
	ex.Routes[0].Target=target.Addr().String()
	ex.Routes[0].AllowedPeers=[]string{"urn:baft:node:"+nodeID}
	ex.TLS=config.TLS{MinVersion:"1.3",CAFile:ca,CertFile:cert,KeyFile:tlsKey,SessionTickets:false}
	ex.Noise=&config.Noise{KeyFile:exPath,PeerPublicKey:irPub,RecordShaping:recordshape.Config{}}

	spoolPath:=filepath.Join(dir,"runtime-telemetry.spool")
	ir,err:=config.LoadFile("../../configs/example-ir.yaml");if err!=nil{t.Fatal(err)}
	ir.Node.ID=nodeID
	ir.Peer.Address=ex.Server.Listen
	ir.Peer.ServerName="ex.test"
	ir.Peer.AllowedIdentity="urn:baft:node:ex-persistent-runtime"
	ir.Management.UnixSocket=filepath.Join(dir,"ir.sock")
	ir.Management.MetricsListen=reserveAddress(t)
	ir.Transport.Shards=1
	ir.Routes[0].Listen=reserveAddress(t)
	ir.TLS=ex.TLS
	ir.Noise=&config.Noise{KeyFile:irPath,PeerPublicKey:exPub,RecordShaping:recordshape.Config{}}
	ir.Telemetry=config.Telemetry{
		Enabled:true,BCCURL:bccHTTP.URL,AgentTokenEnv:"BAFT_AGENT_TOKEN",
		IntervalSeconds:1,RouteProbeIntervalSeconds:1,
		SpoolPath:spoolPath,SpoolMaxPending:32,
	}

	if err:=config.Validate(ex);err!=nil{t.Fatalf("EX config: %v",err)}
	if err:=config.Validate(ir);err!=nil{t.Fatalf("IR config: %v",err)}

	root,cancelRoot:=context.WithTimeout(context.Background(),30*time.Second);defer cancelRoot()
	exCtx,cancelEX:=context.WithCancel(root);defer cancelEX()
	exDone:=make(chan error,1)
	go func(){exDone<-node.NewRuntime().Run(exCtx,ex)}()
	waitTCP(t,ex.Server.Listen,time.Now().Add(8*time.Second))

	irCtx1,cancelIR1:=context.WithCancel(root)
	irDone1:=make(chan error,1)
	go func(){irDone1<-node.NewRuntime().Run(irCtx1,ir)}()
	waitTCP(t,ir.Routes[0].Listen,time.Now().Add(8*time.Second))

	const payloadSize=1<<20
	payload:=bytes.Repeat([]byte{0x6b},payloadSize)
	conn,err:=net.DialTimeout("tcp",ir.Routes[0].Listen,time.Second);if err!=nil{t.Fatal(err)}
	_ = conn.SetDeadline(time.Now().Add(8*time.Second))
	if _,err=conn.Write(payload);err!=nil{t.Fatal(err)}
	got:=make([]byte,len(payload))
	if _,err=io.ReadFull(conn,got);err!=nil{t.Fatal(err)}
	_ = conn.Close()
	if !bytes.Equal(got,payload){t.Fatal("echo mismatch")}

	var boot string
	deadline:=time.Now().Add(6*time.Second)
	for{
		sp,err:=telemetry.OpenSpool(spoolPath,nodeID,32)
		if err==nil{
			in,out,_:=sp.LastCounters()
			if in==payloadSize&&out==payloadSize&&sp.PendingCount()>=1{
				boot=sp.BootID()
				break
			}
		}
		if time.Now().After(deadline){t.Fatalf("runtime telemetry never reached durable spool: %v",err)}
		time.Sleep(50*time.Millisecond)
	}

	cancelIR1()
	select{
	case e:=<-irDone1:if e!=nil{t.Fatalf("first IR runtime: %v",e)}
	case <-time.After(6*time.Second):t.Fatal("first IR runtime shutdown timeout")
	}

	// Restart while BCC remains unavailable. The same spool/BootID must be used.
	irCtx2,cancelIR2:=context.WithCancel(root);defer cancelIR2()
	irDone2:=make(chan error,1)
	go func(){irDone2<-node.NewRuntime().Run(irCtx2,ir)}()
	waitTCP(t,ir.Routes[0].Listen,time.Now().Add(8*time.Second))
	sp,err:=telemetry.OpenSpool(spoolPath,nodeID,32);if err!=nil{t.Fatal(err)}
	if sp.BootID()!=boot{t.Fatalf("runtime restart changed BootID %s -> %s",boot,sp.BootID())}
	if sp.PendingCount()==0{t.Fatal("pending telemetry disappeared during restart")}

	bccAvailable.Store(true)
	deadline=time.Now().Add(8*time.Second)
	for{
		fs:=store.FinanceSnapshot()
		cur,ok:=store.TelemetrySnapshot(nodeID)
		sp,spErr:=telemetry.OpenSpool(spoolPath,nodeID,32)
		if len(fs)==1&&ok&&spErr==nil&&sp.PendingCount()==0&&
			fs[0].IngressBytes==payloadSize&&fs[0].EgressBytes==payloadSize&&cur.BootID==boot{
			total:=uint64(payloadSize*2)
			wantCost:=int64((total*uint64(costRate))/(1<<30))
			wantRevenue:=int64((total*uint64(revenueRate))/(1<<30))
			if fs[0].CostMicros!=wantCost||fs[0].RevenueMicros!=wantRevenue||fs[0].ProfitMicros!=wantRevenue-wantCost{
				t.Fatalf("finance mismatch got=%+v want cost=%d revenue=%d",fs[0],wantCost,wantRevenue)
			}
			// Let at least one further same-epoch sample arrive; zero traffic delta
			// must not change the financial total.
			time.Sleep(1200*time.Millisecond)
			again:=store.FinanceSnapshot()[0]
			if again.IngressBytes!=payloadSize||again.EgressBytes!=payloadSize||again.CostMicros!=wantCost||again.RevenueMicros!=wantRevenue{
				t.Fatalf("post-recovery periodic telemetry double-counted: %+v",again)
			}
			t.Logf("PASS runtime spool outage+restart boot=%s ingress=%d egress=%d sequence=%d",boot,again.IngressBytes,again.EgressBytes,cur.Sequence)
			break
		}
		if time.Now().After(deadline){t.Fatalf("recovery deadline finance=%+v cursor=%+v ok=%v spoolErr=%v",fs,cur,ok,spErr)}
		time.Sleep(100*time.Millisecond)
	}

	cancelIR2()
	select{
	case e:=<-irDone2:if e!=nil{t.Fatalf("second IR runtime: %v",e)}
	case <-time.After(6*time.Second):t.Fatal("second IR runtime shutdown timeout")
	}
	cancelEX()
	select{
	case e:=<-exDone:if e!=nil{t.Fatalf("EX runtime: %v",e)}
	case <-time.After(6*time.Second):t.Fatal("EX runtime shutdown timeout")
	}
}
