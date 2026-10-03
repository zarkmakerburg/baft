package integration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sort"
	"sync"
	"testing"
	"time"

	carrierh2 "github.com/zarkmakerburg/baft/internal/carrier/h2"
	"github.com/zarkmakerburg/baft/internal/identity"
	"github.com/zarkmakerburg/baft/internal/protocol"
	"github.com/zarkmakerburg/baft/internal/routes"
	"github.com/zarkmakerburg/baft/internal/session"
)

const stageEMetricPrefix = "BAFT_BENCH_METRIC "

func emitStageEMetric(t *testing.T, metric map[string]any) {
	t.Helper()
	b, err := json.Marshal(metric)
	if err != nil {
		t.Fatalf("marshal benchmark metric: %v", err)
	}
	t.Logf("%s%s", stageEMetricPrefix, b)
}

func percentileMillis(values []time.Duration, percentile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	cp := append([]time.Duration(nil), values...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	idx := int(float64(len(cp)-1) * percentile)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(cp) {
		idx = len(cp) - 1
	}
	return float64(cp[idx]) / float64(time.Millisecond)
}


type stageEFrameCoalescingWriter struct {
	mu      sync.Mutex
	w       io.Writer
	pending []byte
	want    int
}

func (w *stageEFrameCoalescingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending = append(w.pending, p...)
	for {
		if w.want == 0 {
			if len(w.pending) < 4 {
				break
			}
			w.want = int(binary.BigEndian.Uint32(w.pending[:4]))
			if w.want < protocol.HeaderSize || w.want > protocol.MaxFrameSize {
				return 0, fmt.Errorf("benchmark coalescer invalid frame size %d", w.want)
			}
		}
		if len(w.pending) < w.want {
			break
		}
		n, err := w.w.Write(w.pending[:w.want])
		if err != nil {
			return 0, err
		}
		if n != w.want {
			return 0, io.ErrShortWrite
		}
		copy(w.pending, w.pending[w.want:])
		w.pending = w.pending[:len(w.pending)-w.want]
		w.want = 0
	}
	return len(p), nil
}

func stageECarrierWriter(w io.Writer, coalesce bool) io.Writer {
	if !coalesce {
		return w
	}
	return &stageEFrameCoalescingWriter{w: w, pending: make([]byte, 0, protocol.MaxFrameSize)}
}

func stageEFDCount() int {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	return len(entries)
}

func stageEIntEnv(t *testing.T, name string, fallback, min, max int) int {
	t.Helper()
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < min || v > max {
		t.Fatalf("%s=%q must be an integer in [%d,%d]", name, raw, min, max)
	}
	return v
}

func stageEMaybeApplyNetem(t *testing.T, addr net.Addr) (int, float64, func()) {
	t.Helper()
	rttRaw := os.Getenv("BAFT_STAGE_E_NETEM_RTT_MS")
	lossRaw := os.Getenv("BAFT_STAGE_E_NETEM_LOSS_PCT")
	if rttRaw == "" && lossRaw == "" {
		return 0, 0, func() {}
	}
	if runtime.GOOS != "linux" {
		t.Fatalf("Stage E netem requires Linux")
	}
	rttMS := 0
	var err error
	if rttRaw != "" {
		rttMS, err = strconv.Atoi(rttRaw)
		if err != nil || rttMS < 0 || rttMS > 10000 {
			t.Fatalf("BAFT_STAGE_E_NETEM_RTT_MS=%q must be an integer in [0,10000]", rttRaw)
		}
	}
	lossPct := 0.0
	if lossRaw != "" {
		lossPct, err = strconv.ParseFloat(lossRaw, 64)
		if err != nil || lossPct < 0 || lossPct > 100 {
			t.Fatalf("BAFT_STAGE_E_NETEM_LOSS_PCT=%q must be in [0,100]", lossRaw)
		}
	}
	_, portRaw, err := net.SplitHostPort(addr.String())
	if err != nil {
		t.Fatalf("netem split address %q: %v", addr.String(), err)
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil || port <= 0 || port > 65535 {
		t.Fatalf("netem invalid port %q", portRaw)
	}

	runTC := func(ignore bool, args ...string) {
		cmdArgs := append([]string{"-n", "tc"}, args...)
		out, runErr := exec.Command("sudo", cmdArgs...).CombinedOutput()
		if runErr != nil && !ignore {
			t.Fatalf("netem tc %v: %v: %s", args, runErr, bytes.TrimSpace(out))
		}
	}
	runTC(true, "qdisc", "del", "dev", "lo", "root")
	runTC(false, "qdisc", "add", "dev", "lo", "root", "handle", "1:", "prio", "bands", "3")
	oneWayMS := float64(rttMS) / 2
	runTC(false, "qdisc", "add", "dev", "lo", "parent", "1:3", "handle", "30:", "netem",
		"delay", fmt.Sprintf("%.3fms", oneWayMS), "loss", fmt.Sprintf("%.3f%%", lossPct))
	portText := strconv.Itoa(port)
	runTC(false, "filter", "add", "dev", "lo", "protocol", "ip", "parent", "1:0", "prio", "3", "u32",
		"match", "ip", "dport", portText, "0xffff", "flowid", "1:3")
	runTC(false, "filter", "add", "dev", "lo", "protocol", "ip", "parent", "1:0", "prio", "3", "u32",
		"match", "ip", "sport", portText, "0xffff", "flowid", "1:3")
	t.Logf("STAGE_E_NETEM rtt_ms=%d loss_pct=%.3f one_way_delay_ms=%.3f carrier_port=%d", rttMS, lossPct, oneWayMS, port)

	cleanup := func() {
		runTC(true, "qdisc", "del", "dev", "lo", "root")
	}
	return rttMS, lossPct, cleanup
}

// TestStageEMeasureMultiFlowThroughput measures the application-visible
// payload rate through the real BAFT Session + H2/mTLS path on loopback.
// It intentionally excludes fixture setup by warming every Flow before the
// timed bulk transfer. This is a reproducible local-path measurement, not an
// Internet-throughput claim.
func TestStageEMeasureMultiFlowThroughput(t *testing.T) {
	runStageEMeasureMultiFlowThroughput(t, false, "B06", "loopback_multiflow_throughput", "loopback BAFT Session + H2/mTLS; warmed flows; not public-network throughput")
}

func TestStageEMeasureFrameCoalescedThroughput(t *testing.T) {
	runStageEMeasureMultiFlowThroughput(t, true, "B09", "loopback_frame_coalesced_throughput", "test-only frame-coalesced BAFT Session + H2/mTLS; same B06 payload profile")
}

func runStageEMeasureMultiFlowThroughput(t *testing.T, coalesce bool, scenario, measurement, scope string) {
	const flowCount = 8
	bytesPerFlow := stageEIntEnv(t, "BAFT_STAGE_E_BYTES_PER_FLOW", 4*1024*1024, 64*1024, 64*1024*1024)
	testTimeout := 45 * time.Second
	clientDeadline := 35 * time.Second
	if os.Getenv("BAFT_STAGE_E_NETEM_RTT_MS") != "" || os.Getenv("BAFT_STAGE_E_NETEM_LOSS_PCT") != "" {
		testTimeout = 5 * time.Minute
		clientDeadline = 4 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer targetLn.Close()

	var targetWG sync.WaitGroup
	targetAcceptErr := make(chan error, 1)
	go func() {
		for i := 0; i < flowCount; i++ {
			c, err := targetLn.Accept()
			if err != nil {
				targetAcceptErr <- err
				return
			}
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
							if werr != nil {
								return
							}
							p = p[w:]
						}
					}
					if rerr != nil {
						if cw, ok := c.(interface{ CloseWrite() error }); ok {
							_ = cw.CloseWrite()
						}
						return
					}
				}
			}(c)
		}
		targetAcceptErr <- nil
	}()

	table, err := routes.New([]routes.Route{{
		ID:           "service-main",
		Target:       targetLn.Addr().String(),
		AllowedPeers: map[string]struct{}{"urn:baft:node:ir-01": {}},
	}})
	if err != nil {
		t.Fatal(err)
	}

	certs := testPKI(t)
	serverTLS, err := identity.ServerTLS(certs.roots, certs.server, map[string]struct{}{"urn:baft:node:ir-01": {}})
	if err != nil {
		t.Fatal(err)
	}
	serverErr := make(chan error, 1)
	hs := httptest.NewUnstartedServer(carrierh2.Handler(func(hctx context.Context, in io.Reader, out io.Writer, peer carrierh2.PeerInfo) error {
		ex, err := session.New(session.Listener, session.Carrier{In: in, Out: stageECarrierWriter(out, coalesce)}, peer.Identity, table, session.Options{
			NodeID: "ex-01", ExpectedPeerNodeID: "ir-01", ShardID: 0,
			ProfileID: "secure-fast", ProfileVersion: 1, ConfigRevision: "stage-e-throughput",
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
	}))
	hs.EnableHTTP2 = true
	hs.TLS = serverTLS
	netemRTTMS, netemLossPct, cleanupNetem := stageEMaybeApplyNetem(t, hs.Listener.Addr())
	defer cleanupNetem()
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

	reqR, reqW := io.Pipe()
	resp, err := h2c.Open(ctx, reqR)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	ir, err := session.New(session.Dialer, session.Carrier{In: resp.Body, Out: stageECarrierWriter(reqW, coalesce)}, "urn:baft:node:ex-01", nil, session.Options{
		NodeID: "ir-01", ExpectedPeerNodeID: "ex-01", ShardID: 0,
		ProfileID: "secure-fast", ProfileVersion: 1, ConfigRevision: "stage-e-throughput",
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
	openErr := make(chan error, flowCount)
	go func() {
		for i := 0; i < flowCount; i++ {
			c, err := localLn.Accept()
			if err != nil {
				openErr <- err
				continue
			}
			go func(c net.Conn) { openErr <- ir.OpenFlow(ctx, "service-main", c) }(c)
		}
	}()

	ready := make(chan error, flowCount)
	startBulk := make(chan struct{})
	clientErr := make(chan error, flowCount)
	var clients sync.WaitGroup

	for i := 0; i < flowCount; i++ {
		i := i
		clients.Add(1)
		go func() {
			defer clients.Done()
			raw, err := net.DialTimeout("tcp", localLn.Addr().String(), time.Second)
			if err != nil {
				ready <- err
				clientErr <- err
				return
			}
			c := raw.(*net.TCPConn)
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(clientDeadline))

			prelude := []byte{0x42, byte(i), 0x45}
			if _, err := c.Write(prelude); err != nil {
				ready <- err
				clientErr <- err
				return
			}
			gotPrelude := make([]byte, len(prelude))
			if _, err := io.ReadFull(c, gotPrelude); err != nil {
				ready <- err
				clientErr <- err
				return
			}
			if !bytes.Equal(gotPrelude, prelude) {
				err := fmt.Errorf("flow %d warmup echo mismatch", i)
				ready <- err
				clientErr <- err
				return
			}
			// Prepare payload and expected hash before the timed region so B06
			// measures transport work rather than fixture allocation/generation.
			payload := make([]byte, bytesPerFlow)
			for j := range payload {
				payload[j] = byte((j*17 + i*29) % 251)
			}
			want := sha256.Sum256(payload)
			ready <- nil
			<-startBulk
			writeDone := make(chan error, 1)
			go func() {
				_, err := io.Copy(c, bytes.NewReader(payload))
				if err == nil {
					err = c.CloseWrite()
				}
				writeDone <- err
			}()

			h := sha256.New()
			n, err := io.Copy(h, c)
			if err != nil {
				clientErr <- err
				return
			}
			if err := <-writeDone; err != nil {
				clientErr <- err
				return
			}
			if n != int64(len(payload)) {
				clientErr <- fmt.Errorf("flow %d bytes=%d want=%d", i, n, len(payload))
				return
			}
			if !bytes.Equal(h.Sum(nil), want[:]) {
				clientErr <- fmt.Errorf("flow %d hash mismatch", i)
				return
			}
			clientErr <- nil
		}()
	}

	for i := 0; i < flowCount; i++ {
		if err := <-ready; err != nil {
			t.Fatalf("warmup %d: %v", i, err)
		}
	}

	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	goroutinesBefore := runtime.NumGoroutine()
	fdBefore := stageEFDCount()

	started := time.Now()
	close(startBulk)
	clients.Wait()
	elapsed := time.Since(started)

	for i := 0; i < flowCount; i++ {
		if err := <-clientErr; err != nil {
			t.Fatalf("client %d: %v", i, err)
		}
	}
	for i := 0; i < flowCount; i++ {
		if err := <-openErr; err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
	}
	if err := <-targetAcceptErr; err != nil {
		t.Fatal(err)
	}
	targetWG.Wait()

	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	fdAfter := stageEFDCount()
	goroutinesAfter := runtime.NumGoroutine()

	txBytes := int64(flowCount * bytesPerFlow)
	seconds := elapsed.Seconds()
	txMbps := float64(txBytes*8) / seconds / 1_000_000
	aggregateMbps := txMbps * 2 // payload traverses BAFT in both directions via echo

	emitStageEMetric(t, map[string]any{
		"scenario":                 scenario,
		"measurement":              measurement,
		"flows":                    flowCount,
		"tx_bytes":                 txBytes,
		"rx_bytes":                 txBytes,
		"elapsed_ms":               float64(elapsed) / float64(time.Millisecond),
		"tx_mbps":                  txMbps,
		"rx_mbps":                  txMbps,
		"aggregate_mbps":           aggregateMbps,
		"heap_alloc_before_bytes":  before.HeapAlloc,
		"heap_alloc_after_bytes":   after.HeapAlloc,
		"goroutines_before":        goroutinesBefore,
		"goroutines_after":         goroutinesAfter,
		"fd_before":                fdBefore,
		"fd_after":                 fdAfter,
		"netem_rtt_ms":             netemRTTMS,
		"netem_loss_percent":       netemLossPct,
		"scope":                    scope,
	})

	_ = reqW.Close()
	_ = resp.Body.Close()
	cancel()
	select {
	case <-irDone:
	case <-time.After(time.Second):
	}
	select {
	case <-serverErr:
	case <-time.After(time.Second):
	}
}

// TestStageEMeasureRecovery measures application-visible recovery latency and
// session survival across one abrupt Carrier cut. All probe TCP connections
// remain the same application flows; reopening a target socket is a failure.
func TestStageEMeasureRecovery(t *testing.T) {
	flows := stageEIntEnv(t, "BAFT_STAGE_E_RECOVERY_FLOWS", 8, 1, 512)
	p := startRecoveryRuntimePair(t, 1)
	defer p.close(t)

	conns := make([]net.Conn, 0, flows)
	for i := 0; i < flows; i++ {
		c := openRecoveryFlow(t, p)
		conns = append(conns, c)
		defer c.Close()
	}
	targetBefore := p.targetAccepts.Load()

	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	goroutinesBefore := runtime.NumGoroutine()
	fdBefore := stageEFDCount()

	cutAt := time.Now()
	cutIDs := p.proxy.CutAll()

	type probeResult struct {
		duration time.Duration
		err      error
	}
	results := make(chan probeResult, flows)
	var wg sync.WaitGroup
	for i, c := range conns {
		i, c := i, c
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.SetDeadline(cutAt.Add(15 * time.Second))
			payload := make([]byte, 4*1024+i*17)
			for j := range payload {
				payload[j] = byte((j*31 + i*7) % 251)
			}
			if _, err := c.Write(payload); err != nil {
				results <- probeResult{err: err}
				return
			}
			got := make([]byte, len(payload))
			if _, err := io.ReadFull(c, got); err != nil {
				results <- probeResult{err: err}
				return
			}
			if !bytes.Equal(got, payload) {
				results <- probeResult{err: fmt.Errorf("flow %d recovery echo mismatch", i)}
				return
			}
			results <- probeResult{duration: time.Since(cutAt)}
		}()
	}
	wg.Wait()
	close(results)

	var latencies []time.Duration
	var failures []string
	for r := range results {
		if r.err != nil {
			failures = append(failures, r.err.Error())
			continue
		}
		latencies = append(latencies, r.duration)
	}

	irAuth, exAuth := waitAuthorityPair(t, p, 2, true)
	targetAfter := p.targetAccepts.Load()

	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	goroutinesAfter := runtime.NumGoroutine()
	fdAfter := stageEFDCount()

	survived := len(latencies)
	survivalPercent := 100 * float64(survived) / float64(flows)
	emitStageEMetric(t, map[string]any{
		"scenario":                 "B07",
		"measurement":              "carrier_cut_recovery",
		"flows":                    flows,
		"flows_survived":           survived,
		"flow_survival_percent":    survivalPercent,
		"logical_session_survived": survived == flows,
		"recovery_ms_p50":          percentileMillis(latencies, 0.50),
		"recovery_ms_p95":          percentileMillis(latencies, 0.95),
		"recovery_ms_p99":          percentileMillis(latencies, 0.99),
		"carrier_connections_cut":  len(cutIDs),
		"target_accepts_before":     targetBefore,
		"target_accepts_after":      targetAfter,
		"recovered_epoch_ir":        irAuth.Epoch,
		"recovered_epoch_ex":        exAuth.Epoch,
		"heap_alloc_before_bytes":  before.HeapAlloc,
		"heap_alloc_after_bytes":   after.HeapAlloc,
		"goroutines_before":        goroutinesBefore,
		"goroutines_after":         goroutinesAfter,
		"fd_before":                fdBefore,
		"fd_after":                 fdAfter,
		"scope":                    "same-process abrupt Carrier cut on existing application TCP flows",
		"failures":                 failures,
	})

	if survived != flows {
		t.Fatalf("session survival %d/%d failures=%v", survived, flows, failures)
	}
	if targetAfter != targetBefore {
		t.Fatalf("target TCP reopened across Carrier recovery before=%d after=%d", targetBefore, targetAfter)
	}
}


// TestStageEMeasureDirectTCPThroughput is the control measurement for B06.
// It uses the same flow count, payload size, warmup, hashing, and timed region,
// but removes BAFT Session and H2/mTLS from the path.
func TestStageEMeasureDirectTCPThroughput(t *testing.T) {
	const flowCount = 8
	bytesPerFlow := stageEIntEnv(t, "BAFT_STAGE_E_BYTES_PER_FLOW", 4*1024*1024, 64*1024, 64*1024*1024)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	netemRTTMS, netemLossPct, cleanupNetem := stageEMaybeApplyNetem(t, ln.Addr())
	defer cleanupNetem()
	clientDeadline := 20 * time.Second
	if netemRTTMS > 0 || netemLossPct > 0 {
		clientDeadline = 4 * time.Minute
	}

	var serverWG sync.WaitGroup
	acceptErr := make(chan error, 1)
	go func() {
		for i := 0; i < flowCount; i++ {
			c, err := ln.Accept()
			if err != nil {
				acceptErr <- err
				return
			}
			serverWG.Add(1)
			go func(c net.Conn) {
				defer serverWG.Done()
				defer c.Close()
				buf := make([]byte, 64*1024)
				for {
					n, rerr := c.Read(buf)
					if n > 0 {
						p := buf[:n]
						for len(p) > 0 {
							w, werr := c.Write(p)
							if werr != nil {
								return
							}
							p = p[w:]
						}
					}
					if rerr != nil {
						if cw, ok := c.(interface{ CloseWrite() error }); ok {
							_ = cw.CloseWrite()
						}
						return
					}
				}
			}(c)
		}
		acceptErr <- nil
	}()

	ready := make(chan error, flowCount)
	startBulk := make(chan struct{})
	clientErr := make(chan error, flowCount)
	var clients sync.WaitGroup

	for i := 0; i < flowCount; i++ {
		i := i
		clients.Add(1)
		go func() {
			defer clients.Done()
			raw, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
			if err != nil {
				ready <- err
				clientErr <- err
				return
			}
			c := raw.(*net.TCPConn)
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(clientDeadline))

			prelude := []byte{0x42, byte(i), 0x45}
			if _, err := c.Write(prelude); err != nil {
				ready <- err
				clientErr <- err
				return
			}
			gotPrelude := make([]byte, len(prelude))
			if _, err := io.ReadFull(c, gotPrelude); err != nil {
				ready <- err
				clientErr <- err
				return
			}
			if !bytes.Equal(gotPrelude, prelude) {
				err := fmt.Errorf("flow %d warmup echo mismatch", i)
				ready <- err
				clientErr <- err
				return
			}

			payload := make([]byte, bytesPerFlow)
			for j := range payload {
				payload[j] = byte((j*17 + i*29) % 251)
			}
			want := sha256.Sum256(payload)
			ready <- nil
			<-startBulk

			writeDone := make(chan error, 1)
			go func() {
				_, err := io.Copy(c, bytes.NewReader(payload))
				if err == nil {
					err = c.CloseWrite()
				}
				writeDone <- err
			}()

			h := sha256.New()
			n, err := io.Copy(h, c)
			if err != nil {
				clientErr <- err
				return
			}
			if err := <-writeDone; err != nil {
				clientErr <- err
				return
			}
			if n != int64(len(payload)) {
				clientErr <- fmt.Errorf("flow %d bytes=%d want=%d", i, n, len(payload))
				return
			}
			if !bytes.Equal(h.Sum(nil), want[:]) {
				clientErr <- fmt.Errorf("flow %d hash mismatch", i)
				return
			}
			clientErr <- nil
		}()
	}

	for i := 0; i < flowCount; i++ {
		if err := <-ready; err != nil {
			t.Fatalf("warmup %d: %v", i, err)
		}
	}

	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	goroutinesBefore := runtime.NumGoroutine()
	fdBefore := stageEFDCount()

	started := time.Now()
	close(startBulk)
	clients.Wait()
	elapsed := time.Since(started)

	for i := 0; i < flowCount; i++ {
		if err := <-clientErr; err != nil {
			t.Fatalf("client %d: %v", i, err)
		}
	}
	if err := <-acceptErr; err != nil {
		t.Fatal(err)
	}
	serverWG.Wait()

	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	fdAfter := stageEFDCount()
	goroutinesAfter := runtime.NumGoroutine()

	txBytes := int64(flowCount * bytesPerFlow)
	seconds := elapsed.Seconds()
	txMbps := float64(txBytes*8) / seconds / 1_000_000
	aggregateMbps := txMbps * 2

	emitStageEMetric(t, map[string]any{
		"scenario":                 "B08",
		"measurement":              "direct_tcp_loopback_throughput",
		"flows":                    flowCount,
		"tx_bytes":                 txBytes,
		"rx_bytes":                 txBytes,
		"elapsed_ms":               float64(elapsed) / float64(time.Millisecond),
		"tx_mbps":                  txMbps,
		"rx_mbps":                  txMbps,
		"aggregate_mbps":           aggregateMbps,
		"heap_alloc_before_bytes":  before.HeapAlloc,
		"heap_alloc_after_bytes":   after.HeapAlloc,
		"goroutines_before":        goroutinesBefore,
		"goroutines_after":         goroutinesAfter,
		"fd_before":                fdBefore,
		"fd_after":                 fdAfter,
		"netem_rtt_ms":             netemRTTMS,
		"netem_loss_percent":       netemLossPct,
		"scope":                    "direct loopback TCP control; same warmed-flow payload profile as B06",
	})
}
