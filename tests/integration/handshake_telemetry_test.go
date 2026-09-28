package integration_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/bcc"
	carrierh2 "github.com/zarkmakerburg/baft/internal/carrier/h2"
	"github.com/zarkmakerburg/baft/internal/config"
	"github.com/zarkmakerburg/baft/internal/node"
	"github.com/zarkmakerburg/baft/internal/recordshape"
	"github.com/zarkmakerburg/baft/internal/securityinternal"
)

func TestBadNoiseHandshakeIncrementsTelemetryAndReachesBCC(t *testing.T){
	certs:=testPKI(t)
	dir:=t.TempDir()
	write:=func(name string,b []byte) string{
		p:=filepath.Join(dir,name)
		if err:=os.WriteFile(p,b,0600);err!=nil{t.Fatal(err)}
		return p
	}
	caPEM:=pem.EncodeToMemory(&pem.Block{Type:"CERTIFICATE",Bytes:certs.caDER})
	ca:=write("ca.pem",caPEM)
	cert:=write("server.pem",pem.EncodeToMemory(&pem.Block{Type:"CERTIFICATE",Bytes:certs.server.Certificate[0]}))
	der,err:=x509.MarshalPKCS8PrivateKey(certs.server.PrivateKey);if err!=nil{t.Fatal(err)}
	tlsKey:=write("server.key",pem.EncodeToMemory(&pem.Block{Type:"PRIVATE KEY",Bytes:der}))

	store,err:=bcc.OpenStore(filepath.Join(dir,"bcc-state.json"));if err!=nil{t.Fatal(err)}
	const token="bad-handshake-agent"
	_,err=store.UpsertNode(bcc.Node{ID:"ex-bad-handshake",Alias:"EX Bad Handshake",Address:"127.0.0.1:29990",Role:"foreign"},token)
	if err!=nil{t.Fatal(err)}
	app,err:=bcc.NewServer(store,"admin");if err!=nil{t.Fatal(err)}
	bccHTTP:=httptest.NewServer(app.Handler());defer bccHTTP.Close()
	t.Setenv("BAFT_AGENT_TOKEN",token)

	target,err:=net.Listen("tcp","127.0.0.1:0");if err!=nil{t.Fatal(err)}
	defer target.Close()
	go func(){for{c,e:=target.Accept();if e!=nil{return};_ = c.Close()}}()

	exKey,err:=securityinternal.GenerateKeyPair();if err!=nil{t.Fatal(err)}
	goodIR,err:=securityinternal.GenerateKeyPair();if err!=nil{t.Fatal(err)}
	badIR,err:=securityinternal.GenerateKeyPair();if err!=nil{t.Fatal(err)}
	exPath:=filepath.Join(dir,"ex-noise.json")
	if err:=securityinternal.SaveKeyPair(exPath,exKey);err!=nil{t.Fatal(err)}
	goodPub,_:=securityinternal.EncodePublicKey(goodIR.Public)

	ex,err:=config.LoadFile("../../configs/example-ex.yaml");if err!=nil{t.Fatal(err)}
	ex.Node.ID="ex-bad-handshake"
	ex.Server.Listen=reserveAddress(t)
	ex.Server.ServerName="ex.test"
	ex.Server.AllowedPeerIdentities=[]string{"urn:baft:node:ir-good"}
	ex.Management.UnixSocket=filepath.Join(dir,"ex.sock")
	ex.Management.MetricsListen=reserveAddress(t)
	ex.Transport.Shards=1
	ex.Routes[0].Target=target.Addr().String()
	ex.Routes[0].AllowedPeers=[]string{"urn:baft:node:ir-good"}
	ex.TLS=config.TLS{MinVersion:"1.3",CAFile:ca,CertFile:cert,KeyFile:tlsKey,SessionTickets:false}
	ex.Noise=&config.Noise{KeyFile:exPath,PeerPublicKey:goodPub,RecordShaping:recordshape.Config{}}
	ex.Telemetry=config.Telemetry{Enabled:true,BCCURL:bccHTTP.URL,AgentTokenEnv:"BAFT_AGENT_TOKEN",IntervalSeconds:1,RouteProbeIntervalSeconds:1}
	if err:=config.Validate(ex);err!=nil{t.Fatal(err)}

	ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second);defer cancel()
	done:=make(chan error,1)
	go func(){done<-node.NewRuntime().Run(ctx,ex)}()
	waitTCP(t,ex.Server.Listen,time.Now().Add(8*time.Second))

	pool:=x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM){t.Fatal("append CA")}
	client,err:=carrierh2.NewClient("https://"+ex.Server.Listen,&tls.Config{
		MinVersion:tls.VersionTLS13,RootCAs:pool,ServerName:"ex.test",NextProtos:[]string{"h2"},
	})
	if err!=nil{t.Fatal(err)}
	defer client.CloseIdleConnections()
	badCtx,badCancel:=context.WithTimeout(context.Background(),3*time.Second)
	_,_,_,badErr:=client.OpenNoise(badCtx,securityinternal.HandshakeConfig{
		Static:badIR,PeerStatic:exKey.Public,RecordShaping:recordshape.Config{},
	})
	badCancel()
	if badErr==nil{t.Fatal("invalid Noise static key unexpectedly accepted")}

	deadline:=time.Now().Add(5*time.Second)
	for{
		cur,ok:=store.TelemetrySnapshot("ex-bad-handshake")
		if ok&&cur.HandshakeErrors>=1{
			t.Logf("PASS bad Noise handshake reached BCC counter=%d sequence=%d",cur.HandshakeErrors,cur.Sequence)
			break
		}
		if time.Now().After(deadline){t.Fatalf("handshake error telemetry not observed cursor=%+v ok=%v",cur,ok)}
		time.Sleep(100*time.Millisecond)
	}

	cancel()
	select{
	case e:=<-done:if e!=nil{t.Fatal(e)}
	case <-time.After(6*time.Second):t.Fatal("runtime shutdown timeout")
	}
}
