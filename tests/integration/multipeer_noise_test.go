package integration_test

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/config"
	"github.com/zarkmakerburg/baft/internal/node"
	"github.com/zarkmakerburg/baft/internal/recordshape"
	"github.com/zarkmakerburg/baft/internal/securityinternal"
)

func TestNoiseListenerAcceptsAndSeparatesTwoIRPeers(t *testing.T) {
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
	go func(){
		for{
			c,e:=target.Accept();if e!=nil{return}
			go func(conn net.Conn){defer conn.Close();_,_=io.Copy(conn,conn)}(c)
		}
	}()

	exKey,err:=securityinternal.GenerateKeyPair();if err!=nil{t.Fatal(err)}
	ir1Key,err:=securityinternal.GenerateKeyPair();if err!=nil{t.Fatal(err)}
	ir2Key,err:=securityinternal.GenerateKeyPair();if err!=nil{t.Fatal(err)}
	exPath:=filepath.Join(dir,"ex-noise.json");ir1Path:=filepath.Join(dir,"ir1-noise.json");ir2Path:=filepath.Join(dir,"ir2-noise.json")
	for _,v:=range []struct{p string;k securityinternal.KeyPair}{{exPath,exKey},{ir1Path,ir1Key},{ir2Path,ir2Key}}{
		if err:=securityinternal.SaveKeyPair(v.p,v.k);err!=nil{t.Fatal(err)}
	}
	exPub,_:=securityinternal.EncodePublicKey(exKey.Public)
	ir1Pub,_:=securityinternal.EncodePublicKey(ir1Key.Public)
	ir2Pub,_:=securityinternal.EncodePublicKey(ir2Key.Public)

	ex,err:=config.LoadFile("../../configs/example-ex.yaml");if err!=nil{t.Fatal(err)}
	ex.Node.ID="ex-shared"
	ex.Server.Listen=reserveAddress(t)
	ex.Server.ServerName="ex.test"
	ex.Server.AllowedPeerIdentities=[]string{"urn:baft:node:ir-master","urn:baft:node:ir-worker"}
	ex.Management.UnixSocket=filepath.Join(dir,"ex.sock")
	ex.Management.MetricsListen=reserveAddress(t)
	ex.Transport.Shards=1
	ex.Routes[0].Target=target.Addr().String()
	ex.Routes[0].AllowedPeers=[]string{"urn:baft:node:ir-master","urn:baft:node:ir-worker"}
	ex.TLS=config.TLS{MinVersion:"1.3",CAFile:ca,CertFile:cert,KeyFile:tlsKey,SessionTickets:false}
	ex.Noise=&config.Noise{
		KeyFile:exPath,
		AllowedPeerPublicKeys:map[string]string{
			"urn:baft:node:ir-master":ir1Pub,
			"urn:baft:node:ir-worker":ir2Pub,
		},
		RecordShaping:recordshape.Config{},
	}

	makeIR:=func(id,keyPath,listen,metrics string) config.Config{
		ir,e:=config.LoadFile("../../configs/example-ir.yaml");if e!=nil{t.Fatal(e)}
		ir.Node.ID=id
		ir.Peer.Address=ex.Server.Listen
		ir.Peer.ServerName="ex.test"
		ir.Peer.AllowedIdentity="urn:baft:node:ex-shared"
		ir.Management.UnixSocket=filepath.Join(dir,id+".sock")
		ir.Management.MetricsListen=metrics
		ir.Transport.Shards=1
		ir.Routes[0].Listen=listen
		ir.TLS=ex.TLS
		ir.Noise=&config.Noise{KeyFile:keyPath,PeerPublicKey:exPub,RecordShaping:recordshape.Config{}}
		return ir
	}
	ir1:=makeIR("ir-master",ir1Path,reserveAddress(t),reserveAddress(t))
	ir2:=makeIR("ir-worker",ir2Path,reserveAddress(t),reserveAddress(t))
	if err:=config.Validate(ex);err!=nil{t.Fatalf("EX config: %v",err)}
	if err:=config.Validate(ir1);err!=nil{t.Fatalf("IR1 config: %v",err)}
	if err:=config.Validate(ir2);err!=nil{t.Fatalf("IR2 config: %v",err)}

	ctx,cancel:=context.WithTimeout(context.Background(),25*time.Second)
	defer cancel()
	exDone:=make(chan error,1);go func(){exDone<-node.NewRuntime().Run(ctx,ex)}()
	waitTCP(t,ex.Server.Listen,time.Now().Add(8*time.Second))

	ir1Done:=make(chan error,1);ir2Done:=make(chan error,1)
	go func(){ir1Done<-node.NewRuntime().Run(ctx,ir1)}()
	go func(){ir2Done<-node.NewRuntime().Run(ctx,ir2)}()
	waitTCP(t,ir1.Routes[0].Listen,time.Now().Add(8*time.Second))
	waitTCP(t,ir2.Routes[0].Listen,time.Now().Add(8*time.Second))

	roundtrip:=func(label,addr string){
		c,e:=net.DialTimeout("tcp",addr,time.Second);if e!=nil{t.Fatal(e)}
		defer c.Close();_ = c.SetDeadline(time.Now().Add(5*time.Second))
		payload:=bytes.Repeat([]byte(label+"|"),256)
		if _,e=c.Write(payload);e!=nil{t.Fatal(e)}
		got:=make([]byte,len(payload));if _,e=io.ReadFull(c,got);e!=nil{t.Fatal(e)}
		if !bytes.Equal(got,payload){t.Fatalf("%s roundtrip mismatch",label)}
	}
	roundtrip("master",ir1.Routes[0].Listen)
	roundtrip("worker",ir2.Routes[0].Listen)
	t.Logf("PASS EX accepted two simultaneous authenticated peers: master=%s worker=%s",ir1.Node.ID,ir2.Node.ID)

	cancel()
	for name,ch:=range map[string]<-chan error{"ex":exDone,"ir1":ir1Done,"ir2":ir2Done}{
		select{
		case e:=<-ch:
			if e!=nil{t.Fatalf("%s runtime: %v",name,e)}
		case <-time.After(8*time.Second):
			t.Fatalf("%s shutdown timed out",name)
		}
	}
	fmt.Sprint()
}
