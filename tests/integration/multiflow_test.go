package integration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"net"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	carrierh2 "github.com/zarkmakerburg/baft/internal/carrier/h2"
	"github.com/zarkmakerburg/baft/internal/identity"
	"github.com/zarkmakerburg/baft/internal/routes"
	"github.com/zarkmakerburg/baft/internal/session"
)

func TestConcurrentMultiFlowTransfer(t *testing.T) {
	const flowCount = 8
	ctx,cancel:=context.WithTimeout(context.Background(),20*time.Second)
	defer cancel()

	targetLn,err:=net.Listen("tcp","127.0.0.1:0")
	if err!=nil { t.Fatal(err) }
	defer targetLn.Close()

	var targetWG sync.WaitGroup
	targetAcceptErr:=make(chan error,1)
	go func(){
		for i:=0;i<flowCount;i++ {
			c,err:=targetLn.Accept()
			if err!=nil { targetAcceptErr<-err; return }
			targetWG.Add(1)
			go func(c net.Conn){
				defer targetWG.Done();defer c.Close()
				buf:=make([]byte,64*1024)
				for {
					n,rerr:=c.Read(buf)
					if n>0 {
						p:=buf[:n]
						for len(p)>0 {
							w,err:=c.Write(p)
							if err!=nil { return }
							p=p[w:]
						}
					}
					if rerr!=nil {
						if cw,ok:=c.(interface{CloseWrite() error});ok { _=cw.CloseWrite() }
						return
					}
				}
			}(c)
		}
		targetAcceptErr<-nil
	}()

	table,err:=routes.New([]routes.Route{{
		ID:"service-main",Target:targetLn.Addr().String(),
		AllowedPeers:map[string]struct{}{"urn:baft:node:ir-01":{}},
	}})
	if err!=nil { t.Fatal(err) }

	certs:=testPKI(t)
	serverTLS,err:=identity.ServerTLS(certs.roots,certs.server,map[string]struct{}{"urn:baft:node:ir-01":{}})
	if err!=nil { t.Fatal(err) }
	serverErr:=make(chan error,1)
	hs:=httptest.NewUnstartedServer(carrierh2.Handler(func(hctx context.Context,in io.Reader,out io.Writer,peer carrierh2.PeerInfo) error {
		ex,err:=session.New(session.Listener,session.Carrier{In:in,Out:out},peer.Identity,table,session.Options{
			NodeID:"ex-01",ExpectedPeerNodeID:"ir-01",ShardID:0,
			ProfileID:"secure-fast",ProfileVersion:1,ConfigRevision:"multiflow",
		})
		if err!=nil { return err }
		err=ex.Run(hctx)
		select{case serverErr<-err:default:}
		return err
	}))
	hs.EnableHTTP2=true;hs.TLS=serverTLS;hs.StartTLS();defer hs.Close()

	clientTLS,err:=identity.ClientTLS(certs.roots,certs.client,"ex.test")
	if err!=nil { t.Fatal(err) }
	h2c,err:=carrierh2.NewClient(hs.URL,clientTLS)
	if err!=nil { t.Fatal(err) }
	defer h2c.CloseIdleConnections()

	reqR,reqW:=io.Pipe()
	resp,err:=h2c.Open(ctx,reqR)
	if err!=nil { t.Fatal(err) }
	defer resp.Body.Close()

	ir,err:=session.New(session.Dialer,session.Carrier{In:resp.Body,Out:reqW},"urn:baft:node:ex-01",nil,session.Options{
		NodeID:"ir-01",ExpectedPeerNodeID:"ex-01",ShardID:0,
		ProfileID:"secure-fast",ProfileVersion:1,ConfigRevision:"multiflow",
	})
	if err!=nil { t.Fatal(err) }
	irDone:=make(chan error,1)
	go func(){irDone<-ir.Run(ctx)}()

	localLn,err:=net.Listen("tcp","127.0.0.1:0")
	if err!=nil { t.Fatal(err) }
	defer localLn.Close()
	openErr:=make(chan error,flowCount)
	go func(){
		for i:=0;i<flowCount;i++ {
			c,err:=localLn.Accept()
			if err!=nil { openErr<-err; continue }
			go func(c net.Conn){openErr<-ir.OpenFlow(ctx,"service-main",c)}(c)
		}
	}()

	var clients sync.WaitGroup
	clientErr:=make(chan error,flowCount)
	for i:=0;i<flowCount;i++ {
		i:=i
		clients.Add(1)
		go func(){
			defer clients.Done()
			raw,err:=net.Dial("tcp",localLn.Addr().String())
			if err!=nil { clientErr<-err; return }
			c:=raw.(*net.TCPConn);defer c.Close()

			size:=512*1024+i*4093
			payload:=make([]byte,size)
			for j:=range payload { payload[j]=byte((j*17+i*29)%251) }
			want:=sha256.Sum256(payload)

			writeDone:=make(chan error,1)
			go func(){
				_,err:=io.Copy(c,bytes.NewReader(payload))
				if err==nil { err=c.CloseWrite() }
				writeDone<-err
			}()
			h:=sha256.New()
			n,err:=io.Copy(h,c)
			if err!=nil { clientErr<-err; return }
			if err:=<-writeDone;err!=nil { clientErr<-err; return }
			if n!=int64(len(payload)) { clientErr<-io.ErrUnexpectedEOF; return }
			var got [32]byte;copy(got[:],h.Sum(nil))
			if got!=want { clientErr<-io.ErrUnexpectedEOF; return }
			clientErr<-nil
		}()
	}
	clients.Wait()
	for i:=0;i<flowCount;i++ {
		if err:=<-clientErr;err!=nil { t.Fatalf("client %d: %v",i,err) }
	}
	for i:=0;i<flowCount;i++ {
		if err:=<-openErr;err!=nil { t.Fatalf("open %d: %v",i,err) }
	}
	if err:=<-targetAcceptErr;err!=nil { t.Fatal(err) }
	targetWG.Wait()

	_ = reqW.Close()
	_ = resp.Body.Close()
	cancel()
	select{case <-irDone:case <-time.After(time.Second):}
	select{case <-serverErr:case <-time.After(time.Second):}
}
