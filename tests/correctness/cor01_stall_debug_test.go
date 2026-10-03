//go:build cor01debug

package correctness_test

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	carrierh2 "github.com/zarkmakerburg/baft/internal/carrier/h2"
	"github.com/zarkmakerburg/baft/internal/identity"
	"github.com/zarkmakerburg/baft/internal/protocol"
	"github.com/zarkmakerburg/baft/internal/routes"
	"github.com/zarkmakerburg/baft/internal/session"
)

type cor01ProgressWriter struct {
	w io.Writer
	n *atomic.Int64
}

func (p cor01ProgressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	if n > 0 {
		p.n.Add(int64(n))
	}
	return n, err
}

type cor01ProgressReader struct {
	r io.Reader
	n *atomic.Int64
}

func (p cor01ProgressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.n.Add(int64(n))
	}
	return n, err
}

type cor01FrameTapSnapshot struct {
	Frames      uint64
	Data        uint64
	Acks        uint64
	Windows     uint64
	LastAck     map[uint64]uint64
	LastWindow  map[uint64]uint64
	ParseErrors uint64
}

type cor01FrameTap struct {
	mu          sync.Mutex
	w           io.Writer
	pending     []byte
	frames      uint64
	data        uint64
	acks        uint64
	windows     uint64
	lastAck     map[uint64]uint64
	lastWindow  map[uint64]uint64
	parseErrors uint64
}

func newCOR01FrameTap(w io.Writer) *cor01FrameTap {
	return &cor01FrameTap{
		w:          w,
		pending:    make([]byte, 0, protocol.MaxFrameSize*2),
		lastAck:    map[uint64]uint64{},
		lastWindow: map[uint64]uint64{},
	}
}

func (t *cor01FrameTap) Write(p []byte) (int, error) {
	n, err := t.w.Write(p)
	if n > 0 {
		t.observe(p[:n])
	}
	return n, err
}

func (t *cor01FrameTap) observe(p []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pending = append(t.pending, p...)
	for {
		if len(t.pending) < 4 {
			return
		}
		total := int(binary.BigEndian.Uint32(t.pending[:4]))
		if total < protocol.HeaderSize || total > protocol.MaxFrameSize {
			t.parseErrors++
			t.pending = t.pending[:0]
			return
		}
		if len(t.pending) < total {
			return
		}
		hdr := t.pending[:protocol.HeaderSize]
		typ := protocol.FrameType(hdr[4])
		streamID := binary.BigEndian.Uint64(hdr[8:16])
		offset := binary.BigEndian.Uint64(hdr[16:24])
		t.frames++
		switch typ {
		case protocol.TypeData:
			t.data++
		case protocol.TypeAck:
			t.acks++
			t.lastAck[streamID] = offset
		case protocol.TypeWindow:
			t.windows++
			t.lastWindow[streamID] = offset
		}
		copy(t.pending, t.pending[total:])
		t.pending = t.pending[:len(t.pending)-total]
	}
}

func (t *cor01FrameTap) Snapshot() cor01FrameTapSnapshot {
	if t == nil {
		return cor01FrameTapSnapshot{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	s := cor01FrameTapSnapshot{
		Frames: t.frames, Data: t.data, Acks: t.acks, Windows: t.windows,
		LastAck: map[uint64]uint64{}, LastWindow: map[uint64]uint64{},
		ParseErrors: t.parseErrors,
	}
	for k, v := range t.lastAck {
		s.LastAck[k] = v
	}
	for k, v := range t.lastWindow {
		s.LastWindow[k] = v
	}
	return s
}

func logCOR01FullStallEvidence(t *testing.T, ir, ex *session.Peer, irTap, exTap *cor01FrameTap, sent, recv int64, stalledFor time.Duration) {
	t.Helper()
	t.Logf("COR-01 STALL watchdog stalled_for=%s sent_bytes=%d recv_bytes=%d delta=%d", stalledFor, sent, recv, sent-recv)
	if ir != nil {
		t.Logf("COR-01 STALL IR debug=%+v", ir.COR01DebugSnapshot())
		t.Logf("COR-01 STALL IR conservation=%+v", ir.ConservationSnapshot())
	}
	if ex != nil {
		t.Logf("COR-01 STALL EX debug=%+v", ex.COR01DebugSnapshot())
		t.Logf("COR-01 STALL EX conservation=%+v", ex.ConservationSnapshot())
	}
	t.Logf("COR-01 STALL frames IR->EX=%+v", irTap.Snapshot())
	t.Logf("COR-01 STALL frames EX->IR=%+v", exTap.Snapshot())
	buf := make([]byte, 8<<20)
	n := runtime.Stack(buf, true)
	t.Logf("COR-01 STALL goroutines:\n%s", string(buf[:n]))
}

func TestCOR01OneGiBBidirectionalInstrumented(t *testing.T) {
	if os.Getenv("BAFT_COR01_1GIB") != "1" {
		t.Skip("set BAFT_COR01_1GIB=1 to run the 1 GiB correctness gate")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Minute)
	defer cancel()

	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil { t.Fatal(err) }
	defer targetLn.Close()
	targetDone := make(chan error, 1)
	go func() {
		c, err := targetLn.Accept()
		if err != nil { targetDone <- err; return }
		defer c.Close()
		buf := make([]byte, 128*1024)
		for {
			n, rerr := c.Read(buf)
			if n > 0 {
				if werr := writeFull(c, buf[:n]); werr != nil { targetDone <- werr; return }
			}
			if rerr != nil {
				if rerr == io.EOF {
					if cw, ok := c.(interface{ CloseWrite() error }); ok { _ = cw.CloseWrite() }
					targetDone <- nil
				} else {
					targetDone <- rerr
				}
				return
			}
		}
	}()

	table, err := routes.New([]routes.Route{{
		ID: "service-main", Target: targetLn.Addr().String(),
		AllowedPeers: map[string]struct{}{"urn:baft:node:ir-01": {}},
	}})
	if err != nil { t.Fatal(err) }

	certs := makePKI(t)
	serverTLS, err := identity.ServerTLS(certs.roots, certs.server, map[string]struct{}{"urn:baft:node:ir-01": {}})
	if err != nil { t.Fatal(err) }
	serverErr := make(chan error, 1)
	exReady := make(chan *session.Peer, 1)
	exTapReady := make(chan *cor01FrameTap, 1)
	hs := httptest.NewUnstartedServer(carrierh2.Handler(func(hctx context.Context, in io.Reader, out io.Writer, peer carrierh2.PeerInfo) error {
		exTap := newCOR01FrameTap(out)
		ex, err := session.New(session.Listener, session.Carrier{In: in, Out: exTap}, peer.Identity, table, session.Options{
			NodeID: "ex-01", ExpectedPeerNodeID: "ir-01", ShardID: 0,
			ProfileID: "secure-fast", ProfileVersion: 1, ConfigRevision: "cor01-debug",
		})
		if err != nil { return err }
		select { case exReady <- ex: default: }
		select { case exTapReady <- exTap: default: }
		err = ex.Run(hctx)
		select { case serverErr <- err: default: }
		return err
	}))
	hs.EnableHTTP2 = true
	hs.TLS = serverTLS
	hs.StartTLS()
	defer hs.Close()

	clientTLS, err := identity.ClientTLS(certs.roots, certs.client, "ex.test")
	if err != nil { t.Fatal(err) }
	h2c, err := carrierh2.NewClient(hs.URL, clientTLS)
	if err != nil { t.Fatal(err) }
	defer h2c.CloseIdleConnections()

	reqR, reqW := io.Pipe()
	irTap := newCOR01FrameTap(reqW)
	resp, err := h2c.Open(ctx, reqR)
	if err != nil { t.Fatal(err) }
	defer resp.Body.Close()
	ir, err := session.New(session.Dialer, session.Carrier{In: resp.Body, Out: irTap}, "urn:baft:node:ex-01", nil, session.Options{
		NodeID: "ir-01", ExpectedPeerNodeID: "ex-01", ShardID: 0,
		ProfileID: "secure-fast", ProfileVersion: 1, ConfigRevision: "cor01-debug",
	})
	if err != nil { t.Fatal(err) }
	irDone := make(chan error, 1)
	go func() { irDone <- ir.Run(ctx) }()

	localLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil { t.Fatal(err) }
	defer localLn.Close()
	openDone := make(chan error, 1)
	go func() {
		c, err := localLn.Accept()
		if err != nil { openDone <- err; return }
		openDone <- ir.OpenFlow(ctx, "service-main", c)
	}()

	raw, err := net.Dial("tcp", localLn.Addr().String())
	if err != nil { t.Fatal(err) }
	user := raw.(*net.TCPConn)
	defer user.Close()

	var exPeer *session.Peer
	var exTap *cor01FrameTap
	select {
	case exPeer = <-exReady:
	case <-time.After(5 * time.Second):
		t.Log("COR-01 watchdog: EX peer not observed")
	}
	select {
	case exTap = <-exTapReady:
	case <-time.After(5 * time.Second):
		t.Log("COR-01 watchdog: EX frame tap not observed")
	}

	var sentProgress atomic.Int64
	var recvProgress atomic.Int64
	watchdogDone := make(chan struct{})
	defer close(watchdogDone)
	watchdogStarted := time.Now()
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		lastSent, lastRecv := sentProgress.Load(), recvProgress.Load()
		lastProgress := time.Now()
		dumped := false
		for {
			select {
			case <-watchdogDone:
				return
			case <-ticker.C:
				s, r := sentProgress.Load(), recvProgress.Load()
				if s != lastSent || r != lastRecv {
					lastSent, lastRecv = s, r
					lastProgress = time.Now()
					dumped = false
					continue
				}
				stalledFor := time.Since(lastProgress)
				totalElapsed := time.Since(watchdogStarted)
				if !dumped && (stalledFor >= 30*time.Second || totalElapsed >= 60*time.Second) {
					t.Logf("COR-01 STALL trigger stalled_for=%s total_elapsed=%s", stalledFor, totalElapsed)
					logCOR01FullStallEvidence(t, ir, exPeer, irTap, exTap, s, r, stalledFor)
					t.Log("COR-01 STALL watchdog: canceling debug context after evidence dump")
					cancel()
					dumped = true
				}
			}
		}
	}()

	type sendResult struct {
		n int64
		sum [32]byte
		err error
	}
	sent := make(chan sendResult, 1)
	go func() {
		h := sha256.New()
		src := io.TeeReader(&patternReader{remaining: oneGiB}, h)
		n, err := io.CopyBuffer(cor01ProgressWriter{w: user, n: &sentProgress}, src, make([]byte, 256*1024))
		if err == nil { err = user.CloseWrite() }
		var sum [32]byte
		copy(sum[:], h.Sum(nil))
		sent <- sendResult{n: n, sum: sum, err: err}
	}()

	recvHash := sha256.New()
	recvN, recvErr := io.CopyBuffer(recvHash, cor01ProgressReader{r: user, n: &recvProgress}, make([]byte, 256*1024))
	sendRes := <-sent
	if sendRes.err != nil { t.Fatal(sendRes.err) }
	if recvErr != nil { t.Fatal(recvErr) }
	if sendRes.n != oneGiB || recvN != oneGiB {
		t.Fatalf("byte count mismatch sent=%d received=%d expected=%d", sendRes.n, recvN, oneGiB)
	}
	var recvSum [32]byte
	copy(recvSum[:], recvHash.Sum(nil))
	if recvSum != sendRes.sum {
		t.Fatalf("SHA-256 mismatch sent=%x received=%x", sendRes.sum, recvSum)
	}
	t.Logf("COR-01 PASS bytes_each_direction=%d sha256=%x", oneGiB, recvSum)
	t.Logf("COR-01 FINAL IR debug=%+v", ir.COR01DebugSnapshot())
	if exPeer != nil { t.Logf("COR-01 FINAL EX debug=%+v", exPeer.COR01DebugSnapshot()) }
	t.Logf("COR-01 FINAL frames IR->EX=%+v", irTap.Snapshot())
	t.Logf("COR-01 FINAL frames EX->IR=%+v", exTap.Snapshot())

	select {
	case err := <-openDone:
		if err != nil { t.Fatal(err) }
	case <-time.After(2 * time.Second):
		t.Fatal("OpenFlow did not complete")
	}
	select {
	case err := <-targetDone:
		if err != nil { t.Fatal(err) }
	case <-time.After(2 * time.Second):
		t.Fatal("target did not finish")
	}
	_ = reqW.Close()
	_ = resp.Body.Close()
	cancel()
	select { case <-irDone: case <-time.After(time.Second): }
	select { case <-serverErr: case <-time.After(time.Second): }
}
