package integration_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/recordshape"
	"github.com/zarkmakerburg/baft/internal/securityinternal"
)

type flushWriter struct{ w http.ResponseWriter }

func (f flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if fl, ok := f.w.(http.Flusher); ok {
		fl.Flush()
	}
	return n, err
}

func TestNoiseIKOverHTTP2TerminatingIntermediary(t *testing.T) {
	initKey, err := securityinternal.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	respKey, err := securityinternal.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	psk := bytes.Repeat([]byte{0x42}, 32)
	plaintext := []byte("BAFT-FIRST-BYTE")

	var capturedReq bytes.Buffer
	var capturedResp bytes.Buffer
	var learned []byte
	var mu sync.Mutex
	serverDone := make(chan error, 1)

	proxy := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			http.Error(w, "h2 required", http.StatusHTTPVersionNotSupported)
			serverDone <- nil
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}

		in := io.TeeReader(r.Body, &capturedReq)
		out := io.MultiWriter(flushWriter{w}, &capturedResp)
		secure, peer, err := securityinternal.Responder(in, out, securityinternal.HandshakeConfig{
			Static:            respKey,
			OneTimePairingPSK: psk,
			RecordShaping:     recordshape.DefaultConfig(true),
		})
		if err != nil {
			serverDone <- err
			return
		}
		mu.Lock()
		learned = append([]byte(nil), peer...)
		mu.Unlock()

		buf := make([]byte, 64)
		n, err := secure.Read(buf)
		if err != nil {
			serverDone <- err
			return
		}
		_, err = secure.Write(append([]byte("ok:"), buf[:n]...))
		serverDone <- err
	}))
	proxy.EnableHTTP2 = true
	proxy.StartTLS()
	defer proxy.Close()

	tr := proxy.Client().Transport.(*http.Transport).Clone()
	tr.ForceAttemptHTTP2 = true
	hc := &http.Client{Transport: tr, Timeout: 10 * time.Second}

	reqR, reqW := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, proxy.URL, reqR)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.ProtoMajor != 2 {
		t.Fatalf("expected HTTP/2, got %s", resp.Proto)
	}

	secure, err := securityinternal.Initiator(resp.Body, reqW, securityinternal.HandshakeConfig{
		Static:            initKey,
		PeerStatic:        respKey.Public,
		OneTimePairingPSK: psk,
		RecordShaping:     recordshape.DefaultConfig(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secure.Write(plaintext); err != nil {
		t.Fatal(err)
	}
	out := make([]byte, 128)
	n, err := secure.Read(out)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(out[:n]), "ok:"+string(plaintext); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	_ = reqW.Close()

	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	gotPeer := append([]byte(nil), learned...)
	mu.Unlock()
	if !bytes.Equal(gotPeer, initKey.Public) {
		t.Fatal("responder did not authenticate the initiator static key")
	}
	if strings.Contains(capturedReq.String(), string(plaintext)) ||
		strings.Contains(capturedResp.String(), string(plaintext)) {
		t.Fatal("TLS-terminating intermediary capture contained BAFT application plaintext")
	}
	t.Logf("noise_h2_intermediary_recordshape_ok proto=%s request_capture=%d response_capture=%d",
		resp.Proto, capturedReq.Len(), capturedResp.Len())
}
