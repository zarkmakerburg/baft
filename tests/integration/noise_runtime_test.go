package integration_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/config"
	"github.com/zarkmakerburg/baft/internal/node"
	"github.com/zarkmakerburg/baft/internal/recordshape"
	"github.com/zarkmakerburg/baft/internal/securityinternal"
)

func reserveAddress(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := ln.Addr().String()
	ln.Close()
	return a
}
func TestNoiseMorphingRuntime(t *testing.T) {
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
	ik, _ := securityinternal.GenerateKeyPair()
	rk, _ := securityinternal.GenerateKeyPair()
	ipath, rpath := filepath.Join(dir, "ir-noise.json"), filepath.Join(dir, "ex-noise.json")
	if err := securityinternal.SaveKeyPair(ipath, ik); err != nil {
		t.Fatal(err)
	}
	if err := securityinternal.SaveKeyPair(rpath, rk); err != nil {
		t.Fatal(err)
	}
	ipub, _ := securityinternal.EncodePublicKey(ik.Public)
	rpub, _ := securityinternal.EncodePublicKey(rk.Public)
	ex, err := config.LoadFile("../../configs/example-ex.yaml")
	if err != nil {
		t.Fatal(err)
	}
	ir, err := config.LoadFile("../../configs/example-ir.yaml")
	if err != nil {
		t.Fatal(err)
	}
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		for {
			c, err := target.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	ex.Server.Listen = reserveAddress(t)
	ex.Server.ServerName = "ex.test"
	ex.Management.MetricsListen = reserveAddress(t)
	ir.Peer.Address = ex.Server.Listen
	ir.Peer.ServerName = "ex.test"
	ir.Management.MetricsListen = reserveAddress(t)
	ir.Transport.Shards = 1
	ex.Transport.Shards = 1
	ex.TLS.CAFile = ca
	ex.TLS.CertFile = cert
	ex.TLS.KeyFile = tlsKey
	ir.TLS = ex.TLS
	ex.Routes[0].Target = target.Addr().String()
	ir.Routes[0].Listen = reserveAddress(t)
	shape := recordshape.DefaultConfig(true)
	ex.Noise = &config.Noise{KeyFile: rpath, PeerPublicKey: ipub, RecordShaping: shape}
	ir.Noise = &config.Noise{KeyFile: ipath, PeerPublicKey: rpub, RecordShaping: shape}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	er, irun := node.NewRuntime(), node.NewRuntime()
	done := make(chan error, 2)
	go func() { done <- er.Run(ctx, ex) }()
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: certs.roots, ServerName: "ex.test", MinVersion: tls.VersionTLS13}, ForceAttemptHTTP2: true}
	hc := &http.Client{Transport: transport, Timeout: time.Second}
	defer transport.CloseIdleConnections()
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err := hc.Get("https://" + ex.Server.Listen + "/")
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if !bytes.Contains(b, []byte("<html")) {
				t.Fatal("missing cover")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("listener not ready", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	go func() { done <- irun.Run(ctx, ir) }()
	var conn net.Conn
	deadline = time.Now().Add(3 * time.Second)
	for {
		conn, err = net.DialTimeout("tcp", ir.Routes[0].Listen, 100*time.Millisecond)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("dialer not ready", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	payload := bytes.Repeat([]byte("runtime-noise-morphing"), 4096)
	writeDone := make(chan error, 1)
	go func() {
		_, err := conn.Write(payload)
		if err == nil {
			err = conn.(*net.TCPConn).CloseWrite()
		}
		writeDone <- err
	}()
	got, err := io.ReadAll(conn)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatal("runtime roundtrip mismatch", err, len(got))
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	cancel()
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(6 * time.Second):
			t.Fatal("runtime shutdown timed out")
		}
	}
	if snap := irun.Resources.Snapshot(); snap.TotalUsed != 0 {
		t.Fatalf("dialer resources leaked: %+v", snap)
	}
	if snap := er.Resources.Snapshot(); snap.TotalUsed != 0 {
		t.Fatalf("listener resources leaked: %+v", snap)
	}
}
