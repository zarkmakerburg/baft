package utlsdial

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"

	utls "github.com/refraction-networking/utls"
)

// selfSigned makes a TLS 1.3 server certificate of the given key type.
func selfSigned(t *testing.T, keyType string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	var pub crypto.PublicKey
	var priv crypto.Signer
	var err error
	switch keyType {
	case "ecdsa":
		var k *ecdsa.PrivateKey
		k, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if k != nil {
			pub, priv = &k.PublicKey, k
		}
	default:
		var p ed25519.PrivateKey
		pub, p, err = ed25519.GenerateKey(rand.Reader)
		priv = p
	}
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"example.com"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	c, _ := x509.ParseCertificate(der)
	pool.AddCert(c)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv, Leaf: c}, pool
}

// serveOneTLS13 accepts a single TLS 1.3 connection and closes it.
func serveOneTLS13(t *testing.T, cert tls.Certificate) net.Listener {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{"http/1.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_ = c.(*tls.Conn).Handshake()
				time.Sleep(50 * time.Millisecond)
				_ = c.Close()
			}()
		}
	}()
	return ln
}

// TestUTLSNegotiatesTLS13ECDSA guards the regression that the default Chrome
// profile must negotiate with a TLS 1.3-only server holding an ECDSA cert
// (a Cloudflare Origin Certificate is ECDSA/RSA).
func TestUTLSNegotiatesTLS13ECDSA(t *testing.T) {
	cert, pool := selfSigned(t, "ecdsa")
	ln := serveOneTLS13(t, cert)
	defer ln.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := Dial(ctx, "tcp", ln.Addr().String(), Config{
		ServerName: "example.com", RootCAs: pool, NextProtos: []string{"http/1.1"},
	})
	if err != nil {
		t.Fatalf("uTLS should negotiate with a TLS1.3 ECDSA server: %v", err)
	}
	conn.Close()
}

// TestUTLSFailsTLS13Ed25519 documents that a browser-fidelity ClientHello
// cannot use an Ed25519 server certificate (browsers do not advertise the
// ed25519 signature algorithm), so uTLS requires an ECDSA/RSA cert.
func TestUTLSFailsTLS13Ed25519(t *testing.T) {
	cert, pool := selfSigned(t, "ed25519")
	ln := serveOneTLS13(t, cert)
	defer ln.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := Dial(ctx, "tcp", ln.Addr().String(), Config{
		ServerName: "example.com", RootCAs: pool, NextProtos: []string{"http/1.1"},
	}); err == nil {
		t.Fatal("expected uTLS to fail against an Ed25519 server certificate")
	}
}

func TestUTLSHonorsExplicitALPNOverChromePreset(t *testing.T) {
	cert, pool := selfSigned(t, "ecdsa")
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{"h2", "http/1.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.(*tls.Conn).Handshake()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := Dial(ctx, "tcp", ln.Addr().String(), Config{
		ServerName: "example.com", RootCAs: pool, NextProtos: []string{"http/1.1"},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	uconn, ok := conn.(*utls.UConn)
	if !ok {
		t.Fatalf("conn type %T, want *utls.UConn", conn)
	}
	if got := uconn.ConnectionState().NegotiatedProtocol; got != "http/1.1" {
		t.Fatalf("negotiated ALPN=%q, want http/1.1; Chrome preset overrode requested protocol", got)
	}
}
