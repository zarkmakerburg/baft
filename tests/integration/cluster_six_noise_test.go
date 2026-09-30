package integration_test

// Identity-stable topology gate: N=6 remains a historical regression fixture; semantic identity is keyed by stable NodeID/RouteID.

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/cluster"
	"github.com/zarkmakerburg/baft/internal/config"
	"github.com/zarkmakerburg/baft/internal/node"
	"github.com/zarkmakerburg/baft/internal/recordshape"
	"github.com/zarkmakerburg/baft/internal/securityinternal"
)

func waitTCP(t *testing.T, addr string, deadline time.Time) {
	t.Helper()
	for {
		c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("listener %s not ready: %v", addr, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type ownedListenerProvider struct {
	mu        sync.Mutex
	listeners map[string]net.Listener
}

func newOwnedListenerProvider() *ownedListenerProvider {
	return &ownedListenerProvider{listeners: map[string]net.Listener{}}
}

func listenerKey(kind node.EndpointKind, name string) string {
	return string(kind) + "|" + name
}

func (p *ownedListenerProvider) add(t *testing.T, kind node.EndpointKind, name string, ln net.Listener) {
	t.Helper()
	if ln == nil {
		t.Fatal("cannot add nil listener")
	}
	k := listenerKey(kind, name)
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.listeners[k]; exists {
		t.Fatalf("duplicate fixture listener key %s", k)
	}
	p.listeners[k] = ln
}

func (p *ownedListenerProvider) Listener(kind node.EndpointKind, name, configured string) (net.Listener, error) {
	k := listenerKey(kind, name)
	p.mu.Lock()
	ln := p.listeners[k]
	if ln != nil {
		delete(p.listeners, k)
	}
	p.mu.Unlock()
	if ln == nil {
		return nil, fmt.Errorf("fixture listener not found kind=%s name=%s", kind, name)
	}
	if got := ln.Addr().String(); got != configured {
		_ = ln.Close()
		return nil, fmt.Errorf("fixture listener address mismatch kind=%s name=%s got=%s want=%s", kind, name, got, configured)
	}
	return ln, nil
}

func (p *ownedListenerProvider) closeRemaining() {
	p.mu.Lock()
	listeners := make([]net.Listener, 0, len(p.listeners))
	for _, ln := range p.listeners {
		listeners = append(listeners, ln)
	}
	p.listeners = map[string]net.Listener{}
	p.mu.Unlock()
	for _, ln := range listeners {
		_ = ln.Close()
	}
}

func bindFixtureTCP(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

func runNNodeNoiseMasterHandshakeRoundTrip(t *testing.T, n int) {
	t.Helper()
	if n < 1 {
		t.Fatalf("N must be >= 1, got %d", n)
	}

	certs := testPKI(t)
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ca := write("ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certs.caDER}))
	cert := write("server.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certs.server.Certificate[0]}))
	der, err := x509.MarshalPKCS8PrivateKey(certs.server.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	tlsKey := write("server.key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	listenerDone := make(chan error, n)
	targets := make([]net.Listener, 0, n)
	foreignCfgs := make([]config.Config, 0, n)
	masterCfgs := make([]config.Config, 0, n)
	foreignProviders := make([]*ownedListenerProvider, 0, n)
	masterProviders := make([]*ownedListenerProvider, 0, n)

	defer func() {
		for _, p := range foreignProviders {
			p.closeRemaining()
		}
		for _, p := range masterProviders {
			p.closeRemaining()
		}
		for _, ln := range targets {
			_ = ln.Close()
		}
	}()

	for i := 0; i < n; i++ {
		target := bindFixtureTCP(t)
		targets = append(targets, target)
		go func(ln net.Listener) {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go func(conn net.Conn) {
					defer conn.Close()
					_, _ = io.Copy(conn, conn)
				}(c)
			}
		}(target)

		ex, err := config.LoadFile("../../configs/example-ex.yaml")
		if err != nil {
			t.Fatal(err)
		}
		ir, err := config.LoadFile("../../configs/example-ir.yaml")
		if err != nil {
			t.Fatal(err)
		}

		exID := fmt.Sprintf("ex-%02d", i+1)
		exProvider := newOwnedListenerProvider()
		irProvider := newOwnedListenerProvider()

		exServerLn := bindFixtureTCP(t)
		exMetricsLn := bindFixtureTCP(t)
		irMetricsLn := bindFixtureTCP(t)
		routeLn := bindFixtureTCP(t)

		exProvider.add(t, node.EndpointServer, exID, exServerLn)
		exProvider.add(t, node.EndpointMetrics, "metrics", exMetricsLn)
		irProvider.add(t, node.EndpointMetrics, "metrics", irMetricsLn)
		irProvider.add(t, node.EndpointRoute, ir.Routes[0].ID, routeLn)

		ex.Node.ID = exID
		ex.Server.Listen = exServerLn.Addr().String()
		ex.Server.ServerName = "ex.test"
		ex.Server.AllowedPeerIdentities = []string{"urn:baft:node:ir-01"}
		ex.Management.UnixSocket = filepath.Join(dir, fmt.Sprintf("baft-ex-%02d.sock", i+1))
		ex.Management.MetricsListen = exMetricsLn.Addr().String()
		ex.Transport.Shards = 1
		ex.Routes[0].Target = target.Addr().String()
		ex.TLS.CAFile = ca
		ex.TLS.CertFile = cert
		ex.TLS.KeyFile = tlsKey

		ir.Node.ID = "ir-01"
		ir.Peer.Address = ex.Server.Listen
		ir.Peer.ServerName = "ex.test"
		ir.Peer.AllowedIdentity = "urn:baft:node:" + exID
		ir.Management.UnixSocket = filepath.Join(dir, fmt.Sprintf("baft-ir-%02d.sock", i+1))
		ir.Management.MetricsListen = irMetricsLn.Addr().String()
		ir.Transport.Shards = 1
		ir.Routes[0].Listen = routeLn.Addr().String()
		ir.TLS = ex.TLS

		ik, err := securityinternal.GenerateKeyPair()
		if err != nil {
			t.Fatal(err)
		}
		rk, err := securityinternal.GenerateKeyPair()
		if err != nil {
			t.Fatal(err)
		}
		ipath := filepath.Join(dir, fmt.Sprintf("ir-noise-%02d.json", i+1))
		rpath := filepath.Join(dir, fmt.Sprintf("ex-noise-%02d.json", i+1))
		if err := securityinternal.SaveKeyPair(ipath, ik); err != nil {
			t.Fatal(err)
		}
		if err := securityinternal.SaveKeyPair(rpath, rk); err != nil {
			t.Fatal(err)
		}
		ipub, err := securityinternal.EncodePublicKey(ik.Public)
		if err != nil {
			t.Fatal(err)
		}
		rpub, err := securityinternal.EncodePublicKey(rk.Public)
		if err != nil {
			t.Fatal(err)
		}

		// This fixture verifies Noise/session/cluster wiring. Record shaping is
		// intentionally disabled; its independent regression suite is unchanged.
		shape := recordshape.Config{}
		ex.Noise = &config.Noise{KeyFile: rpath, PeerPublicKey: ipub, RecordShaping: shape}
		ir.Noise = &config.Noise{KeyFile: ipath, PeerPublicKey: rpub, RecordShaping: shape}

		foreignCfgs = append(foreignCfgs, ex)
		masterCfgs = append(masterCfgs, ir)
		foreignProviders = append(foreignProviders, exProvider)
		masterProviders = append(masterProviders, irProvider)
	}

	for i := range foreignCfgs {
		cfg := foreignCfgs[i]
		rt := node.NewRuntime()
		rt.SetListenerProviderForTest(foreignProviders[i])
		go func() { listenerDone <- rt.Run(ctx, cfg) }()
		select {
		case <-rt.ListenerReadyForTest():
			actual, startErr := rt.ListenerReadinessForTest()
			if startErr != nil {
				t.Fatalf("N=%d listener %s startup failed: %v", n, cfg.Server.Listen, startErr)
			}
			if actual != cfg.Server.Listen {
				t.Fatalf("N=%d listener bound unexpected address got=%s want=%s", n, actual, cfg.Server.Listen)
			}
		case <-time.After(12 * time.Second):
			t.Fatalf("N=%d listener %s readiness condition not reached", n, cfg.Server.Listen)
		}
	}

	masterDone := make(chan error, 1)
	flowOpenErr := make(chan string, n*2)
	master := cluster.NewMaster()
	nextMasterRuntime := 0
	master.SetRuntimeFactoryForTest(func() *node.Runtime {
		rt := node.NewRuntime()
		if nextMasterRuntime >= len(masterProviders) {
			t.Fatalf("runtime factory requested %d runtimes for N=%d", nextMasterRuntime+1, n)
		}
		idx:=nextMasterRuntime
		rt.SetListenerProviderForTest(masterProviders[idx])
		rt.SetFlowOpenErrorHookForTest(func(routeID string,err error){
			select{case flowOpenErr<-fmt.Sprintf("node=%d route=%s err=%v",idx+1,routeID,err):default:}
		})
		nextMasterRuntime++
		return rt
	})
	go func() { masterDone <- master.Run(ctx, masterCfgs) }()
	select {
	case <-master.ReadyForTest():
		if err := master.ReadinessForTest(); err != nil {
			t.Fatalf("N=%d master startup failed before route readiness: %v", n, err)
		}
	case err := <-masterDone:
		if err == nil {
			t.Fatalf("N=%d master stopped before route readiness", n)
		}
		t.Fatalf("N=%d master startup failed: %v", n, err)
	case <-time.After(12 * time.Second):
		t.Fatalf("N=%d master route readiness condition not reached", n)
	}
	if nextMasterRuntime != n {
		t.Fatalf("runtime factory count mismatch got=%d want=%d", nextMasterRuntime, n)
	}

	for i, cfg := range masterCfgs {
		conn, err := net.DialTimeout("tcp", cfg.Routes[0].Listen, time.Second)
		if err != nil {
			select{
			case masterErr:=<-masterDone:
				t.Fatalf("N=%d route=%d dial: %v; master=%v",n,i+1,err,masterErr)
			default:
				t.Fatalf("N=%d route=%d dial: %v; master=running",n,i+1,err)
			}
		}
		payload := bytes.Repeat([]byte(fmt.Sprintf("route-%02d-noise-ok|", i+1)), 128)
		if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			_ = conn.Close()
			t.Fatal(err)
		}
		if _, err := conn.Write(payload); err != nil {
			_ = conn.Close()
			t.Fatalf("N=%d route=%d write: %v", n, i+1, err)
		}
		got := make([]byte, len(payload))
		if _, err := io.ReadFull(conn, got); err != nil {
			_ = conn.Close()
			cause:="no OpenFlow error captured"
			select{case cause=<-flowOpenErr:default:}
			select{
			case masterErr:=<-masterDone:
				t.Fatalf("N=%d route=%d read: %v; flow=%s; master=%v",n,i+1,err,cause,masterErr)
			default:
				t.Fatalf("N=%d route=%d read: %v; flow=%s; master=running",n,i+1,err,cause)
			}
		}
		_ = conn.Close()
		if !bytes.Equal(got, payload) {
			t.Fatalf("N=%d route=%d roundtrip mismatch", n, i+1)
		}
		t.Logf("PASS N=%d route=%02d peer=%s noise_ik=ok roundtrip_bytes=%d", n, i+1, cfg.Peer.Address, len(payload))
	}

	cancel()
	select {
	case err := <-masterDone:
		if err != nil {
			t.Fatalf("N=%d master shutdown: %v", n, err)
		}
	case <-time.After(8 * time.Second):
		t.Fatalf("N=%d master shutdown timed out", n)
	}
	for i := 0; i < n; i++ {
		select {
		case err := <-listenerDone:
			if err != nil {
				t.Fatalf("N=%d foreign listener shutdown: %v", n, err)
			}
		case <-time.After(8 * time.Second):
			t.Fatalf("N=%d foreign listener shutdown timed out", n)
		}
	}
}

// N=6 is retained as a historical regression fixture, not an architecture limit.
func TestSixNodeNoiseMasterHandshakeRoundTrip(t *testing.T) {
	runNNodeNoiseMasterHandshakeRoundTrip(t, 6)
}

func TestNNodeNoiseMasterHandshakeRoundTrip(t *testing.T) {
	for _, n := range []int{1, 2, 3, 6, 8, 16} {
		n := n
		t.Run(fmt.Sprintf("N=%d", n), func(t *testing.T) {
			runNNodeNoiseMasterHandshakeRoundTrip(t, n)
		})
	}
}

func TestNNodeNoiseMasterHandshakeRandomizedCardinality(t *testing.T) {
	const seed int64 = 57016
	rng := rand.New(rand.NewSource(seed))
	seen := map[int]struct{}{}
	values := make([]int, 0, 4)
	for len(values) < cap(values) {
		n := 1 + rng.Intn(16)
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		values = append(values, n)
	}
	for _, n := range values {
		n := n
		t.Run(fmt.Sprintf("seed=%d/N=%d", seed, n), func(t *testing.T) {
			runNNodeNoiseMasterHandshakeRoundTrip(t, n)
		})
	}
}
