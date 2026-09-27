package integration_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	carrierh2 "github.com/zarkmakerburg/baft/internal/carrier/h2"
	"github.com/zarkmakerburg/baft/internal/identity"
	"github.com/zarkmakerburg/baft/internal/routes"
	"github.com/zarkmakerburg/baft/internal/session"
)

type pki struct { roots *x509.CertPool; server tls.Certificate; client tls.Certificate }

func testPKI(t *testing.T) pki {
	t.Helper()
	caKey,err:=ecdsa.GenerateKey(elliptic.P256(),rand.Reader);if err!=nil{t.Fatal(err)}
	now:=time.Now().Add(-time.Minute)
	caT:=&x509.Certificate{SerialNumber:big.NewInt(1),Subject:pkix.Name{CommonName:"BAFT integration CA"},NotBefore:now,NotAfter:now.Add(time.Hour),IsCA:true,BasicConstraintsValid:true,KeyUsage:x509.KeyUsageCertSign|x509.KeyUsageDigitalSignature}
	caDER,err:=x509.CreateCertificate(rand.Reader,caT,caT,&caKey.PublicKey,caKey);if err!=nil{t.Fatal(err)}
	caCert,err:=x509.ParseCertificate(caDER);if err!=nil{t.Fatal(err)}
	roots:=x509.NewCertPool();roots.AddCert(caCert)
	issue:=func(serial int64,dns,uri string,eku x509.ExtKeyUsage)tls.Certificate{key,err:=ecdsa.GenerateKey(elliptic.P256(),rand.Reader);if err!=nil{t.Fatal(err)};ct:=&x509.Certificate{SerialNumber:big.NewInt(serial),Subject:pkix.Name{CommonName:"BAFT integration"},NotBefore:now,NotAfter:now.Add(time.Hour),KeyUsage:x509.KeyUsageDigitalSignature,ExtKeyUsage:[]x509.ExtKeyUsage{eku}};if dns!=""{ct.DNSNames=[]string{dns}};if uri!=""{u,err:=url.Parse(uri);if err!=nil{t.Fatal(err)};ct.URIs=[]*url.URL{u}};der,err:=x509.CreateCertificate(rand.Reader,ct,caCert,&key.PublicKey,caKey);if err!=nil{t.Fatal(err)};keyDER,err:=x509.MarshalPKCS8PrivateKey(key);if err!=nil{t.Fatal(err)};pair,err:=tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type:"CERTIFICATE",Bytes:der}),pem.EncodeToMemory(&pem.Block{Type:"PRIVATE KEY",Bytes:keyDER}));if err!=nil{t.Fatal(err)};return pair}
	return pki{roots:roots,server:issue(2,"ex.test","",x509.ExtKeyUsageServerAuth),client:issue(3,"","urn:baft:node:ir-01",x509.ExtKeyUsageClientAuth)}
}

func TestVerticalH2MTLSRouteHalfCloseHash(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),10*time.Second);defer cancel()
	targetLn,err:=net.Listen("tcp","127.0.0.1:0");if err!=nil{t.Fatal(err)};defer targetLn.Close()
	targetDone:=make(chan error,1);go func(){c,err:=targetLn.Accept();if err!=nil{targetDone<-err;return};defer c.Close();b,err:=io.ReadAll(c);if err!=nil{targetDone<-err;return};if _,err:=c.Write(b);err!=nil{targetDone<-err;return};if cw,ok:=c.(interface{CloseWrite()error});ok{_ = cw.CloseWrite()};targetDone<-nil}()
	routeTable,err:=routes.New([]routes.Route{{ID:"service-main",Target:targetLn.Addr().String(),AllowedPeers:map[string]struct{}{"urn:baft:node:ir-01":{}}}});if err!=nil{t.Fatal(err)}
	certs:=testPKI(t);serverTLS,err:=identity.ServerTLS(certs.roots,certs.server,map[string]struct{}{"urn:baft:node:ir-01":{}});if err!=nil{t.Fatal(err)}
	serverErr:=make(chan error,1)
	hs:=httptest.NewUnstartedServer(carrierh2.Handler(func(hctx context.Context,in io.Reader,out io.Writer,peer carrierh2.PeerInfo)error{exPeer,err:=session.New(session.Listener,session.Carrier{In:in,Out:out},peer.Identity,routeTable,session.Options{NodeID:"ex-01",ExpectedPeerNodeID:"ir-01",ShardID:0,ProfileID:"secure-fast",ProfileVersion:1,ConfigRevision:"integration"});if err!=nil{return err};err=exPeer.Run(hctx);select{case serverErr<-err:default:};return err}))
	hs.EnableHTTP2=true;hs.TLS=serverTLS;hs.StartTLS();defer hs.Close()
	clientTLS,err:=identity.ClientTLS(certs.roots,certs.client,"ex.test");if err!=nil{t.Fatal(err)};h2c,err:=carrierh2.NewClient(hs.URL,clientTLS);if err!=nil{t.Fatal(err)};defer h2c.CloseIdleConnections()
	requestReader,requestWriter:=io.Pipe();resp,err:=h2c.Open(ctx,requestReader);if err!=nil{t.Fatal(err)};defer resp.Body.Close()
	irPeer,err:=session.New(session.Dialer,session.Carrier{In:resp.Body,Out:requestWriter},"urn:baft:node:ex-01",nil,session.Options{NodeID:"ir-01",ExpectedPeerNodeID:"ex-01",ShardID:0,ProfileID:"secure-fast",ProfileVersion:1,ConfigRevision:"integration"});if err!=nil{t.Fatal(err)}
	irRun:=make(chan error,1);go func(){irRun<-irPeer.Run(ctx)}()
	irLn,err:=net.Listen("tcp","127.0.0.1:0");if err!=nil{t.Fatal(err)};defer irLn.Close();openErr:=make(chan error,1);go func(){c,err:=irLn.Accept();if err!=nil{openErr<-err;return};openErr<-irPeer.OpenFlow(ctx,"service-main",c)}()
	userConnRaw,err:=net.Dial("tcp",irLn.Addr().String());if err!=nil{t.Fatal(err)};userConn:=userConnRaw.(*net.TCPConn);defer userConn.Close()
	payload:=make([]byte,256*1024+123);for i:=range payload{payload[i]=byte((i*31+7)%251)};wantHash:=sha256.Sum256(payload);if _,err:=userConn.Write(payload);err!=nil{t.Fatal(err)};if err:=userConn.CloseWrite();err!=nil{t.Fatal(err)};got,err:=io.ReadAll(userConn);if err!=nil{t.Fatal(err)};gotHash:=sha256.Sum256(got);if !bytes.Equal(gotHash[:],wantHash[:]){t.Fatalf("end-to-end hash mismatch: got=%x want=%x bytes=%d",gotHash,wantHash,len(got))};if len(got)!=len(payload){t.Fatalf("length mismatch got=%d want=%d",len(got),len(payload))}
	select{case err:=<-openErr:if err!=nil{t.Fatal(err)};case <-time.After(time.Second):t.Fatal("IR flow open did not complete")};select{case err:=<-targetDone:if err!=nil{t.Fatal(err)};case <-time.After(time.Second):t.Fatal("target did not complete after half-close")}
	_ = requestWriter.Close();_ = resp.Body.Close();cancel();select{case <-irRun:case <-time.After(time.Second):};select{case <-serverErr:case <-time.After(time.Second):}
}
