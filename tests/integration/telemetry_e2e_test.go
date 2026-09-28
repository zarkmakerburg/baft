package integration_test

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/bcc"
	"github.com/zarkmakerburg/baft/internal/config"
	"github.com/zarkmakerburg/baft/internal/node"
	"github.com/zarkmakerburg/baft/internal/recordshape"
	"github.com/zarkmakerburg/baft/internal/securityinternal"
)

func TestRuntimeTelemetryReachesBCCExactlyOnce(t *testing.T){
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
	const agentToken="telemetry-agent-token"
	_,err=store.UpsertNode(bcc.Node{ID:"ir-telemetry",Alias:"IR Telemetry",Address:"127.0.0.1:29999",Role:"worker"},agentToken)
	if err!=nil{t.Fatal(err)}
	const costRate int64 = 1 << 28
	const revenueRate int64 = 1 << 29
	if err:=store.SetFinancePolicy("ir-telemetry",costRate,revenueRate);err!=nil{t.Fatal(err)}
	app,err:=bcc.NewServer(store,"admin");if err!=nil{t.Fatal(err)}
	bccHTTP:=httptest.NewServer(app.Handler());defer bccHTTP.Close()
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
	ex.Node.ID="ex-telemetry"
	ex.Server.Listen=reserveAddress(t)
	ex.Server.ServerName="ex.test"
	ex.Server.AllowedPeerIdentities=[]string{"urn:baft:node:ir-telemetry"}
	ex.Management.UnixSocket=filepath.Join(dir,"ex.sock")
	ex.Management.MetricsListen=reserveAddress(t)
	ex.Transport.Shards=1
	ex.Routes[0].Target=target.Addr().String()
	ex.Routes[0].AllowedPeers=[]string{"urn:baft:node:ir-telemetry"}
	ex.TLS=config.TLS{MinVersion:"1.3",CAFile:ca,CertFile:cert,KeyFile:tlsKey,SessionTickets:false}
	ex.Noise=&config.Noise{KeyFile:exPath,PeerPublicKey:irPub,RecordShaping:recordshape.Config{}}

	ir,err:=config.LoadFile("../../configs/example-ir.yaml");if err!=nil{t.Fatal(err)}
	ir.Node.ID="ir-telemetry"
	ir.Peer.Address=ex.Server.Listen
	ir.Peer.ServerName="ex.test"
	ir.Peer.AllowedIdentity="urn:baft:node:ex-telemetry"
	ir.Management.UnixSocket=filepath.Join(dir,"ir.sock")
	ir.Management.MetricsListen=reserveAddress(t)
	ir.Transport.Shards=1
	ir.Routes[0].Listen=reserveAddress(t)
	ir.TLS=ex.TLS
	ir.Noise=&config.Noise{KeyFile:irPath,PeerPublicKey:exPub,RecordShaping:recordshape.Config{}}
	ir.Telemetry=config.Telemetry{Enabled:true,BCCURL:bccHTTP.URL,AgentTokenEnv:"BAFT_AGENT_TOKEN",IntervalSeconds:1}

	if err:=config.Validate(ex);err!=nil{t.Fatalf("EX config: %v",err)}
	if err:=config.Validate(ir);err!=nil{t.Fatalf("IR config: %v",err)}

	ctx,cancel:=context.WithTimeout(context.Background(),20*time.Second);defer cancel()
	exDone:=make(chan error,1);irDone:=make(chan error,1)
	go func(){exDone<-node.NewRuntime().Run(ctx,ex)}()
	waitTCP(t,ex.Server.Listen,time.Now().Add(8*time.Second))
	go func(){irDone<-node.NewRuntime().Run(ctx,ir)}()
	waitTCP(t,ir.Routes[0].Listen,time.Now().Add(8*time.Second))

	const payloadSize = 1 << 20
	payload:=bytes.Repeat([]byte{0x5a},payloadSize)
	c,err:=net.DialTimeout("tcp",ir.Routes[0].Listen,time.Second);if err!=nil{t.Fatal(err)}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(8*time.Second))
	if _,err=c.Write(payload);err!=nil{t.Fatal(err)}
	got:=make([]byte,len(payload))
	if _,err=io.ReadFull(c,got);err!=nil{t.Fatal(err)}
	if !bytes.Equal(got,payload){t.Fatal("echo mismatch")}

	deadline:=time.Now().Add(5*time.Second)
	for{
		fs:=store.FinanceSnapshot()
		cur,ok:=store.TelemetrySnapshot("ir-telemetry")
		if len(fs)==1&&ok&&fs[0].IngressBytes==payloadSize&&fs[0].EgressBytes==payloadSize&&cur.ActiveSessions>=1&&cur.HandshakeErrors==0{
			total:=uint64(payloadSize*2)
			wantCost:=int64((total*uint64(costRate))/(1<<30))
			wantRevenue:=int64((total*uint64(revenueRate))/(1<<30))
			if fs[0].CostMicros!=wantCost||fs[0].RevenueMicros!=wantRevenue||fs[0].ProfitMicros!=wantRevenue-wantCost{
				t.Fatalf("finance mismatch got=%+v want cost=%d revenue=%d",fs[0],wantCost,wantRevenue)
			}
			t.Logf("PASS runtime->BCC exact telemetry ingress=%d egress=%d active_sessions=%d sequence=%d",fs[0].IngressBytes,fs[0].EgressBytes,cur.ActiveSessions,cur.Sequence)
			break
		}
		if time.Now().After(deadline){
			t.Fatalf("telemetry deadline finance=%+v cursor=%+v ok=%v",fs,cur,ok)
		}
		time.Sleep(100*time.Millisecond)
	}

	cancel()
	for name,ch:=range map[string]<-chan error{"ex":exDone,"ir":irDone}{
		select{
		case e:=<-ch:if e!=nil{t.Fatalf("%s runtime: %v",name,e)}
		case <-time.After(8*time.Second):t.Fatalf("%s shutdown timeout",name)
		}
	}
}
