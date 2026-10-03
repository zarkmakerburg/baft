package integration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/protocol"
	"github.com/zarkmakerburg/baft/internal/routes"
	"github.com/zarkmakerburg/baft/internal/session"
)

// TestStageEMeasurePlainTCPSessionThroughput isolates the cost of BAFT
// Session/framing/replay/flow-control from the H2+TLS carrier. It uses the
// exact B06 flow count and payload profile but a raw loopback TCP carrier.
func TestStageEMeasurePlainTCPSessionThroughput(t *testing.T) {
	const (
		flowCount    = 8
		bytesPerFlow = 4 * 1024 * 1024
	)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil { t.Fatal(err) }
	defer targetLn.Close()

	var targetWG sync.WaitGroup
	targetAcceptErr := make(chan error, 1)
	go func() {
		for i := 0; i < flowCount; i++ {
			c, err := targetLn.Accept()
			if err != nil { targetAcceptErr <- err; return }
			targetWG.Add(1)
			go func(c net.Conn) {
				defer targetWG.Done()
				defer c.Close()
				buf := make([]byte, 64*1024)
				for {
					n, rerr := c.Read(buf)
					if n > 0 {
						p := buf[:n]
						for len(p) > 0 {
							w, werr := c.Write(p)
							if werr != nil { return }
							p = p[w:]
						}
					}
					if rerr != nil {
						if cw, ok := c.(interface{ CloseWrite() error }); ok { _ = cw.CloseWrite() }
						return
					}
				}
			}(c)
		}
		targetAcceptErr <- nil
	}()

	table, err := routes.New([]routes.Route{{
		ID: "service-main", Target: targetLn.Addr().String(),
		AllowedPeers: map[string]struct{}{"urn:baft:node:ir-01": {}},
	}})
	if err != nil { t.Fatal(err) }

	carrierLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil { t.Fatal(err) }
	defer carrierLn.Close()
	serverDone := make(chan error, 1)
	go func() {
		c, err := carrierLn.Accept()
		if err != nil { serverDone <- err; return }
		defer c.Close()
		ex, err := session.New(session.Listener, session.Carrier{In:c, Out:c}, "urn:baft:node:ir-01", table, session.Options{
			NodeID:"ex-01", ExpectedPeerNodeID:"ir-01", ShardID:0,
			ProfileID:"secure-fast", ProfileVersion:1, ConfigRevision:"stage-e-plain-tcp",
		})
		if err != nil { serverDone <- err; return }
		serverDone <- ex.Run(ctx)
	}()

	carrierConn, err := net.DialTimeout("tcp", carrierLn.Addr().String(), time.Second)
	if err != nil { t.Fatal(err) }
	defer carrierConn.Close()
	ir, err := session.New(session.Dialer, session.Carrier{In:carrierConn, Out:carrierConn}, "urn:baft:node:ex-01", nil, session.Options{
		NodeID:"ir-01", ExpectedPeerNodeID:"ex-01", ShardID:0,
		ProfileID:"secure-fast", ProfileVersion:1, ConfigRevision:"stage-e-plain-tcp",
	})
	if err != nil { t.Fatal(err) }
	irDone := make(chan error,1)
	go func(){ irDone <- ir.Run(ctx) }()

	localLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil { t.Fatal(err) }
	defer localLn.Close()
	openErr := make(chan error, flowCount)
	go func(){
		for i:=0;i<flowCount;i++ {
			c, err := localLn.Accept()
			if err != nil { openErr <- err; continue }
			go func(c net.Conn){ openErr <- ir.OpenFlow(ctx,"service-main",c) }(c)
		}
	}()

	ready := make(chan error, flowCount)
	startBulk := make(chan struct{})
	clientErr := make(chan error, flowCount)
	var clients sync.WaitGroup
	for i:=0;i<flowCount;i++ {
		i:=i
		clients.Add(1)
		go func(){
			defer clients.Done()
			raw,err:=net.DialTimeout("tcp",localLn.Addr().String(),time.Second)
			if err!=nil { ready<-err; clientErr<-err; return }
			c:=raw.(*net.TCPConn)
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(35*time.Second))

			prelude:=[]byte{0x42,byte(i),0x45}
			if _,err:=c.Write(prelude);err!=nil{ready<-err;clientErr<-err;return}
			got:=make([]byte,len(prelude))
			if _,err:=io.ReadFull(c,got);err!=nil{ready<-err;clientErr<-err;return}
			if !bytes.Equal(got,prelude){err:=fmt.Errorf("flow %d warmup echo mismatch",i);ready<-err;clientErr<-err;return}

			payload:=make([]byte,bytesPerFlow)
			for j:=range payload { payload[j]=byte((j*17+i*29)%251) }
			want:=sha256.Sum256(payload)
			ready<-nil
			<-startBulk

			writeDone:=make(chan error,1)
			go func(){
				_,err:=io.Copy(c,bytes.NewReader(payload))
				if err==nil { err=c.CloseWrite() }
				writeDone<-err
			}()
			h:=sha256.New()
			n,err:=io.Copy(h,c)
			if err!=nil{clientErr<-err;return}
			if err:=<-writeDone;err!=nil{clientErr<-err;return}
			if n!=int64(len(payload)){clientErr<-fmt.Errorf("flow %d bytes=%d want=%d",i,n,len(payload));return}
			if !bytes.Equal(h.Sum(nil),want[:]){clientErr<-fmt.Errorf("flow %d hash mismatch",i);return}
			clientErr<-nil
		}()
	}

	for i:=0;i<flowCount;i++ { if err:=<-ready;err!=nil{t.Fatalf("warmup %d: %v",i,err)} }
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	goroutinesBefore:=runtime.NumGoroutine()
	fdBefore:=stageEFDCount()

	started:=time.Now()
	close(startBulk)
	clients.Wait()
	elapsed:=time.Since(started)

	for i:=0;i<flowCount;i++ { if err:=<-clientErr;err!=nil{t.Fatalf("client %d: %v",i,err)} }
	for i:=0;i<flowCount;i++ { if err:=<-openErr;err!=nil{t.Fatalf("open %d: %v",i,err)} }
	if err:=<-targetAcceptErr;err!=nil{t.Fatal(err)}
	targetWG.Wait()

	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	txBytes:=int64(flowCount*bytesPerFlow)
	txMbps:=float64(txBytes*8)/elapsed.Seconds()/1_000_000
	emitStageEMetric(t,map[string]any{
		"scenario":"B10",
		"measurement":"plain_tcp_baft_session_throughput",
		"flows":flowCount,
		"tx_bytes":txBytes,
		"rx_bytes":txBytes,
		"elapsed_ms":float64(elapsed)/float64(time.Millisecond),
		"tx_mbps":txMbps,
		"rx_mbps":txMbps,
		"aggregate_mbps":txMbps*2,
		"heap_alloc_before_bytes":before.HeapAlloc,
		"heap_alloc_after_bytes":after.HeapAlloc,
		"goroutines_before":goroutinesBefore,
		"goroutines_after":runtime.NumGoroutine(),
		"fd_before":fdBefore,
		"fd_after":stageEFDCount(),
		"scope":"BAFT Session/framing over raw loopback TCP; same B06 warmed-flow payload profile; no H2/TLS",
	})

	cancel()
	_ = carrierConn.Close()
	select { case <-irDone: case <-time.After(time.Second): }
	select { case <-serverDone: case <-time.After(time.Second): }
}

// TestStageEMeasureProtocolDecodeAllocation quantifies protocol.Decode's
// payload allocation on the same 32 KiB DATA frame size used by the Session
// data pump. It does not modify or replace production Decode.
func TestStageEMeasureProtocolDecodeAllocation(t *testing.T) {
	payload:=make([]byte,32*1024)
	for i:=range payload { payload[i]=byte(i%251) }
	var wire bytes.Buffer
	if err:=protocol.Encode(&wire,protocol.Frame{Type:protocol.TypeData,StreamID:1,Offset:0,Payload:payload});err!=nil{t.Fatal(err)}
	encoded:=append([]byte(nil),wire.Bytes()...)

	var r bytes.Reader
	r.Reset(encoded)
	if _,err:=protocol.Decode(&r);err!=nil{t.Fatal(err)}

	allocs:=testing.AllocsPerRun(2000,func(){
		r.Reset(encoded)
		if _,err:=protocol.Decode(&r);err!=nil{panic(err)}
	})

	const loops=10000
	runtime.GC()
	var before,after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i:=0;i<loops;i++{
		r.Reset(encoded)
		if _,err:=protocol.Decode(&r);err!=nil{t.Fatal(err)}
	}
	runtime.ReadMemStats(&after)
	totalAlloc:=after.TotalAlloc-before.TotalAlloc
	bytesPerDecode:=float64(totalAlloc)/loops

	emitStageEMetric(t,map[string]any{
		"scenario":"B11",
		"measurement":"protocol_decode_allocation",
		"payload_bytes":len(payload),
		"encoded_frame_bytes":len(encoded),
		"allocs_per_decode":allocs,
		"bytes_allocated_per_decode":bytesPerDecode,
		"allocated_bytes_per_payload_byte":bytesPerDecode/float64(len(payload)),
		"iterations":loops,
		"scope":"protocol.Decode 32KiB DATA frame allocation; bytes.Reader reused; production implementation unchanged",
	})
}
