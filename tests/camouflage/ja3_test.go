// Package camouflage measures how distinguishable BAFT's carrier TLS
// ClientHello is from a real browser's. It is measurement only: it captures
// the real ClientHello that internal/identity.ClientTLS produces, computes its
// JA3, and records the concrete distinguishers (GREASE, cipher/extension set).
// This quantifies the gap that a Reality / uTLS carrier would close. It fails
// only if capture itself breaks, so it stays a living record, not a gate.
package camouflage

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/md5"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/hex"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/identity"
)

// A current Chrome ClientHello JA3 (for contrast; browsers also randomise via
// GREASE, so the exact value drifts -- the point is the shape, not equality).
const chromeJA3Example = "771,4865-4866-4867-49195-49199-49196-49200-52393-52392-49171-49172-156-157-47-53,0-23-65281-10-11-35-16-5-13-18-51-45-43-27-17513,29-23-24,0"

func selfSigned(t *testing.T) (*x509.CertPool, tls.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "baft-test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(c)
	return pool, tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: c}
}

// captureClientHello dials a tap that records the first TLS flight and returns
// the raw ClientHello handshake message (TLS record payload, de-framed).
func captureClientHello(t *testing.T, cfg *tls.Config) []byte {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan []byte, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			got <- nil
			return
		}
		defer c.Close()
		buf := make([]byte, 4096)
		n, _ := c.Read(buf)
		got <- buf[:n]
	}()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	tc := tls.Client(conn, cfg)
	_ = tc.HandshakeContext(testDeadlineCtx())
	conn.Close()
	raw := <-got
	if len(raw) < 6 || raw[0] != 0x16 {
		t.Fatalf("did not capture a TLS handshake record: % x", raw[:min(len(raw), 8)])
	}
	recLen := int(binary.BigEndian.Uint16(raw[3:5]))
	if 5+recLen > len(raw) {
		recLen = len(raw) - 5
	}
	return raw[5 : 5+recLen]
}

func TestCarrierClientHelloFingerprint(t *testing.T) {
	pool, cert := selfSigned(t)
	cfg, err := identity.ClientTLS(pool, cert, "example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	hs := captureClientHello(t, cfg)
	ja3, parts, greaseSeen := ja3FromClientHello(t, hs)
	sum := md5.Sum([]byte(ja3))
	t.Logf("BAFT carrier ClientHello JA3 string: %s", ja3)
	t.Logf("BAFT carrier ClientHello JA3 MD5:    %s", hex.EncodeToString(sum[:]))
	t.Logf("parts: version=%s ciphers=%s exts=%s curves=%s", parts[0], parts[1], parts[2], parts[3])
	t.Logf("a real Chrome JA3 (for contrast):    %s", chromeJA3Example)
	t.Logf("GREASE values present (browsers use them): %v", greaseSeen)
	// Record the gap rather than gate on it: this is the status quo the carrier
	// camouflage work will change.
	if ja3 == chromeJA3Example {
		t.Log("NOTE: JA3 already matches the sample browser (unexpected at this stage)")
	} else {
		t.Log("GAP: BAFT carrier ClientHello is distinguishable from a browser (expected; closed by a Reality/uTLS carrier)")
	}
	if !greaseSeen {
		t.Log("GAP: no GREASE in the ClientHello -- a strong, cheap DPI distinguisher from Chrome/Firefox")
	}
}
