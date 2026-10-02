package bcc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/zarkmakerburg/baft/internal/sshboot"
)

// BootstrapConfig turns on server inventory + SSH bootstrap.
type BootstrapConfig struct {
	// InstallScript is install.sh as shipped with this BCC.
	InstallScript []byte
	// PublicURL is how agents reach this BCC (https://...).
	PublicURL string
	// AllowHTTP lets the agent talk plain HTTP to BCC (lab setups only).
	AllowHTTP bool
}

type bootstrapState struct {
	cfg  BootstrapConfig
	mu   sync.Mutex // one bootstrap at a time
	scan func(context.Context, sshboot.Target) (string, string, error)
	run  func(context.Context, sshboot.Request) (sshboot.Result, error)
}

// ConfigureBootstrap enables /api/bootstrap and /api/bootstrap/hostkey.
func (s *Server) ConfigureBootstrap(cfg BootstrapConfig) {
	s.boot = &bootstrapState{cfg: cfg, scan: sshboot.ScanHostKey, run: sshboot.Bootstrap}
}

// credentialsAreProtected is true when SSH credentials in a request body are
// not exposed on the network: TLS, or a loopback peer (reverse proxy / tunnel).
func credentialsAreProtected(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) bootstrapReady(w http.ResponseWriter) bool {
	if s.boot == nil {
		http.Error(w, "server bootstrap is not configured (start baft-bcc with --install-script and --public-url)", http.StatusNotImplemented)
		return false
	}
	return true
}

// bootstrapHostKey reads a server's SSH host key so the operator can confirm
// its fingerprint before any credential is sent.
func (s *Server) bootstrapHostKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if !s.admin(w, r) || !s.bootstrapReady(w) {
		return
	}
	var in struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	}
	if err := decodeJSON(r, &in); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if in.Port == 0 {
		in.Port = 22
	}
	t := sshboot.Target{Host: in.Host, Port: in.Port}
	details := map[string]any{"host": in.Host, "port": in.Port}
	fp, typ, err := s.boot.scan(r.Context(), t)
	if err != nil {
		s.auditFailure(w, r, "bootstrap.hostkey", in.Host, details, err, http.StatusBadGateway)
		return
	}
	details["fingerprint"] = fp
	if err := s.auditAdmin(r, "bootstrap.hostkey", in.Host, "success", details); err != nil {
		http.Error(w, "audit log failure", 500)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"host": in.Host, "port": strconv.Itoa(in.Port), "fingerprint": fp, "key_type": typ})
}

// bootstrap registers a server in the inventory and installs baft-agent on it
// over SSH. The SSH credentials exist only in this request: they are not
// stored, not audited and not echoed.
func (s *Server) bootstrap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if !s.admin(w, r) || !s.bootstrapReady(w) {
		return
	}
	if !credentialsAreProtected(r) {
		http.Error(w, "SSH credentials must not cross the network in clear text: use https", http.StatusBadRequest)
		return
	}
	if s.jobKey == nil {
		http.Error(w, "job signing is not configured; the agent could not verify jobs", http.StatusConflict)
		return
	}
	var in struct {
		NodeID      string `json:"node_id"`
		Alias       string `json:"alias"`
		Role        string `json:"role"`
		Address     string `json:"address"`
		Host        string `json:"host"`
		Port        int    `json:"port"`
		User        string `json:"user"`
		Fingerprint string `json:"host_key_fingerprint"`
		Password    string `json:"password"`
		PrivateKey  string `json:"private_key"`
		Passphrase  string `json:"passphrase"`
	}
	err := decodeJSON(r, &in)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if in.Port == 0 {
		in.Port = 22
	}
	if in.User == "" {
		in.User = "root"
	}
	if in.Role == "" {
		in.Role = "foreign"
	}
	method := "password"
	if in.PrivateKey != "" {
		method = "private_key"
	}
	// Audit records who/where/how, never the credential.
	details := map[string]any{"host": in.Host, "port": in.Port, "user": in.User, "auth": method, "host_key_fingerprint": in.Fingerprint, "role": in.Role}

	if !s.boot.mu.TryLock() {
		http.Error(w, "another bootstrap is running", http.StatusConflict)
		return
	}
	defer s.boot.mu.Unlock()

	token, err := newAgentToken()
	if err != nil {
		http.Error(w, "token generation failed", 500)
		return
	}
	req := sshboot.Request{
		Target:             sshboot.Target{Host: in.Host, Port: in.Port, User: in.User},
		Auth:               sshboot.Auth{Password: in.Password, PrivateKey: in.PrivateKey, Passphrase: in.Passphrase},
		HostKeyFingerprint: in.Fingerprint, InstallScript: s.boot.cfg.InstallScript,
		BCCURL: s.boot.cfg.PublicURL, NodeID: in.NodeID, AgentToken: token,
		BCCJobKey: s.JobPublicKey(), AllowHTTP: s.boot.cfg.AllowHTTP,
	}
	in.Password, in.PrivateKey, in.Passphrase = "", "", ""

	address := in.Address
	if address == "" {
		address = net.JoinHostPort(in.Host, strconv.Itoa(in.Port))
	}
	s.mutationMu.Lock()
	node, err := s.store.UpsertNode(Node{ID: in.NodeID, Alias: in.Alias, Address: address, Role: in.Role}, token)
	s.mutationMu.Unlock()
	if err != nil {
		s.auditFailure(w, r, "node.bootstrap", in.NodeID, details, err, http.StatusBadRequest)
		return
	}
	res, err := s.boot.run(r.Context(), req)
	if err != nil {
		details["output_tail"] = tail(res.Output, 2000)
		s.auditFailure(w, r, "node.bootstrap", in.NodeID, details, err, http.StatusBadGateway)
		return
	}
	if err := s.auditAdmin(r, "node.bootstrap", in.NodeID, "success", details); err != nil {
		http.Error(w, "audit log failure", 500)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"node": node, "output": tail(res.Output, 8000)})
}

func newAgentToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", errors.New("no randomness")
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return "..." + s[len(s)-n:]
	}
	return s
}
