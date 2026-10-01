package integration_test

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/config"
	"github.com/zarkmakerburg/baft/internal/mesh"
	"github.com/zarkmakerburg/baft/internal/node"
	"github.com/zarkmakerburg/baft/internal/securityinternal"
)

func TestIRToIRNoiseMeshRoundTrip(t *testing.T){
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

	target,err:=net.Listen("tcp","127.0.0.1:0");if err!=nil{t.Fatal(err)}
	defer target.Close()
	go func(){for{c,e:=target.Accept();if e!=nil{return};go func(x net.Conn){defer x.Close();_,_=io.Copy(x,x)}(c)}}()

	ir1Key,err:=securityinternal.GenerateKeyPair();if err!=nil{t.Fatal(err)}
	ir2Key,err:=securityinternal.GenerateKeyPair();if err!=nil{t.Fatal(err)}
	ir1Path:=filepath.Join(dir,"ir1.json");ir2Path:=filepath.Join(dir,"ir2.json")
	if err:=securityinternal.SaveKeyPair(ir1Path,ir1Key);err!=nil{t.Fatal(err)}
	if err:=securityinternal.SaveKeyPair(ir2Path,ir2Key);err!=nil{t.Fatal(err)}
	ir1Pub,_:=securityinternal.EncodePublicKey(ir1Key.Public)
	ir2Pub,_:=securityinternal.EncodePublicKey(ir2Key.Public)

	listenerAddr:=reserveAddress(t)
	localMesh:=reserveAddress(t)
	tlsCfg:=config.TLS{MinVersion:"1.3",CAFile:ca,CertFile:cert,KeyFile:tlsKey,SessionTickets:false}
	dialer,listener,err:=mesh.BuildLink(mesh.LinkSpec{
		DialerNodeID:"ir-master",
		ListenerNodeID:"ir-worker",
		ListenerAddress:listenerAddr,
		ServerName:"ex.test",
		DialerLocalListen:localMesh,
		ListenerTarget:target.Addr().String(),
		DialerNoiseKeyFile:ir1Path,
		ListenerNoiseKeyFile:ir2Path,
		DialerPublicKey:ir1Pub,
		ListenerPublicKey:ir2Pub,
		DialerMetrics:reserveAddress(t),
		ListenerMetrics:reserveAddress(t),
		DialerSocket:filepath.Join(dir,"ir1.sock"),
		ListenerSocket:filepath.Join(dir,"ir2.sock"),
		TLS:tlsCfg,
	})
	if err!=nil{t.Fatal(err)}

	ctx,cancel:=context.WithTimeout(context.Background(),20*time.Second);defer cancel()
	ldone:=make(chan error,1);ddone:=make(chan error,1)
	go func(){ldone<-node.NewRuntime().Run(ctx,listener)}()
	waitTCP(t,listener.Server.Listen,time.Now().Add(8*time.Second))
	go func(){ddone<-node.NewRuntime().Run(ctx,dialer)}()
	waitTCP(t,dialer.Routes[0].Listen,time.Now().Add(8*time.Second))

	c,err:=net.DialTimeout("tcp",dialer.Routes[0].Listen,time.Second);if err!=nil{t.Fatal(err)}
	_ = c.SetDeadline(time.Now().Add(5*time.Second))
	payload:=bytes.Repeat([]byte("ir-mesh-noise|"),128)
	if _,err=c.Write(payload);err!=nil{t.Fatal(err)}
	got:=make([]byte,len(payload));if _,err=io.ReadFull(c,got);err!=nil{t.Fatal(err)}
	_ = c.Close()
	if !bytes.Equal(got,payload){t.Fatal("mesh roundtrip mismatch")}
	t.Logf("PASS secure IR-to-IR BAFT/Noise mesh roundtrip bytes=%d",len(payload))

	cancel()
	for name,ch:=range map[string]<-chan error{"listener":ldone,"dialer":ddone}{
		select{
		case e:=<-ch:if e!=nil{t.Fatalf("%s: %v",name,e)}
		case <-time.After(8*time.Second):t.Fatalf("%s shutdown timeout",name)
		}
	}
}
