package utlsdial

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	utls "github.com/refraction-networking/utls"
)

func serverPool(t *testing.T, s *httptest.Server) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(s.Certificate())
	return pool
}

func TestUTLSDialAuthenticatesServerAndSpeaksHTTP(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
	}))
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := Dial(ctx, "tcp", s.Listener.Addr().String(), Config{
		ServerName: "example.com",
		RootCAs:    serverPool(t, s),
		NextProtos: []string{"http/1.1"},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	// Drive one HTTP/1.1 request over the authenticated connection.
	_, err = conn.Write([]byte("GET / HTTP/1.1\r\nHost: example.com\r\nConnection: close\r\n\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

// TestUTLSDialRejectsUntrustedServer proves the parroted dial still verifies the
// server: an empty root pool must make the handshake fail.
func TestUTLSDialRejectsUntrustedServer(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Dial(ctx, "tcp", s.Listener.Addr().String(), Config{
		ServerName: "example.com",
		RootCAs:    x509.NewCertPool(), // trusts nothing
		NextProtos: []string{"http/1.1"},
	})
	if err == nil {
		t.Fatal("dial accepted an untrusted server certificate")
	}
}

// TestUTLSDialRejectsBadServerName proves SNI/hostname verification is enforced.
func TestUTLSDialRejectsBadServerName(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Dial(ctx, "tcp", s.Listener.Addr().String(), Config{
		ServerName: "not-in-cert.example.net",
		RootCAs:    serverPool(t, s),
		NextProtos: []string{"http/1.1"},
	})
	if err == nil {
		t.Fatal("dial accepted a certificate that does not match the server name")
	}
}

func TestUTLSDialPinEnforced(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Pin that always rejects -> dial fails even with a valid chain.
	_, err := Dial(ctx, "tcp", s.Listener.Addr().String(), Config{
		ServerName: "example.com",
		RootCAs:    serverPool(t, s),
		NextProtos: []string{"http/1.1"},
		Pin:        func(leaf *x509.Certificate) error { return errors.New("nope") },
	})
	if err == nil || !strings.Contains(err.Error(), "pin") {
		t.Fatalf("expected pin rejection, got %v", err)
	}

	// Pin that accepts the real leaf -> dial succeeds.
	conn, err := Dial(ctx, "tcp", s.Listener.Addr().String(), Config{
		ServerName: "example.com",
		RootCAs:    serverPool(t, s),
		NextProtos: []string{"http/1.1"},
		Pin:        func(leaf *x509.Certificate) error { return nil },
	})
	if err != nil {
		t.Fatalf("pin-accept dial: %v", err)
	}
	conn.Close()
}

func TestCaptureClientHelloNonEmpty(t *testing.T) {
	raw, err := CaptureClientHello(Config{ServerName: "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 40 || raw[0] != 0x01 {
		t.Fatalf("not a ClientHello (len=%d first=%#x)", len(raw), func() byte {
			if len(raw) > 0 {
				return raw[0]
			}
			return 0
		}())
	}
}

func TestUTLSDialExplicitALPNOverridesBrowserPreset(t *testing.T) {
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	s.EnableHTTP2 = true
	s.TLS = &tls.Config{NextProtos: []string{"h2", "http/1.1"}}
	s.StartTLS()
	defer s.Close()

	for _, tc := range []struct {
		name string
		want string
	}{
		{name: "websocket-http1", want: "http/1.1"},
		{name: "http2", want: "h2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, err := Dial(ctx, "tcp", s.Listener.Addr().String(), Config{
				ServerName: "example.com",
				RootCAs:    serverPool(t, s),
				NextProtos: []string{tc.want},
				Hello:      utls.HelloChrome_120,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			got := conn.(*utls.UConn).ConnectionState().NegotiatedProtocol
			if got != tc.want {
				t.Fatalf("negotiated ALPN %q, want %q", got, tc.want)
			}
		})
	}
}
