package correctness_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	carrierh2 "github.com/zarkmakerburg/baft/internal/carrier/h2"
	"github.com/zarkmakerburg/baft/internal/identity"
	"github.com/zarkmakerburg/baft/internal/routes"
	"github.com/zarkmakerburg/baft/internal/session"
)

const oneGiB int64 = 1 << 30

type patternReader struct {
	remaining int64
	offset    uint64
}

type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (r countingReader) Read(p []byte) (int,error) {
	n,err:=r.r.Read(p)
	if n>0 { r.n.Add(int64(n)) }
	return n,err
}

type countingWriter struct {
	w io.Writer
	n *atomic.Int64
}

func (w countingWriter) Write(p []byte) (int,error) {
	n,err:=w.w.Write(p)
	if n>0 { w.n.Add(int64(n)) }
	return n,err
}

// cor01SocketState captures kernel TCP state for the target connection at a
// stall: both endpoints, sampled twice so window probes/updates are visible.
func cor01SocketState(port int) string {
	var b strings.Builder
	filter:=fmt.Sprintf("( sport = :%d or dport = :%d )",port,port)
	for i:=0;i<2;i++{
		if i==1{time.Sleep(3*time.Second)}
		out,err:=exec.Command("ss","-tinmoeH","state","all",filter).CombinedOutput()
		fmt.Fprintf(&b,"--- ss sample %d (err=%v)\n%s\n",i,err,out)
	}
	if out,err:=exec.Command("nstat","-az","TcpExtTCPRcvQDrop","TcpExtTCPBacklogDrop","TcpExtPruneCalled","TcpExtRcvPruned","TcpExtTCPZeroWindowDrop","TcpExtTCPOFODrop","TcpExtTCPRcvCollapsed","TcpExtTCPTimeouts","TcpExtTCPLossProbes","TcpRetransSegs").CombinedOutput();true{
		fmt.Fprintf(&b,"--- nstat (err=%v)\n%s\n",err,out)
	}
	for _,f:=range []string{"/proc/net/sockstat","/proc/sys/net/ipv4/tcp_mem","/proc/sys/net/ipv4/tcp_rmem","/proc/sys/net/ipv4/tcp_wmem"}{
		out,err:=os.ReadFile(f)
		fmt.Fprintf(&b,"--- %s (err=%v)\n%s\n",f,err,out)
	}
	return b.String()
}

type cor01StallEvidence struct {
	SentBytes int64
	RecvBytes int64
	TargetReadBytes int64
	TargetWrittenBytes int64
	IR session.SessionDebugSnapshotForTest
	EX session.SessionDebugSnapshotForTest
	IRWindowSent uint64
	IRWindowRecv uint64
	EXWindowSent uint64
	EXWindowRecv uint64
	Goroutines string
	Sockets string
}

func (r *patternReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := len(p)
	if int64(n) > r.remaining {
		n = int(r.remaining)
	}
	for i := 0; i < n; i++ {
		p[i] = byte(((r.offset + uint64(i))*31 + 7) % 251)
	}
	r.offset += uint64(n)
	r.remaining -= int64(n)
	return n, nil
}

type pki struct {
	roots  *x509.CertPool
	server tls.Certificate
	client tls.Certificate
}

func makePKI(t *testing.T) pki {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil { t.Fatal(err) }
	now := time.Now().Add(-time.Minute)
	caT := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{CommonName:"BAFT COR-01 CA"},
		NotBefore: now, NotAfter: now.Add(time.Hour),
		IsCA:true, BasicConstraintsValid:true,
		KeyUsage:x509.KeyUsageCertSign|x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader,caT,caT,&caKey.PublicKey,caKey)
	if err != nil { t.Fatal(err) }
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil { t.Fatal(err) }
	roots := x509.NewCertPool(); roots.AddCert(caCert)

	issue := func(serial int64, dns, uri string, eku x509.ExtKeyUsage) tls.Certificate {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil { t.Fatal(err) }
		ct := &x509.Certificate{
			SerialNumber:big.NewInt(serial), Subject:pkix.Name{CommonName:"BAFT COR-01"},
			NotBefore:now, NotAfter:now.Add(time.Hour),
			KeyUsage:x509.KeyUsageDigitalSignature, ExtKeyUsage:[]x509.ExtKeyUsage{eku},
		}
		if dns != "" { ct.DNSNames=[]string{dns} }
		if uri != "" {
			u, err := url.Parse(uri); if err != nil { t.Fatal(err) }
			ct.URIs=[]*url.URL{u}
		}
		der, err := x509.CreateCertificate(rand.Reader,ct,caCert,&key.PublicKey,caKey)
		if err != nil { t.Fatal(err) }
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil { t.Fatal(err) }
		pair, err := tls.X509KeyPair(
			pem.EncodeToMemory(&pem.Block{Type:"CERTIFICATE",Bytes:der}),
			pem.EncodeToMemory(&pem.Block{Type:"PRIVATE KEY",Bytes:keyDER}),
		)
		if err != nil { t.Fatal(err) }
		return pair
	}
	return pki{
		roots:roots,
		server:issue(2,"ex.test","",x509.ExtKeyUsageServerAuth),
		client:issue(3,"","urn:baft:node:ir-01",x509.ExtKeyUsageClientAuth),
	}
}

func writeFull(w io.Writer, p []byte) error {
	for len(p)>0 {
		n,err:=w.Write(p)
		if n>0 { p=p[n:] }
		if err!=nil { return err }
		if n==0 { return io.ErrShortWrite }
	}
	return nil
}

func TestCOR01OneGiBBidirectional(t *testing.T) {
	if os.Getenv("BAFT_COR01_1GIB") != "1" {
		t.Skip("set BAFT_COR01_1GIB=1 to run the 1 GiB correctness gate")
	}
	ctx,cancel:=context.WithTimeout(context.Background(),18*time.Minute)
	defer cancel()
	var sentBytes,recvBytes atomic.Int64
	var targetReadBytes,targetWrittenBytes atomic.Int64
	var irWindowSent,irWindowRecv,exWindowSent,exWindowRecv atomic.Uint64
	observeWindows:=func(sent,recv *atomic.Uint64) func(session.WindowUpdateDiagnosticForTest) {
		return func(ev session.WindowUpdateDiagnosticForTest) {
			switch ev.Direction {
			case "sent": sent.Add(1)
			case "received": recv.Add(1)
			}
		}
	}
	var exMu sync.Mutex
	var exPeer *session.Peer
	targetBufBytes:=128*1024
	if raw:=os.Getenv("BAFT_COR01_TARGET_BUF_KIB");raw!=""{
		kib,err:=strconv.Atoi(raw)
		if err!=nil||kib<=0||kib>1024{t.Fatalf("invalid BAFT_COR01_TARGET_BUF_KIB=%q",raw)}
		targetBufBytes=kib*1024
	}
	t.Logf("COR-T1 target_echo_buffer_bytes=%d",targetBufBytes)

	targetLn,err:=net.Listen("tcp","127.0.0.1:0")
	if err!=nil { t.Fatal(err) }
	defer targetLn.Close()
	targetDone:=make(chan error,1)
	go func(){
		c,err:=targetLn.Accept()
		if err!=nil { targetDone<-err; return }
		defer c.Close()
		buf:=make([]byte,targetBufBytes)
		for {
			n,rerr:=c.Read(buf)
			if n>0 {
				targetReadBytes.Add(int64(n))
				if werr:=writeFull(countingWriter{w:c,n:&targetWrittenBytes},buf[:n]); werr!=nil { targetDone<-werr; return }
			}
			if rerr!=nil {
				if rerr==io.EOF {
					if cw,ok:=c.(interface{CloseWrite() error});ok { _=cw.CloseWrite() }
					targetDone<-nil
				} else { targetDone<-rerr }
				return
			}
		}
	}()

	table,err:=routes.New([]routes.Route{{
		ID:"service-main",Target:targetLn.Addr().String(),
		AllowedPeers:map[string]struct{}{"urn:baft:node:ir-01":{}},
	}})
	if err!=nil { t.Fatal(err) }

	certs:=makePKI(t)
	serverTLS,err:=identity.ServerTLS(certs.roots,certs.server,map[string]struct{}{"urn:baft:node:ir-01":{}})
	if err!=nil { t.Fatal(err) }
	serverErr:=make(chan error,1)
	hs:=httptest.NewUnstartedServer(carrierh2.Handler(func(hctx context.Context,in io.Reader,out io.Writer,peer carrierh2.PeerInfo) error {
		ex,err:=session.New(session.Listener,session.Carrier{In:in,Out:out},peer.Identity,table,session.Options{
			NodeID:"ex-01",ExpectedPeerNodeID:"ir-01",ShardID:0,
			ProfileID:"secure-fast",ProfileVersion:1,ConfigRevision:"cor01",
		})
		if err!=nil { return err }
		ex.SetWindowUpdateObserverForTest(observeWindows(&exWindowSent,&exWindowRecv))
		exMu.Lock();exPeer=ex;exMu.Unlock()
		err=ex.Run(hctx)
		select { case serverErr<-err: default: }
		return err
	}))
	hs.EnableHTTP2=true; hs.TLS=serverTLS; hs.StartTLS(); defer hs.Close()

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
		ProfileID:"secure-fast",ProfileVersion:1,ConfigRevision:"cor01",
	})
	if err!=nil { t.Fatal(err) }
	ir.SetWindowUpdateObserverForTest(observeWindows(&irWindowSent,&irWindowRecv))
	irDone:=make(chan error,1)
	go func(){ irDone<-ir.Run(ctx) }()

	localLn,err:=net.Listen("tcp","127.0.0.1:0")
	if err!=nil { t.Fatal(err) }
	defer localLn.Close()
	openDone:=make(chan error,1)
	go func(){
		c,err:=localLn.Accept()
		if err!=nil { openDone<-err; return }
		openDone<-ir.OpenFlow(ctx,"service-main",c)
	}()

	raw,err:=net.Dial("tcp",localLn.Addr().String())
	if err!=nil { t.Fatal(err) }
	user:=raw.(*net.TCPConn)
	defer user.Close()

	type sendResult struct{
		n int64
		sum [32]byte
		err error
	}
	sent:=make(chan sendResult,1)
	go func(){
		h:=sha256.New()
		src:=io.TeeReader(&patternReader{remaining:oneGiB},h)
		n,err:=io.CopyBuffer(countingWriter{w:user,n:&sentBytes},src,make([]byte,256*1024))
		if err==nil { err=user.CloseWrite() }
		var sum [32]byte
		copy(sum[:],h.Sum(nil))
		sent<-sendResult{n:n,sum:sum,err:err}
	}()

	stallCh:=make(chan cor01StallEvidence,1)
	watchDone:=make(chan struct{})
	go func(){
		ticker:=time.NewTicker(2*time.Second)
		defer ticker.Stop()
		lastSent,lastRecv:=sentBytes.Load(),recvBytes.Load()
		lastProgress:=time.Now()
		for{
			select{
			case <-watchDone:
				return
			case <-ticker.C:
				s,r:=sentBytes.Load(),recvBytes.Load()
				if s!=lastSent||r!=lastRecv{
					lastSent,lastRecv=s,r
					lastProgress=time.Now()
					continue
				}
				if time.Since(lastProgress)<15*time.Second{continue}
				exMu.Lock();xp:=exPeer;exMu.Unlock()
				exSnap:=session.SessionDebugSnapshotForTest{}
				if xp!=nil{exSnap=xp.DebugSnapshotForTest()}
				buf:=make([]byte,4<<20)
				n:=runtime.Stack(buf,true)
				ev:=cor01StallEvidence{
					SentBytes:s,RecvBytes:r,
					TargetReadBytes:targetReadBytes.Load(),TargetWrittenBytes:targetWrittenBytes.Load(),
					IR:ir.DebugSnapshotForTest(),EX:exSnap,
					IRWindowSent:irWindowSent.Load(),IRWindowRecv:irWindowRecv.Load(),
					EXWindowSent:exWindowSent.Load(),EXWindowRecv:exWindowRecv.Load(),
					Goroutines:string(buf[:n]),
					Sockets:cor01SocketState(targetLn.Addr().(*net.TCPAddr).Port),
				}
				select{case stallCh<-ev:default:}
				_ = user.Close()
				cancel()
				return
			}
		}
	}()

	recvHash:=sha256.New()
	recvN,recvErr:=io.CopyBuffer(recvHash,countingReader{r:user,n:&recvBytes},make([]byte,256*1024))
	sendRes:=<-sent
	close(watchDone)
	select{
	case ev:=<-stallCh:
		t.Logf("COR-T1 STALL sent=%d recv=%d target_read=%d target_written=%d target_inflight=%d ir_windows_sent=%d ir_windows_recv=%d ex_windows_sent=%d ex_windows_recv=%d IR=%+v EX=%+v",
			ev.SentBytes,ev.RecvBytes,ev.TargetReadBytes,ev.TargetWrittenBytes,ev.TargetReadBytes-ev.TargetWrittenBytes,
			ev.IRWindowSent,ev.IRWindowRecv,ev.EXWindowSent,ev.EXWindowRecv,ev.IR,ev.EX)
		t.Logf("COR-T1 SOCKETS:\n%s",ev.Sockets)
		t.Logf("COR-T1 GOROUTINES:\n%s",ev.Goroutines)
		t.Fatalf("COR-01 liveness stall: no byte progress for 15s")
	default:
	}
	if sendRes.err!=nil { t.Fatal(sendRes.err) }
	if recvErr!=nil { t.Fatal(recvErr) }
	if sendRes.n!=oneGiB || recvN!=oneGiB {
		t.Fatalf("byte count mismatch sent=%d received=%d expected=%d",sendRes.n,recvN,oneGiB)
	}
	var recvSum [32]byte
	copy(recvSum[:],recvHash.Sum(nil))
	if recvSum!=sendRes.sum {
		t.Fatalf("SHA-256 mismatch sent=%x received=%x",sendRes.sum,recvSum)
	}
	t.Logf("COR-01 PASS bytes_each_direction=%d sha256=%x",oneGiB,recvSum)

	select {
	case err:=<-openDone:
		if err!=nil { t.Fatal(err) }
	case <-time.After(2*time.Second):
		t.Fatal("OpenFlow did not complete")
	}
	select {
	case err:=<-targetDone:
		if err!=nil { t.Fatal(err) }
	case <-time.After(2*time.Second):
		t.Fatal("target did not finish")
	}
	_ = reqW.Close()
	_ = resp.Body.Close()
	cancel()
	select { case <-irDone: case <-time.After(time.Second): }
	select { case <-serverErr: case <-time.After(time.Second): }
}


func TestCOR01DirectTCPControl(t *testing.T) {
	if os.Getenv("BAFT_COR01_1GIB") != "1" {
		t.Skip("set BAFT_COR01_1GIB=1 to run the 1 GiB correctness gate")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Minute)
	defer cancel()

	var sentBytes, recvBytes atomic.Int64
	var targetReadBytes, targetWrittenBytes atomic.Int64

	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer targetLn.Close()

	targetDone := make(chan error, 1)
	go func() {
		c, err := targetLn.Accept()
		if err != nil {
			targetDone <- err
			return
		}
		defer c.Close()
		buf := make([]byte, 128*1024)
		for {
			n, rerr := c.Read(buf)
			if n > 0 {
				targetReadBytes.Add(int64(n))
				if werr := writeFull(countingWriter{w: c, n: &targetWrittenBytes}, buf[:n]); werr != nil {
					targetDone <- werr
					return
				}
			}
			if rerr != nil {
				if rerr == io.EOF {
					if cw, ok := c.(interface{ CloseWrite() error }); ok {
						_ = cw.CloseWrite()
					}
					targetDone <- nil
				} else {
					targetDone <- rerr
				}
				return
			}
		}
	}()

	raw, err := net.Dial("tcp", targetLn.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	user := raw.(*net.TCPConn)
	defer user.Close()

	type sendResult struct {
		n   int64
		sum [32]byte
		err error
	}
	sent := make(chan sendResult, 1)
	go func() {
		h := sha256.New()
		src := io.TeeReader(&patternReader{remaining: oneGiB}, h)
		n, err := io.CopyBuffer(countingWriter{w: user, n: &sentBytes}, src, make([]byte, 256*1024))
		if err == nil {
			err = user.CloseWrite()
		}
		var sum [32]byte
		copy(sum[:], h.Sum(nil))
		sent <- sendResult{n: n, sum: sum, err: err}
	}()

	type directStallEvidence struct {
		sent, recv, targetRead, targetWritten int64
		goroutines                             string
	}
	stallCh := make(chan directStallEvidence, 1)
	watchDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		lastSent, lastRecv := sentBytes.Load(), recvBytes.Load()
		lastProgress := time.Now()
		for {
			select {
			case <-watchDone:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				s, r := sentBytes.Load(), recvBytes.Load()
				if s != lastSent || r != lastRecv {
					lastSent, lastRecv = s, r
					lastProgress = time.Now()
					continue
				}
				if time.Since(lastProgress) < 15*time.Second {
					continue
				}
				buf := make([]byte, 4<<20)
				n := runtime.Stack(buf, true)
				ev := directStallEvidence{
					sent:          s,
					recv:          r,
					targetRead:    targetReadBytes.Load(),
					targetWritten: targetWrittenBytes.Load(),
					goroutines:    string(buf[:n]),
				}
				select {
				case stallCh <- ev:
				default:
				}
				_ = user.Close()
				cancel()
				return
			}
		}
	}()

	recvHash := sha256.New()
	recvN, recvErr := io.CopyBuffer(recvHash, countingReader{r: user, n: &recvBytes}, make([]byte, 256*1024))
	sendRes := <-sent
	close(watchDone)

	select {
	case ev := <-stallCh:
		t.Logf("COR-T1 DIRECT STALL sent=%d recv=%d target_read=%d target_written=%d target_inflight=%d",
			ev.sent, ev.recv, ev.targetRead, ev.targetWritten, ev.targetRead-ev.targetWritten)
		t.Logf("COR-T1 DIRECT GOROUTINES:\n%s", ev.goroutines)
		t.Fatalf("COR-01 direct TCP liveness stall: no byte progress for 15s")
	default:
	}

	if sendRes.err != nil {
		t.Fatal(sendRes.err)
	}
	if recvErr != nil {
		t.Fatal(recvErr)
	}
	if sendRes.n != oneGiB || recvN != oneGiB {
		t.Fatalf("byte count mismatch sent=%d received=%d expected=%d", sendRes.n, recvN, oneGiB)
	}
	var recvSum [32]byte
	copy(recvSum[:], recvHash.Sum(nil))
	if recvSum != sendRes.sum {
		t.Fatalf("SHA-256 mismatch sent=%x received=%x", sendRes.sum, recvSum)
	}
	t.Logf("COR-01 DIRECT PASS bytes_each_direction=%d sha256=%x", oneGiB, recvSum)

	select {
	case err := <-targetDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("direct target did not finish")
	}
}
