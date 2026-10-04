package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// pki writes the EX's outer-TLS material: a local CA and a server
// certificate for host. It replaces the installer's openssl calls, which
// needed ED25519 support and -addext/-copy_extensions (OpenSSL >= 3.0).
func pki(args []string) {
	fs := flag.NewFlagSet("pki", flag.ExitOnError)
	dir := fs.String("dir", "", "output directory (ca.pem, ca.key, server.pem, server.key)")
	host := fs.String("host", "", "server name or IP placed in the certificate SAN")
	keyType := fs.String("key-type", "ed25519", "certificate key type: ed25519 (default) or ecdsa (P-256; required for the uTLS/browser-fingerprint carrier, since browsers do not support Ed25519 certificates)")
	_ = fs.Parse(args)
	if *dir == "" || *host == "" {
		die("--dir and --host are required")
	}
	if err := writePKIType(*dir, *host, *keyType, time.Now()); err != nil {
		die(err.Error())
	}
}

// genCertKey makes a key pair of the requested type for a certificate.
func genCertKey(keyType string) (crypto.PublicKey, crypto.Signer, error) {
	switch keyType {
	case "", "ed25519":
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		return pub, priv, err
	case "ecdsa":
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, nil, err
		}
		return &k.PublicKey, k, nil
	default:
		return nil, nil, errors.New("key-type must be ed25519 or ecdsa")
	}
}

// writePKI keeps the historical Ed25519 default for callers (and tests).
func writePKI(dir, host string, now time.Time) error {
	return writePKIType(dir, host, "ed25519", now)
}

func writePKIType(dir, host, keyType string, now time.Time) error {
	caPub, caKey, err := genCertKey(keyType)
	if err != nil {
		return err
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "BAFT Local CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, caPub, caKey)
	if err != nil {
		return err
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return err
	}
	srvPub, srvKey, err := genCertKey(keyType)
	if err != nil {
		return err
	}
	srvTmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(0, 0, 825),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		srvTmpl.IPAddresses = []net.IP{ip}
	} else {
		srvTmpl.DNSNames = []string{host}
	}
	srvDER, err := x509.CreateCertificate(rand.Reader, srvTmpl, ca, srvPub, caKey)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	for _, f := range []struct {
		name, typ string
		der       func() ([]byte, error)
		mode      os.FileMode
	}{
		{"ca.key", "PRIVATE KEY", func() ([]byte, error) { return x509.MarshalPKCS8PrivateKey(caKey) }, 0o600},
		{"server.key", "PRIVATE KEY", func() ([]byte, error) { return x509.MarshalPKCS8PrivateKey(srvKey) }, 0o600},
		{"ca.pem", "CERTIFICATE", func() ([]byte, error) { return caDER, nil }, 0o644},
		{"server.pem", "CERTIFICATE", func() ([]byte, error) { return srvDER, nil }, 0o644},
	} {
		der, err := f.der()
		if err != nil {
			return err
		}
		if err := atomicWrite(filepath.Join(dir, f.name), pem.EncodeToMemory(&pem.Block{Type: f.typ, Bytes: der}), f.mode); err != nil {
			return fmt.Errorf("%s: %w", f.name, err)
		}
	}
	return nil
}

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		die(err.Error())
	}
	return n.Add(n, big.NewInt(1))
}
