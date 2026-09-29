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

func TestSixNodeNoiseMasterHandshakeRoundTrip(t *testing.T) {
	const regressionNodeCount = 6
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

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	// Keep every released ephemeral address distinct inside this fixture. This
	// removes self-collision between server, metrics and local-route endpoints;
	// readiness itself is still event-driven below.
	usedAddrs:=map[string]struct{}{}
	reserveUnique:=func()string{
		for{
			a:=reserveAddress(t)
			if _,exists:=usedAddrs[a];exists{continue}
			usedAddrs[a]=struct{}{}
			return a
		}
	}

	listenerDone := make(chan error, regressionNodeCount)
	var listeners []*net.TCPListener
	var foreignMetrics []net.Listener
	var masterMetrics []net.Listener
	masterCfgs := make([]config.Config, 0, regressionNodeCount)
	foreignCfgs := make([]config.Config, 0, regressionNodeCount)
	defer func(){
		for _,ln:=range foreignMetrics{_ = ln.Close()}
		for _,ln:=range masterMetrics{_ = ln.Close()}
	}()

	for i := 0; i < regressionNodeCount; i++ {
		target, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listeners = append(listeners, target.(*net.TCPListener))
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

		exMetricsLn,err:=net.Listen("tcp","127.0.0.1:0")
		if err!=nil{t.Fatal(err)}
		irMetricsLn,err:=net.Listen("tcp","127.0.0.1:0")
		if err!=nil{_ = exMetricsLn.Close();t.Fatal(err)}
		foreignMetrics=append(foreignMetrics,exMetricsLn)
		masterMetrics=append(masterMetrics,irMetricsLn)

		exID := fmt.Sprintf("ex-%02d", i+1)
		ex.Node.ID = exID
		ex.Server.Listen = reserveUnique()
		ex.Server.ServerName = "ex.test"
		ex.Server.AllowedPeerIdentities = []string{"urn:baft:node:ir-01"}
		ex.Management.UnixSocket = fmt.Sprintf("/tmp/baft-ex-%02d.sock", i+1)
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
		ir.Management.UnixSocket = fmt.Sprintf("/tmp/baft-ir-%02d.sock", i+1)
		ir.Management.MetricsListen = irMetricsLn.Addr().String()
		ir.Transport.Shards = 1
		ir.Routes[0].Listen = reserveUnique()
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

		// Keep shaping disabled in this cluster handshake test. The existing
		// recordshape statistical suite remains the separate regression gate.
		shape := recordshape.Config{}
		ex.Noise = &config.Noise{KeyFile: rpath, PeerPublicKey: ipub, RecordShaping: shape}
		ir.Noise = &config.Noise{KeyFile: ipath, PeerPublicKey: rpub, RecordShaping: shape}

		foreignCfgs = append(foreignCfgs, ex)
		masterCfgs = append(masterCfgs, ir)
	}

	defer func() {
		for _, ln := range listeners {
			_ = ln.Close()
		}
	}()

	for i := range foreignCfgs {
		cfg := foreignCfgs[i]
		rt := node.NewRuntime()
		rt.SetMetricsListenerForTest(foreignMetrics[i])
		go func() { listenerDone <- rt.Run(ctx, cfg) }()
		// Wait on the Runtime's post-bind readiness condition, not repeated
		// connection attempts against a listener that may not exist yet.
		select {
		case <-rt.ListenerReadyForTest():
			actual,startErr:=rt.ListenerReadinessForTest()
			if startErr!=nil{t.Fatalf("listener %s startup failed: %v",cfg.Server.Listen,startErr)}
			if actual!=cfg.Server.Listen{t.Fatalf("listener bound unexpected address got=%s want=%s",actual,cfg.Server.Listen)}
		case <-time.After(12*time.Second):
			t.Fatalf("listener %s readiness condition not reached",cfg.Server.Listen)
		}
	}

	masterDone := make(chan error, 1)
	master:=cluster.NewMaster()
	nextMasterRuntime:=0
	master.SetRuntimeFactoryForTest(func()*node.Runtime{
		rt:=node.NewRuntime()
		if nextMasterRuntime<len(masterMetrics){
			rt.SetMetricsListenerForTest(masterMetrics[nextMasterRuntime])
			nextMasterRuntime++
		}
		return rt
	})
	go func() { masterDone <- master.Run(ctx, masterCfgs) }()
	select{
	case <-master.ReadyForTest():
		if err:=master.ReadinessForTest();err!=nil{t.Fatalf("master startup failed before route readiness: %v",err)}
	case err:=<-masterDone:
		if err==nil{t.Fatal("master stopped before route readiness")}
		t.Fatalf("master startup failed: %v",err)
	case <-time.After(8*time.Second):
		t.Fatal("master route readiness condition not reached")
	}

	for i, cfg := range masterCfgs {
		conn, err := net.DialTimeout("tcp", cfg.Routes[0].Listen, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		payload := bytes.Repeat([]byte(fmt.Sprintf("route-%02d-noise-ok|", i+1)), 128)
		if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			_ = conn.Close()
			t.Fatal(err)
		}
		if _, err := conn.Write(payload); err != nil {
			_ = conn.Close()
			t.Fatal(err)
		}
		got := make([]byte, len(payload))
		if _, err := io.ReadFull(conn, got); err != nil {
			_ = conn.Close()
			t.Fatal(err)
		}
		_ = conn.Close()
		if !bytes.Equal(got, payload) {
			t.Fatalf("route %d roundtrip mismatch", i+1)
		}
		t.Logf("PASS route=%02d peer=%s noise_ik=ok roundtrip_bytes=%d", i+1, cfg.Peer.Address, len(payload))
	}

	cancel()
	select {
	case err := <-masterDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("master shutdown timed out")
	}
	for i := 0; i < regressionNodeCount; i++ {
		select {
		case err := <-listenerDone:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(8 * time.Second):
			t.Fatal("foreign listener shutdown timed out")
		}
	}
}
