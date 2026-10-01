package main

import (
	"crypto/tls"
	"crypto/x509"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/config"
	"github.com/zarkmakerburg/baft/internal/securityinternal"
)

// The generated pair must pass the same loader `baft run` uses, and each
// side must name the other's identity and pin the other's key.
func TestGeneratedConfigsLoadAndPinEachOther(t *testing.T) {
	dir := t.TempDir()
	if err := writePKI(filepath.Join(dir, "pki"), "192.0.2.10", time.Now()); err != nil {
		t.Fatal(err)
	}
	ex, _ := securityinternal.GenerateKeyPair()
	ir, _ := securityinternal.GenerateKeyPair()
	ca, _ := os.ReadFile(filepath.Join(dir, "pki", "ca.pem"))
	d, psk, err := securityinternal.NewPairingDescriptor("192.0.2.10:8443", "192.0.2.10", "urn:baft:node:ex-1", ex.Public, ca, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	c := addCommonFlags(flag.NewFlagSet("t", flag.ContinueOnError), filepath.Join(dir, "admin.sock"))

	irCfg, err := irConfig(d, "urn:baft:node:ir-1", filepath.Join(dir, "pki", "ca.pem"), filepath.Join(dir, "ir.json"), "127.0.0.1:1443", c)
	if err != nil {
		t.Fatal(err)
	}
	irPath := filepath.Join(dir, "ir.yaml")
	if err := writeConfig(irPath, irCfg); err != nil {
		t.Fatal(err)
	}
	reply, err := securityinternal.NewPairingReply(d, ex.Public, psk, "urn:baft:node:ir-1", ir.Public)
	if err != nil {
		t.Fatal(err)
	}
	id, pub, err := securityinternal.DecodePairingReply(reply, psk, ex.Public, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	p := exPending{Version: 1, Identity: d.NodeIdentity, ServerName: d.ServerName}
	pki := func(n string) string { return filepath.Join(dir, "pki", n) }
	exCfg, err := exConfig(p, id, pub, "0.0.0.0:8443", pki("ca.pem"), pki("server.pem"), pki("server.key"), filepath.Join(dir, "ex.json"), "127.0.0.1:2443", c)
	if err != nil {
		t.Fatal(err)
	}
	exPath := filepath.Join(dir, "ex.yaml")
	if err := writeConfig(exPath, exCfg); err != nil {
		t.Fatal(err)
	}

	gotIR, err := config.LoadFile(irPath)
	if err != nil {
		t.Fatal(err)
	}
	gotEX, err := config.LoadFile(exPath)
	if err != nil {
		t.Fatal(err)
	}
	exPub, _ := securityinternal.EncodePublicKey(ex.Public)
	irPub, _ := securityinternal.EncodePublicKey(ir.Public)
	if gotIR.Noise.PeerPublicKey != exPub || gotEX.Noise.PeerPublicKey != irPub {
		t.Fatal("configs do not pin each other's Noise key")
	}
	if gotIR.Peer.AllowedIdentity != "urn:baft:node:"+gotEX.Node.ID || gotEX.Server.AllowedPeerIdentities[0] != "urn:baft:node:"+gotIR.Node.ID {
		t.Fatal("node ids do not match the identities each side expects")
	}
	if gotIR.TLS.CertFile != "" || gotIR.TLS.KeyFile != "" {
		t.Fatal("Noise dialer config carries an unused client certificate")
	}
	if gotIR.Routes[0].RemoteRoute != gotEX.Routes[0].ID {
		t.Fatal("route ids do not match")
	}
}

func TestPKIServerCertificateVerifiesForHost(t *testing.T) {
	for _, host := range []string{"192.0.2.10", "ex.example"} {
		dir := t.TempDir()
		if err := writePKI(dir, host, time.Now()); err != nil {
			t.Fatal(err)
		}
		pair, err := tls.LoadX509KeyPair(filepath.Join(dir, "server.pem"), filepath.Join(dir, "server.key"))
		if err != nil {
			t.Fatal(err)
		}
		caPEM, _ := os.ReadFile(filepath.Join(dir, "ca.pem"))
		roots := x509.NewCertPool()
		roots.AppendCertsFromPEM(caPEM)
		leaf, _ := x509.ParseCertificate(pair.Certificate[0])
		if _, err := leaf.Verify(x509.VerifyOptions{DNSName: host, Roots: roots}); err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		for _, n := range []string{"ca.key", "server.key"} {
			st, _ := os.Stat(filepath.Join(dir, n))
			if st.Mode().Perm() != 0o600 {
				t.Fatalf("%s mode %v", n, st.Mode().Perm())
			}
		}
	}
}
