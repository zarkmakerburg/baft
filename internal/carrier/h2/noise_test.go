package h2

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/identity"
	"github.com/zarkmakerburg/baft/internal/recordshape"
	"github.com/zarkmakerburg/baft/internal/securityinternal"
)

func TestNoiseHTMLFallbackAndAuthenticatedHTTP2(t *testing.T) {
	ik, err := securityinternal.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	rk, err := securityinternal.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	shape := recordshape.DefaultConfig(true)
	cfg := securityinternal.HandshakeConfig{Static: rk, PeerStatic: ik.Public, RecordShaping: shape}
	revoked := identity.NewRevocationSet()
	var calls atomic.Int32
	h, err := HandlerWithNoise(func(ctx context.Context, in io.Reader, out io.Writer, p PeerInfo) error {
		calls.Add(1)
		if p.Identity != "urn:baft:node:ir" {
			t.Error("identity mapping")
		}
		b := make([]byte, 5)
		if _, err := io.ReadFull(in, b); err != nil {
			return err
		}
		_, err := out.Write(b)
		return err
	}, NoiseOptions{Handshake: cfg, PeerIdentity: "urn:baft:node:ir", Revocations: revoked, HandshakeTimeout: 100 * time.Millisecond, MaxPending: 2})
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewUnstartedServer(h)
	s.EnableHTTP2 = true
	s.StartTLS()
	defer s.Close()
	tr := s.Client().Transport.(*http.Transport).Clone()
	tr.ForceAttemptHTTP2 = true
	hc := &http.Client{Transport: tr, Timeout: 2 * time.Second}
	defer tr.CloseIdleConnections()
	var baseline string
	for i, p := range []struct {
		method, path, ct string
		body             []byte
	}{{"GET", "/", "", nil}, {"POST", CarrierPath, "text/plain", []byte("scan")}, {"POST", CarrierPath, "application/octet-stream", []byte{0, 0}}, {"POST", CarrierPath, "application/octet-stream", append([]byte{0, 96}, bytes.Repeat([]byte{1}, 96)...)}, {"GET", CarrierPath, "", nil}} {
		req, _ := http.NewRequest(p.method, s.URL+p.path, bytes.NewReader(p.body))
		req.Header.Set("Content-Type", p.ct)
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/html; charset=utf-8" {
			t.Fatal(resp.Status, resp.Header)
		}
		if i == 0 {
			baseline = string(b)
		} else if string(b) != baseline {
			t.Fatal("probe response differs from ordinary site")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("probe reached authenticated stream")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, err := NewClient(s.URL, tr.TLSClientConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	ic := securityinternal.HandshakeConfig{Static: ik, PeerStatic: rk.Public, RecordShaping: shape}
	resp, pw, conn, err := client.OpenNoise(ctx, ic)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 5)
	if _, err := io.ReadFull(conn, b); err != nil || string(b) != "hello" {
		t.Fatal(err, string(b))
	}
	pw.Close()
	resp.Body.Close()
	if calls.Load() != 1 {
		t.Fatal("valid stream not called")
	}
	// Unknown static key must receive HTML, not a Noise response/session.
	bad, _ := securityinternal.GenerateKeyPair()
	ic.Static = bad
	if _, _, _, err := client.OpenNoise(ctx, ic); err == nil {
		t.Fatal("unknown Noise peer accepted")
	}
	revoked.RevokeIdentity("urn:baft:node:ir")
	ic.Static = ik
	if _, _, _, err := client.OpenNoise(ctx, ic); err == nil {
		t.Fatal("revoked peer accepted")
	}
	if calls.Load() != 1 {
		t.Fatal("rejected identity reached stream")
	}
	// HTTP/1.1 requests also see the website through ordinary HTTPS.
	h1TLS := tr.TLSClientConfig.Clone()
	h1TLS.NextProtos = []string{"http/1.1"}
	h1 := &http.Client{Transport: &http.Transport{TLSClientConfig: h1TLS, TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{}}, Timeout: time.Second}
	r1, err := h1.Get(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(r1.Body)
	r1.Body.Close()
	h1.CloseIdleConnections()
	if string(body) != baseline {
		t.Fatal("HTTP/1 site differs")
	}
}
func TestNoiseSlowProbeBounded(t *testing.T) {
	ik, _ := securityinternal.GenerateKeyPair()
	rk, _ := securityinternal.GenerateKeyPair()
	phaseCh := make(chan string, 1)
	h, err := HandlerWithNoise(nil, NoiseOptions{
		Handshake: securityinternal.HandshakeConfig{Static: rk, PeerStatic: ik.Public},
		PeerIdentity: "test", HandshakeTimeout: 20 * time.Millisecond, MaxPending: 1,
		OnHandshakeFailure: func(phase string) { phaseCh <- phase },
	})
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewUnstartedServer(h)
	s.EnableHTTP2 = true
	s.StartTLS()
	defer s.Close()
	tr := s.Client().Transport.(*http.Transport).Clone()
	tr.ForceAttemptHTTP2 = true
	hc := &http.Client{Transport: tr, Timeout: time.Second}
	defer tr.CloseIdleConnections()
	pr, pw := io.Pipe()
	defer pw.Close()
	req, _ := http.NewRequest("POST", s.URL+CarrierPath, pr)
	req.Header.Set("Content-Type", "application/octet-stream")
	start := time.Now()
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if time.Since(start) > time.Second {
		t.Fatal("slow handshake unbounded")
	}
	if resp.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatal(resp.Header)
	}
	select {
	case phase := <-phaseCh:
		if phase != "noise_timeout" {
			t.Fatalf("failure phase=%q, want noise_timeout", phase)
		}
	case <-time.After(time.Second):
		t.Fatal("missing phase-aware server failure callback")
	}
}

func TestOpenNoiseReportsTLSPhase(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan struct{})
	go func() {
		conn, _ := ln.Accept()
		if conn != nil {
			_ = conn.Close()
		}
		close(accepted)
	}()

	client, err := NewClient("https://"+ln.Addr().String(), &tls.Config{
		MinVersion: tls.VersionTLS13, ServerName: "field-test.invalid", NextProtos: []string{"h2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	ik, _ := securityinternal.GenerateKeyPair()
	rk, _ := securityinternal.GenerateKeyPair()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _, _, err = client.OpenNoise(ctx, securityinternal.HandshakeConfig{Static: ik, PeerStatic: rk.Public})
	if err == nil {
		t.Fatal("TLS failure unexpectedly succeeded")
	}
	got := err.Error()
	if !strings.Contains(got, "phase=tls_handshake_error") {
		t.Fatalf("error missing TLS phase: %v", err)
	}
	if !strings.Contains(got, "tcp_connect_complete@") || !strings.Contains(got, "tls_handshake_error@") {
		t.Fatalf("error missing phase timeline: %v", err)
	}
	if strings.Contains(got, ln.Addr().String()) || strings.Contains(got, "https://") {
		t.Fatalf("phase error leaked endpoint metadata: %v", err)
	}
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("server did not accept test connection")
	}
}

func TestNoisePhaseTraceSanitizedErrorPreservesCause(t *testing.T) {
	op := &net.OpError{Op: "read", Net: "tcp",
		Addr: &net.TCPAddr{IP: net.ParseIP("192.0.2.17"), Port: 18443},
		Err:  io.EOF,
	}
	original := &url.Error{Op: "Post", URL: "https://private.example:18443/baft/v1/carrier", Err: op}
	trace := newNoisePhaseTrace()
	trace.note("tls_handshake_error", "")
	wrapped := trace.wrap(original, false, true)
	if !errors.Is(wrapped, io.EOF) {
		t.Fatalf("transport cause lost: %v", wrapped)
	}
	var got *net.OpError
	if !errors.As(wrapped, &got) || got != op {
		t.Fatal("typed transport error identity lost")
	}
	for _, secret := range []string{"private.example", "192.0.2.17", "18443", "https://"} {
		if strings.Contains(wrapped.Error(), secret) {
			t.Fatalf("sanitized diagnostic leaked endpoint metadata: %v", wrapped)
		}
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		if !errors.Is(trace.wrap(cause, false, true), cause) {
			t.Fatalf("context cause lost: %v", cause)
		}
	}
	if !errors.Is(trace.wrap(context.Canceled, true, true), context.DeadlineExceeded) {
		t.Fatal("internal handshake timeout did not retain deadline semantics")
	}
}
