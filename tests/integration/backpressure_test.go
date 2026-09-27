package integration_test

import (
	"context"
	"crypto/tls"
	"github.com/zarkmakerburg/baft/internal/recordshape"
	"github.com/zarkmakerburg/baft/internal/securityinternal"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	carrierh2 "github.com/zarkmakerburg/baft/internal/carrier/h2"
	"github.com/zarkmakerburg/baft/internal/identity"
	"github.com/zarkmakerburg/baft/internal/resources"
	"github.com/zarkmakerburg/baft/internal/routes"
	"github.com/zarkmakerburg/baft/internal/session"
)

type countingZeroReader struct {
	n atomic.Int64
}

func (r *countingZeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	r.n.Add(int64(len(p)))
	return len(p), nil
}

func smallAllocator(t *testing.T) *resources.Allocator {
	t.Helper()
	a, err := resources.NewAllocator(resources.Limits{
		Total:          128 * 1024,
		Receive:        64 * 1024,
		Replay:         64 * 1024,
		PerFlowReceive: 64 * 1024,
		PerFlowReplay:  64 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestSlowReceiverCreatesBackpressureWithoutGrowingBAFTMemory(t *testing.T) {
	runSlowReceiver(t, false)
}
func TestNoiseMorphingBackpressureBounded(t *testing.T) { runSlowReceiver(t, true) }
func runSlowReceiver(t *testing.T, stealth bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer targetLn.Close()

	targetAccepted := make(chan struct{})
	releaseTarget := make(chan struct{})
	targetDone := make(chan error, 1)
	go func() {
		c, err := targetLn.Accept()
		if err != nil {
			targetDone <- err
			return
		}
		defer c.Close()
		// This fixture is an external target: TCP FIN may follow a long drain of
		// kernel-buffered bytes. Stop its blocking copy when the test is cancelled.
		stop := context.AfterFunc(ctx, func() { _ = c.Close() })
		defer stop()
		if tcp, ok := c.(*net.TCPConn); ok {
			_ = tcp.SetReadBuffer(1024)
		}
		close(targetAccepted)
		select {
		case <-releaseTarget:
		case <-ctx.Done():
			targetDone <- ctx.Err()
			return
		}
		_, err = io.Copy(io.Discard, c)
		if err == nil {
			if cw, ok := c.(interface{ CloseWrite() error }); ok {
				err = cw.CloseWrite()
			}
		}
		targetDone <- err
	}()

	table, err := routes.New([]routes.Route{{
		ID:           "service-main",
		Target:       targetLn.Addr().String(),
		AllowedPeers: map[string]struct{}{"urn:baft:node:ir-01": {}},
	}})
	if err != nil {
		t.Fatal(err)
	}

	irAlloc := smallAllocator(t)
	exAlloc := smallAllocator(t)
	certs := testPKI(t)
	serverTLS, err := identity.ServerTLS(certs.roots, certs.server, map[string]struct{}{"urn:baft:node:ir-01": {}})
	if err != nil {
		t.Fatal(err)
	}

	serverErr := make(chan error, 1)
	stream := func(hctx context.Context, in io.Reader, out io.Writer, peer carrierh2.PeerInfo) error {
		ex, err := session.New(session.Listener, session.Carrier{In: in, Out: out}, peer.Identity, table, session.Options{
			NodeID: "ex-01", ExpectedPeerNodeID: "ir-01", ShardID: 0,
			ProfileID: "secure-fast", ProfileVersion: 1, ConfigRevision: "slow-receiver",
			Resources: exAlloc,
		})
		if err != nil {
			return err
		}
		err = ex.Run(hctx)
		select {
		case serverErr <- err:
		default:
		}
		return err
	}
	var handler http.Handler = carrierh2.Handler(stream)
	var initCfg securityinternal.HandshakeConfig
	if stealth {
		ik, e := securityinternal.GenerateKeyPair()
		if e != nil {
			t.Fatal(e)
		}
		rk, e := securityinternal.GenerateKeyPair()
		if e != nil {
			t.Fatal(e)
		}
		shape := recordshape.DefaultConfig(true)
		initCfg = securityinternal.HandshakeConfig{Static: ik, PeerStatic: rk.Public, RecordShaping: shape}
		handler, err = carrierh2.HandlerWithNoise(stream, carrierh2.NoiseOptions{Handshake: securityinternal.HandshakeConfig{Static: rk, PeerStatic: ik.Public, RecordShaping: shape}, PeerIdentity: "urn:baft:node:ir-01"})
		if err != nil {
			t.Fatal(err)
		}
		serverTLS.ClientAuth = tls.NoClientCert
		serverTLS.VerifyConnection = nil
	}
	hs := httptest.NewUnstartedServer(handler)
	hs.EnableHTTP2 = true
	hs.TLS = serverTLS
	hs.StartTLS()
	defer hs.Close()

	clientTLS, err := identity.ClientTLS(certs.roots, certs.client, "ex.test")
	if err != nil {
		t.Fatal(err)
	}
	h2c, err := carrierh2.NewClient(hs.URL, clientTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer h2c.CloseIdleConnections()

	var reqW *io.PipeWriter
	var resp *http.Response
	var carrier session.Carrier
	if stealth {
		var conn *securityinternal.Conn
		resp, reqW, conn, err = h2c.OpenNoise(ctx, initCfg)
		if err != nil {
			t.Fatal(err)
		}
		carrier = session.Carrier{In: conn, Out: conn}
	} else {
		reqR, pw := io.Pipe()
		reqW = pw
		resp, err = h2c.Open(ctx, reqR)
		if err != nil {
			t.Fatal(err)
		}
		carrier = session.Carrier{In: resp.Body, Out: reqW}
	}
	defer resp.Body.Close()
	defer reqW.Close()
	ir, err := session.New(session.Dialer, carrier, "urn:baft:node:ex-01", nil, session.Options{
		NodeID: "ir-01", ExpectedPeerNodeID: "ex-01", ShardID: 0,
		ProfileID: "secure-fast", ProfileVersion: 1, ConfigRevision: "slow-receiver",
		Resources: irAlloc,
	})
	if err != nil {
		t.Fatal(err)
	}
	irDone := make(chan error, 1)
	go func() { irDone <- ir.Run(ctx) }()

	localLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer localLn.Close()
	openDone := make(chan error, 1)
	go func() {
		c, err := localLn.Accept()
		if err != nil {
			openDone <- err
			return
		}
		if tcp, ok := c.(*net.TCPConn); ok {
			_ = tcp.SetReadBuffer(1024)
		}
		openDone <- ir.OpenFlow(ctx, "service-main", c)
	}()

	raw, err := net.Dial("tcp", localLn.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	user := raw.(*net.TCPConn)
	defer user.Close()
	_ = user.SetWriteBuffer(1024)

	select {
	case err := <-openDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("flow did not open")
	}
	select {
	case <-targetAccepted:
	case <-time.After(2 * time.Second):
		t.Fatal("target was not connected")
	}

	const totalSource = int64(16 << 20)
	source := &countingZeroReader{}
	writeDone := make(chan error, 1)
	go func() {
		_, err := io.CopyN(user, source, totalSource)
		if err == nil {
			err = user.CloseWrite()
		}
		writeDone <- err
	}()

	select {
	case err := <-writeDone:
		t.Fatalf("16 MiB source completed while target was not reading; expected backpressure, err=%v", err)
	case <-time.After(200 * time.Millisecond):
	}
	stalledAt := source.n.Load()
	if stalledAt <= 0 || stalledAt >= totalSource {
		t.Fatalf("unexpected source progress before drain: %d", stalledAt)
	}

	irSnap := irAlloc.Snapshot()
	exSnap := exAlloc.Snapshot()
	if irSnap.TotalUsed > 128*1024 || irSnap.ReplayUsed > 64*1024 {
		t.Fatalf("IR allocator exceeded configured cap: %#v", irSnap)
	}
	if exSnap.TotalUsed > 128*1024 || exSnap.ReceiveUsed > 64*1024 {
		t.Fatalf("EX allocator exceeded configured cap: %#v", exSnap)
	}
	if exSnap.ReceiveUsed != 64*1024 {
		t.Fatalf("expected one stable 64 KiB receive reservation, got %#v", exSnap)
	}

	close(releaseTarget)

	progressDeadline := time.NewTimer(3 * time.Second)
	defer progressDeadline.Stop()
	progressTick := time.NewTicker(10 * time.Millisecond)
	defer progressTick.Stop()
	resumed := false
	for !resumed {
		select {
		case err := <-writeDone:
			if err != nil {
				t.Fatal(err)
			}
			resumed = true
		case <-progressTick.C:
			if source.n.Load() > stalledAt {
				resumed = true
			}
		case <-progressDeadline.C:
			t.Fatalf("source did not resume after receiver drain: stalled=%d now=%d", stalledAt, source.n.Load())
		}
	}

	// This test is about bounded backpressure and liveness after drain. COR-01
	// separately proves full-transfer correctness, so stop this intentionally
	// slow flow once forward progress has been observed.
	_ = user.Close()
	cancel()
	_ = reqW.Close()
	_ = resp.Body.Close()

	select {
	case <-writeDone:
	case <-time.After(time.Second):
		t.Fatal("source writer did not stop after cancellation")
	}
	select {
	case <-targetDone:
	case <-time.After(2 * time.Second):
		t.Fatal("target did not stop after cancellation")
	}
	select {
	case <-irDone:
	case <-time.After(time.Second):
	}
	select {
	case <-serverErr:
	case <-time.After(time.Second):
	}
}
