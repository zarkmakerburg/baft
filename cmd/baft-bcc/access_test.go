package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAccessCommands(t *testing.T) {
	file := filepath.Join(t.TempDir(), "access.json")
	if code := runAccess([]string{"show", "--access-file", file}); code != 1 {
		t.Fatalf("show without a file = %d", code)
	}
	if code := runAccess([]string{"init", "--access-file", file}); code != 0 {
		t.Fatalf("init = %d", code)
	}
	if code := runAccess([]string{"init", "--access-file", file}); code != 1 {
		t.Fatalf("second init = %d, want refusal", code)
	}
	for _, sub := range []string{"show", "regenerate"} {
		if code := runAccess([]string{sub, "--access-file", file}); code != 0 {
			t.Fatalf("%s = %d", sub, code)
		}
	}
	if code := runAccess([]string{"bogus"}); code != 2 {
		t.Fatalf("unknown subcommand = %d", code)
	}
}

func TestIsLoopbackListen(t *testing.T) {
	for addr, want := range map[string]bool{"127.0.0.1:8080": true, "localhost:8080": true, "[::1]:8080": true, "0.0.0.0:8080": false, "203.0.113.5:443": false, "bad": false} {
		if got := isLoopbackListen(addr); got != want {
			t.Errorf("isLoopbackListen(%q) = %v", addr, got)
		}
	}
}

func writeCert(t *testing.T, dir, cn string) (string, string) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	cert, keyf := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
	os.WriteFile(keyf, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600)
	return cert, keyf
}

func TestReloadingCertPicksUpRenewal(t *testing.T) {
	dir := t.TempDir()
	cert, key := writeCert(t, dir, "first")
	rc := &reloadingCert{certFile: cert, keyFile: key}
	c1, err := rc.get(nil)
	if err != nil {
		t.Fatal(err)
	}
	writeCert(t, dir, "second")
	later := time.Now().Add(time.Minute)
	os.Chtimes(cert, later, later)
	os.Chtimes(key, later, later)
	c2, err := rc.get(nil)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(c2.Certificate[0])
	if c1 == c2 || leaf.Subject.CommonName != "second" {
		t.Fatal("renewed certificate not picked up")
	}
	os.WriteFile(cert, []byte("garbage"), 0o644)
	os.Chtimes(cert, later.Add(time.Minute), later.Add(time.Minute))
	if c3, err := rc.get(nil); err != nil || c3 != c2 {
		t.Fatal("a broken renewal must keep the last good certificate")
	}
}
