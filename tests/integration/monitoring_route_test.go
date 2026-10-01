package integration_test

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
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

func TestRouteDisconnectTurnsMonitoringDownWithin30Seconds(t *testing.T){
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
	const agentToken="route-monitor-agent"
	_,err=store.UpsertNode(bcc.Node{ID:"ex-monitor",Alias:"EX Monitor",Address:"127.0.0.1:29998",Role:"foreign"},agentToken)
	if err!=nil{t.Fatal(err)}
	app,err:=bcc.NewServer(store,"admin");if err!=nil{t.Fatal(err)}
	bccHTTP:=httptest.NewServer(app.Handler());defer bccHTTP.Close()
	t.Setenv("BAFT_AGENT_TOKEN",agentToken)

	target,err:=net.Listen("tcp","127.0.0.1:0");if err!=nil{t.Fatal(err)}
	targetClosed:=false
	defer func(){if !targetClosed{_ = target.Close()}}()
	go func(){
		for{
			c,e:=target.Accept();if e!=nil{return}
			_ = c.Close()
		}
	}()

	exKey,err:=securityinternal.GenerateKeyPair();if err!=nil{t.Fatal(err)}
	irKey,err:=securityinternal.GenerateKeyPair();if err!=nil{t.Fatal(err)}
	exPath:=filepath.Join(dir,"ex-noise.json")
	if err:=securityinternal.SaveKeyPair(exPath,exKey);err!=nil{t.Fatal(err)}
	irPub,_:=securityinternal.EncodePublicKey(irKey.Public)

	ex,err:=config.LoadFile("../../configs/example-ex.yaml");if err!=nil{t.Fatal(err)}
	ex.Node.ID="ex-monitor"
	ex.Server.Listen=reserveAddress(t)
	ex.Server.ServerName="ex.test"
	ex.Server.AllowedPeerIdentities=[]string{"urn:baft:node:ir-monitor"}
	ex.Management.UnixSocket=filepath.Join(dir,"ex.sock")
	ex.Management.MetricsListen=reserveAddress(t)
	ex.Transport.Shards=1
	ex.Routes[0].Target=target.Addr().String()
	ex.Routes[0].AllowedPeers=[]string{"urn:baft:node:ir-monitor"}
	ex.TLS=config.TLS{MinVersion:"1.3",CAFile:ca,CertFile:cert,KeyFile:tlsKey,SessionTickets:false}
	ex.Noise=&config.Noise{KeyFile:exPath,PeerPublicKey:irPub,RecordShaping:recordshape.Config{}}
	ex.Telemetry=config.Telemetry{
		Enabled:true,BCCURL:bccHTTP.URL,AgentTokenEnv:"BAFT_AGENT_TOKEN",
		IntervalSeconds:60,RouteProbeIntervalSeconds:1,
	}
	if err:=config.Validate(ex);err!=nil{t.Fatal(err)}

	ctx,cancel:=context.WithTimeout(context.Background(),20*time.Second);defer cancel()
	done:=make(chan error,1)
	go func(){done<-node.NewRuntime().Run(ctx,ex)}()
	waitTCP(t,ex.Server.Listen,time.Now().Add(8*time.Second))
	if err:=store.SetHealth("ex-monitor","up",1,time.Now());err!=nil{t.Fatal(err)}

	readRouteStatus:=func() string {
		req:=httptest.NewRequest(http.MethodGet,"/api/monitoring",nil)
		req.Header.Set("Authorization","Bearer admin")
		rr:=httptest.NewRecorder()
		app.Handler().ServeHTTP(rr,req)
		if rr.Code!=http.StatusOK{return ""}
		var view []bcc.MonitoringNode
		if err:=json.Unmarshal(rr.Body.Bytes(),&view);err!=nil{return ""}
		if len(view)!=1||len(view[0].Routes)==0{return ""}
		return view[0].Routes[0].Status
	}

	upDeadline:=time.Now().Add(8*time.Second)
	for readRouteStatus()!="up"{
		if time.Now().After(upDeadline){t.Fatalf("route never became up; cursor=%+v",store.History("ex-monitor",time.Now()))}
		time.Sleep(100*time.Millisecond)
	}

	start:=time.Now()
	if err:=target.Close();err!=nil{t.Fatal(err)}
	targetClosed=true
	downDeadline:=start.Add(30*time.Second)
	for readRouteStatus()!="down"{
		if time.Now().After(downDeadline){t.Fatal("route did not become DOWN within 30 seconds")}
		time.Sleep(100*time.Millisecond)
	}
	elapsed:=time.Since(start)
	if elapsed>=30*time.Second{t.Fatalf("route transition took %s",elapsed)}
	t.Logf("PASS route transitioned UP->DOWN through BCC monitoring in %s",elapsed)

	cancel()
	select{
	case e:=<-done:if e!=nil{t.Fatal(e)}
	case <-time.After(8*time.Second):t.Fatal("runtime shutdown timeout")
	}
}
