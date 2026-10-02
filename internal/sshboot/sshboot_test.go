package sshboot

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

const (
	testToken  = "tok-0123456789abcdef"
	testJobKey = "JOBKEY-abcdefghijklmnop"
)

type fakeSSHD struct {
	addr     string
	hostFP   string
	mu       sync.Mutex
	command  string
	stdin    string
	exitCode uint32
	reply    string // written to the session's stdout
	pubKey   ssh.PublicKey
}

func newSSHD(t *testing.T, password string, authorized ssh.PublicKey) *fakeSSHD {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	hostSigner, _ := ssh.NewSignerFromKey(priv)
	d := &fakeSSHD{hostFP: ssh.FingerprintSHA256(hostSigner.PublicKey()), pubKey: authorized}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if password != "" && string(pw) == password {
				return nil, nil
			}
			return nil, io.EOF
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if authorized != nil && string(k.Marshal()) == string(authorized.Marshal()) {
				return nil, nil
			}
			return nil, io.EOF
		},
	}
	cfg.AddHostKey(hostSigner)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	d.addr = ln.Addr().String()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go d.serve(c, cfg)
		}
	}()
	return d
}

func (d *fakeSSHD) serve(c net.Conn, cfg *ssh.ServerConfig) {
	conn, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		ch, creqs, err := nc.Accept()
		if err != nil {
			return
		}
		go func() {
			for r := range creqs {
				if r.Type != "exec" {
					r.Reply(false, nil)
					continue
				}
				r.Reply(true, nil)
				var p struct{ Cmd string }
				ssh.Unmarshal(r.Payload, &p)
				in, _ := io.ReadAll(ch)
				d.mu.Lock()
				d.command, d.stdin = p.Cmd, string(in)
				code, reply := d.exitCode, d.reply
				d.mu.Unlock()
				io.WriteString(ch, reply)
				ch.SendRequest("exit-status", false, ssh.Marshal(struct{ C uint32 }{code}))
				ch.Close()
				return
			}
		}()
	}
}

func hostPort(t *testing.T, addr string) Target {
	h, p, _ := net.SplitHostPort(addr)
	var port int
	for _, c := range p {
		port = port*10 + int(c-'0')
	}
	return Target{Host: h, Port: port, User: "root"}
}

func baseReq(d *fakeSSHD, t *testing.T) Request {
	return Request{
		Target: hostPort(t, d.addr), Auth: Auth{Password: "s3cret-pass"},
		HostKeyFingerprint: d.hostFP, InstallScript: []byte("#!/bin/bash\necho installing\n"),
		BCCURL: "https://bcc.example.com", NodeID: "ex-1", AgentToken: testToken, BCCJobKey: testJobKey,
	}
}

func TestScanHostKeyReturnsTheServersFingerprint(t *testing.T) {
	d := newSSHD(t, "s3cret-pass", nil)
	fp, typ, err := ScanHostKey(context.Background(), hostPort(t, d.addr))
	if err != nil || fp != d.hostFP || typ != "ssh-ed25519" {
		t.Fatalf("scan = %q %q %v, want %q", fp, typ, err, d.hostFP)
	}
}

func TestBootstrapPinsTheHostKey(t *testing.T) {
	d := newSSHD(t, "s3cret-pass", nil)
	req := baseReq(d, t)
	req.HostKeyFingerprint = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	_, err := Bootstrap(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "host key mismatch") {
		t.Fatalf("wrong fingerprint accepted: %v", err)
	}
	if d.command != "" {
		t.Fatal("a command ran on a server whose host key did not match")
	}
}

func TestBootstrapSendsSecretsOnStdinOnly(t *testing.T) {
	d := newSSHD(t, "s3cret-pass", nil)
	d.reply = "installing... token " + testToken + " done\n"
	res, err := Bootstrap(context.Background(), baseReq(d, t))
	if err != nil {
		t.Fatal(err)
	}
	if d.command != "bash -s" {
		t.Fatalf("remote command %q", d.command)
	}
	for _, secret := range []string{testToken, testJobKey, "s3cret-pass"} {
		if strings.Contains(d.command, secret) {
			t.Fatalf("secret on the command line")
		}
	}
	for _, want := range []string{"export BAFT_AGENT_TOKEN='" + testToken + "'", "export BAFT_BCC_JOB_KEY='" + testJobKey + "'",
		"--agent-only --bcc-url 'https://bcc.example.com' --node-id 'ex-1'", "echo installing", "BAFT_INSTALL_SCRIPT_EOF"} {
		if !strings.Contains(d.stdin, want) {
			t.Errorf("script is missing %q:\n%s", want, d.stdin)
		}
	}
	if strings.Contains(res.Output, testToken) || !strings.Contains(res.Output, "[redacted]") {
		t.Fatalf("output not redacted: %q", res.Output)
	}
}

func TestNonRootUsesSudo(t *testing.T) {
	d := newSSHD(t, "s3cret-pass", nil)
	req := baseReq(d, t)
	req.User = "ubuntu"
	if _, err := Bootstrap(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.command, "sudo -n bash -s") {
		t.Fatalf("non-root user does not use sudo: %q", d.command)
	}
}

func TestFailedInstallAndBadPasswordLeakNothing(t *testing.T) {
	d := newSSHD(t, "s3cret-pass", nil)
	d.exitCode, d.reply = 3, "boom with "+testJobKey+"\n"
	res, err := Bootstrap(context.Background(), baseReq(d, t))
	if err == nil || strings.Contains(res.Output, testJobKey) {
		t.Fatalf("failed install: err=%v output=%q", err, res.Output)
	}
	bad := baseReq(d, t)
	bad.Password = "wrong-password-value"
	_, err = Bootstrap(context.Background(), bad)
	if err == nil || strings.Contains(err.Error(), "wrong-password-value") {
		t.Fatalf("bad password: %v", err)
	}
}

func TestPrivateKeyAuth(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	sshPub, _ := ssh.NewPublicKey(pub)
	der, _ := x509.MarshalPKCS8PrivateKey(priv)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	d := newSSHD(t, "", sshPub)
	req := baseReq(d, t)
	req.Auth = Auth{PrivateKey: pemKey}
	if _, err := Bootstrap(context.Background(), req); err != nil {
		t.Fatalf("key auth: %v", err)
	}
	req.Auth = Auth{PrivateKey: "not a key"}
	if _, err := Bootstrap(context.Background(), req); err == nil || strings.Contains(err.Error(), "not a key") {
		t.Fatalf("bad key: %v", err)
	}
	req.Auth = Auth{PrivateKey: pemKey, Password: "x"}
	if _, err := Bootstrap(context.Background(), req); err == nil {
		t.Fatal("password and key together accepted")
	}
}

func TestRequestValidation(t *testing.T) {
	d := newSSHD(t, "s3cret-pass", nil)
	cases := map[string]func(*Request){
		"no fingerprint":  func(r *Request) { r.HostKeyFingerprint = "" },
		"md5 fingerprint": func(r *Request) { r.HostKeyFingerprint = "MD5:aa:bb" },
		"no user":         func(r *Request) { r.User = "" },
		"bad user":        func(r *Request) { r.User = "root; rm -rf /" },
		"bad host":        func(r *Request) { r.Host = "a b" },
		"bad port":        func(r *Request) { r.Port = 0 },
		"http bcc":        func(r *Request) { r.BCCURL = "http://bcc.example.com" },
		"quote in url":    func(r *Request) { r.BCCURL = "https://x/'; id #" },
		"bad node":        func(r *Request) { r.NodeID = "a b" },
		"quote in token":  func(r *Request) { r.AgentToken = "a'b" },
		"no script":       func(r *Request) { r.InstallScript = nil },
		"no credentials":  func(r *Request) { r.Auth = Auth{} },
	}
	for name, mutate := range cases {
		req := baseReq(d, t)
		mutate(&req)
		if _, err := Bootstrap(context.Background(), req); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if d.command != "" {
		t.Fatal("an invalid request reached the server")
	}
	ok := baseReq(d, t)
	ok.BCCURL, ok.AllowHTTP = "http://10.0.0.1:8080", true
	if _, err := Bootstrap(context.Background(), ok); err != nil {
		t.Fatalf("http with AllowHTTP: %v", err)
	}
	if !strings.Contains(d.stdin, "BAFT_AGENT_ALLOW_HTTP=1") {
		t.Fatal("AllowHTTP not passed to the installer")
	}
}
