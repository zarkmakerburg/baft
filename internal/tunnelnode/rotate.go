package tunnelnode

// Transactional certificate rotation (A4 Stage F): the node side of
// replacing the EX's outer TLS certificate authority and server certificate
// of a BAFT-built tunnel without ever leaving the IR unable to verify the
// certificate the EX is serving.
//
//	PREPARE     EX   stages a new CA + server certificate next to the live
//	                 set; nothing live changes.
//	DISTRIBUTE  IR   trusts OLD + NEW (its CA file becomes a bundle) and
//	                 restarts; the old certificate must still verify.
//	VERIFY      EX   staged set is intact and chains to the new CA.
//	            IR   the new leaf verifies against the live bundle and the
//	                 old one is still served and accepted.
//	ACTIVATE    EX   swaps the whole pki directory atomically, restarts and
//	                 proves it serves the new leaf; restores the old set on
//	                 any failure.
//	CONFIRM     IR   sees the new leaf over the real path and the tunnel is
//	                 healthy.
//	RETIRE_OLD  IR   trusts NEW only (restores the bundle on failure);
//	            EX   destroys the old key material. No rollback after this.
//
// The trust window is OLD+NEW on the IR from DISTRIBUTE until RETIRE; the EX
// serves exactly one certificate at every instant. Every state the files can
// be in after a crash or reboot is bootable and mutually trusted:
//
//	IR trusts   EX serves   reached by
//	old         old         before DISTRIBUTE, after a full rollback
//	old+new     old         DISTRIBUTE..ACTIVATE, during rollback
//	old+new     new         ACTIVATE..RETIRE
//	new         new         after RETIRE
//
// The two unsafe pairs (old/new, new/old) are excluded by order: the EX
// activates only after the IR proved the new leaf verifies, the EX rolls back
// before the IR does (and the IR refuses to drop the new CA unless it sees
// the EX serving a certificate the old trust accepts), and the IR retires the
// old CA only after it saw the new leaf served, after which neither side can
// roll back.
//
// Records live in <StateDir>/rotations/<id>/rot.json; every operation is
// idempotent and resumes an interrupted one.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/zarkmakerburg/baft/internal/config"
)

// Rotation phases on a node.
const (
	RotStaging    = "staging"    // EX: the new set is being generated
	RotStaged     = "staged"     // EX: new set ready next to the live one
	RotActivating = "activating" // EX: swap in progress
	RotActive     = "active"     // EX: serving the new certificate, old kept
	RotTrusting   = "trusting"   // IR: trusts old + new
	RotConfirmed  = "confirmed"  // IR: saw the new certificate served
	RotRetiring   = "retiring"   // either: dropping the old material
	RotRetired    = "retired"    // final: old material gone
	RotRolledBack = "rolled_back"
)

// Rotation is the persisted state of one rotation on this node.
type Rotation struct {
	Version  int       `json:"version"`
	ID       string    `json:"id"`
	TunnelID string    `json:"tunnel_id"`
	Role     string    `json:"role"`
	Epoch    int       `json:"epoch"`
	Phase    string    `json:"phase"`
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`

	Host          string `json:"host,omitempty"`
	NewCASHA256   string `json:"new_ca_sha256,omitempty"`
	NewCertSHA256 string `json:"new_cert_sha256,omitempty"`
	OldCertSHA256 string `json:"old_cert_sha256,omitempty"`
	// EX: digests of the live pki files before, and of the staged set.
	OldPKI map[string]string `json:"old_pki,omitempty"`
	NewPKI map[string]string `json:"new_pki,omitempty"`
	// IR: digests of the CA file before and of the bundle BAFT wrote.
	OldCAFileSHA256       string `json:"old_ca_file_sha256,omitempty"`
	InstalledCAFileSHA256 string `json:"installed_ca_file_sha256,omitempty"`
	TrustLoaded           bool   `json:"trust_loaded,omitempty"`

	Verified  bool      `json:"verified,omitempty"`
	Activated time.Time `json:"activated,omitempty"`
	Retired   time.Time `json:"retired,omitempty"`
}

// RotationPlan is what the EX hands BCC after PREPARE. Everything in it is
// public (certificates and digests); the new private keys never leave the
// EX.
type RotationPlan struct {
	RotationID    string `json:"rotation_id"`
	TunnelID      string `json:"tunnel_id"`
	Epoch         int    `json:"epoch"`
	Host          string `json:"host"`
	CADER         string `json:"ca_der"`
	CASHA256      string `json:"ca_sha256"`
	CertDER       string `json:"cert_der"`
	CertSHA256    string `json:"cert_sha256"`
	OldCertSHA256 string `json:"old_cert_sha256"`
}

// RotationEvidence is what a node proves about itself after a step.
type RotationEvidence struct {
	RotationID string `json:"rotation_id"`
	Role       string `json:"role"`
	Step       string `json:"step"`
	Phase      string `json:"phase"`
	Epoch      int    `json:"epoch"`
	NodeEpoch  int    `json:"node_epoch"`

	ServiceActive bool `json:"service_active"`
	// The leaf the TLS probe saw and whether it is the old or new one.
	ServedCertSHA256 string `json:"served_cert_sha256,omitempty"`
	ServesNew        bool   `json:"serves_new"`
	// IR: the live CA file and the CAs in it.
	CAFileSHA256 string   `json:"ca_file_sha256,omitempty"`
	TrustedCAs   []string `json:"trusted_cas,omitempty"`
	OldTrusted   bool     `json:"old_trusted"`
	NewTrusted   bool     `json:"new_trusted"`
	// EX: the live leaf on disk.
	LiveCertSHA256 string `json:"live_cert_sha256,omitempty"`
	Detail         string `json:"detail,omitempty"`
}

var hexRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ---- paths ----

func (m *Manager) rotRoot() string                { return filepath.Join(m.StateDir, "rotations") }
func (m *Manager) rotDir(rid string) string       { return filepath.Join(m.rotRoot(), rid) }
func (m *Manager) rotPath(rid string) string      { return filepath.Join(m.rotDir(rid), "rot.json") }
func (m *Manager) rotActivePath() string          { return filepath.Join(m.rotRoot(), "active") }
func (m *Manager) rotEpochPath() string           { return filepath.Join(m.rotRoot(), "epoch") }
func (m *Manager) pkiNext(rid string) string      { return m.pkiDir() + ".next-" + rid }
func (m *Manager) pkiPrev(rid string) string      { return m.pkiDir() + ".prev-" + rid }
func (m *Manager) rotBackup(rid, n string) string { return filepath.Join(m.rotDir(rid), "backup", n) }

func (m *Manager) readRot(rid string) (Rotation, error) {
	var r Rotation
	b, err := os.ReadFile(m.rotPath(rid))
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("corrupt rotation record %s: %w", rid, err)
	}
	return r, nil
}

func (m *Manager) writeRot(r *Rotation) error {
	r.Updated = m.Now().UTC()
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(m.rotPath(r.ID), append(b, '\n'), 0o600)
}

func (m *Manager) rotActiveID() string {
	b, err := os.ReadFile(m.rotActivePath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// RotationInProgress names a rotation on this node that has not finished
// (retired or rolled back), or "".
func (m *Manager) rotationInProgress() string {
	id := m.rotActiveID()
	if id == "" {
		return ""
	}
	r, err := m.readRot(id)
	if err != nil {
		return id // a pointer without a readable record: be safe
	}
	if r.Phase == RotRetired || r.Phase == RotRolledBack {
		return ""
	}
	return id
}

func (m *Manager) clearRotActive(rid string) {
	if m.rotActiveID() == rid {
		_ = os.Remove(m.rotActivePath())
	}
}

// CertEpoch is the node's certificate generation: the epoch of the last
// rotation that retired its old material (0 before any rotation).
func (m *Manager) CertEpoch() int {
	b, err := os.ReadFile(m.rotEpochPath())
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func (m *Manager) fault(point string) error {
	if m.Fault == nil {
		return nil
	}
	return m.Fault(point)
}

// ---- checks ----

// rotationTarget proves the live tunnel on this node is the BAFT-built
// tunnelID in role, that no tunnel change runs, and returns its config.
// Only files BAFT installed for that tunnel are ever rotated.
func (m *Manager) rotationTarget(tunnelID, role string) (config.Config, error) {
	var cfg config.Config
	if !idRe.MatchString(tunnelID) {
		return cfg, errors.New("invalid tunnel id")
	}
	if cur := m.activeID(); cur != "" {
		if t, err := m.readTxn(cur); err == nil && (t.Phase == PhasePrepared || t.Phase == PhaseCommitted) {
			return cfg, fmt.Errorf("tunnel change %s is in progress on this node", cur)
		}
	}
	t, err := m.readTxn(tunnelID)
	if err != nil {
		return cfg, fmt.Errorf("tunnel %s was not built on this node", tunnelID)
	}
	if t.Role != role {
		return cfg, fmt.Errorf("this node is the %s side of tunnel %s", t.Role, tunnelID)
	}
	if t.Phase != PhaseFinalized {
		return cfg, fmt.Errorf("tunnel %s is %s, not finalized", tunnelID, t.Phase)
	}
	var mk Marker
	if err := readJSON(m.markerPath(), &mk); err != nil || mk.ManagedBy != "baft" || mk.TunnelID != tunnelID {
		return cfg, fmt.Errorf("the live configuration is not BAFT's tunnel %s", tunnelID)
	}
	raw, err := os.ReadFile(m.liveConfig())
	if err != nil {
		return cfg, fmt.Errorf("live config: %w", err)
	}
	want := t.InstalledConfigSHA
	if want == "" {
		want = mk.ConfigSHA256
	}
	if shaHex(raw) != want {
		return cfg, errors.New("the live config was changed outside BAFT; nothing was changed")
	}
	cfg, err = config.LoadFile(m.liveConfig())
	if err != nil {
		return cfg, fmt.Errorf("live config does not load: %w", err)
	}
	if cfg.Noise == nil {
		return cfg, errors.New("only Noise tunnels built by BCC can rotate certificates")
	}
	if role == RoleEX {
		if cfg.Server == nil || cfg.TLS.CAFile != filepath.Join(m.pkiDir(), "ca.pem") ||
			cfg.TLS.CertFile != filepath.Join(m.pkiDir(), "server.pem") || cfg.TLS.KeyFile != filepath.Join(m.pkiDir(), "server.key") {
			return cfg, errors.New("the live config does not use BAFT's certificate files")
		}
	} else {
		if cfg.Peer == nil || cfg.TLS.CAFile != filepath.Join(m.stage(tunnelID), "peer-ca.pem") {
			return cfg, errors.New("the live config does not use BAFT's pinned CA file")
		}
	}
	return cfg, nil
}

// beginRotation refuses a second rotation, a stale epoch, or a tunnel that
// is not this node's live BAFT tunnel.
func (m *Manager) beginRotation(rid, tunnelID, role string, epoch int) (config.Config, error) {
	if !idRe.MatchString(rid) {
		return config.Config{}, errors.New("invalid rotation id")
	}
	if cur := m.rotationInProgress(); cur != "" && cur != rid {
		return config.Config{}, fmt.Errorf("rotation %s is still in progress on this node", cur)
	}
	if epoch <= m.CertEpoch() {
		return config.Config{}, fmt.Errorf("rotation epoch %d is not newer than this node's certificate epoch %d", epoch, m.CertEpoch())
	}
	return m.rotationTarget(tunnelID, role)
}

func (m *Manager) rotFor(rid, role string) (Rotation, config.Config, error) {
	r, err := m.readRot(rid)
	if err != nil {
		return r, config.Config{}, fmt.Errorf("unknown rotation %s on this node", rid)
	}
	if r.Role != role {
		return r, config.Config{}, fmt.Errorf("rotation %s is the %s side on this node", rid, r.Role)
	}
	cfg, err := m.rotationTarget(r.TunnelID, role)
	return r, cfg, err
}

// ---- certificate helpers ----

func parseCerts(b []byte) ([]*x509.Certificate, error) {
	var out []*x509.Certificate
	for {
		var blk *pem.Block
		blk, b = pem.Decode(b)
		if blk == nil {
			break
		}
		if blk.Type != "CERTIFICATE" {
			return nil, errors.New("not a certificate file")
		}
		c, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, errors.New("no certificate found")
	}
	return out, nil
}

func poolOf(certs []*x509.Certificate) *x509.CertPool {
	p := x509.NewCertPool()
	for _, c := range certs {
		p.AddCert(c)
	}
	return p
}

func certPEM(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func readLeaf(path string) (*x509.Certificate, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cs, err := parseCerts(b)
	if err != nil {
		return nil, err
	}
	return cs[0], nil
}

func certSHAs(certs []*x509.Certificate) []string {
	out := make([]string, 0, len(certs))
	for _, c := range certs {
		out = append(out, shaHex(c.Raw))
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// probeTLS completes a TLS handshake verified by pool and returns the
// SHA-256 of the leaf the server presented.
func probeTLS(ctx context.Context, addr, serverName string, pool *x509.CertPool) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	d := tls.Dialer{Config: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: pool, ServerName: serverName, NextProtos: []string{"h2", "http/1.1"}}}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", err
	}
	defer c.Close()
	cs := c.(*tls.Conn).ConnectionState()
	if len(cs.PeerCertificates) == 0 {
		return "", errors.New("no certificate presented")
	}
	return shaHex(cs.PeerCertificates[0].Raw), nil
}

// probeServed returns the leaf digest the EX serves, verified by pool. It
// is the IR's view over the real path (peer address) or the EX's view of its
// own listener.
func (m *Manager) probeServed(ctx context.Context, cfg config.Config, r Rotation, pool *x509.CertPool) (string, error) {
	if r.Role == RoleIR {
		return probeTLS(ctx, cfg.Peer.Address, cfg.Peer.ServerName, pool)
	}
	host, port, err := net.SplitHostPort(cfg.Server.Listen)
	if err != nil {
		return "", err
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return probeTLS(ctx, net.JoinHostPort(host, port), r.Host, pool)
}

var pkiFiles = []string{"ca.pem", "ca.key", "server.pem", "server.key", "host"}

// pkiDigests returns the digest of each certificate file in dir ("" when the
// directory is missing).
func pkiDigests(dir string) map[string]string {
	out := map[string]string{}
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return nil
	}
	for _, n := range pkiFiles {
		if b, err := os.ReadFile(filepath.Join(dir, n)); err == nil {
			out[n] = shaHex(b)
		}
	}
	return out
}

func sameDigests(a, b map[string]string) bool {
	if len(a) == 0 || len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// securePKI applies the modes and owners ensurePKI uses.
func (m *Manager) securePKI(dir string) error {
	_ = os.Chmod(dir, 0o750)
	_ = os.Chmod(filepath.Join(dir, "ca.key"), 0o600)
	_ = os.Chmod(filepath.Join(dir, "server.key"), 0o600)
	_ = os.Chmod(filepath.Join(dir, "ca.pem"), 0o644)
	_ = os.Chmod(filepath.Join(dir, "server.pem"), 0o644)
	if err := m.chownTo(filepath.Join(dir, "server.key"), m.User, m.User); err != nil {
		return err
	}
	return m.chownTo(dir, "root", m.User)
}

// writeLike replaces path atomically, keeping its mode and owner.
func writeLike(path string, b []byte) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if err := writeFile(path, b, fi.Mode().Perm()); err != nil {
		return err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		_ = os.Chown(path, int(st.Uid), int(st.Gid))
	}
	return nil
}

func (m *Manager) restartService(ctx context.Context) error {
	if _, err := m.System.Systemctl(ctx, "restart", m.Service); err != nil {
		return fmt.Errorf("systemctl restart %s: %w", m.Service, err)
	}
	return m.waitActive(ctx)
}

func (m *Manager) evidence(ctx context.Context, r Rotation, step, detail string) RotationEvidence {
	state, _ := m.System.Systemctl(ctx, "is-active", m.Service)
	return RotationEvidence{RotationID: r.ID, Role: r.Role, Step: step, Phase: r.Phase, Epoch: r.Epoch, NodeEpoch: m.CertEpoch(),
		ServiceActive: state == "active", Detail: detail}
}

func evidenceJSON(e RotationEvidence) string {
	b, _ := json.Marshal(e)
	return string(b)
}

// ---- EX: PREPARE ----

// RotatePrepareEX stages a new CA and server certificate for the live
// tunnel's certificate name next to the live set. Nothing live changes.
func (m *Manager) RotatePrepareEX(ctx context.Context, rid, tunnelID string, epoch int) (RotationPlan, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.readRot(rid)
	switch {
	case err == nil:
		if r.Role != RoleEX || r.TunnelID != tunnelID || r.Epoch != epoch {
			return RotationPlan{}, fmt.Errorf("rotation %s exists with other parameters", rid)
		}
		switch r.Phase {
		case RotStaged, RotActivating, RotActive:
			return m.planOf(r)
		case RotStaging: // interrupted: generate again
		default:
			return RotationPlan{}, fmt.Errorf("rotation %s is %s; start a new rotation", rid, r.Phase)
		}
	case errors.Is(err, os.ErrNotExist):
		if _, err := m.beginRotation(rid, tunnelID, RoleEX, epoch); err != nil {
			return RotationPlan{}, err
		}
		leaf, err := readLeaf(filepath.Join(m.pkiDir(), "server.pem"))
		if err != nil {
			return RotationPlan{}, fmt.Errorf("live server certificate: %w", err)
		}
		host := ""
		if b, err := os.ReadFile(filepath.Join(m.pkiDir(), "host")); err == nil {
			host = strings.TrimSpace(string(b))
		}
		if host == "" && len(leaf.IPAddresses) > 0 {
			host = leaf.IPAddresses[0].String()
		} else if host == "" && len(leaf.DNSNames) > 0 {
			host = leaf.DNSNames[0]
		}
		if !hostRe.MatchString(host) || leaf.VerifyHostname(host) != nil {
			return RotationPlan{}, fmt.Errorf("cannot tell the certificate name of the live certificate (%q)", host)
		}
		r = Rotation{Version: 1, ID: rid, TunnelID: tunnelID, Role: RoleEX, Epoch: epoch, Phase: RotStaging,
			Created: m.Now().UTC(), Host: host, OldCertSHA256: shaHex(leaf.Raw), OldPKI: pkiDigests(m.pkiDir())}
		if err := os.MkdirAll(m.rotDir(rid), 0o700); err != nil {
			return RotationPlan{}, err
		}
		if err := m.writeRot(&r); err != nil {
			return RotationPlan{}, err
		}
		if err := writeFile(m.rotActivePath(), []byte(rid+"\n"), 0o600); err != nil {
			return RotationPlan{}, err
		}
	default:
		return RotationPlan{}, err
	}
	if err := m.fault("prepare:after-record"); err != nil {
		return RotationPlan{}, err
	}
	// The staging directory carries this rotation's id and the record says it
	// is still being generated, so whatever is there is our own partial work.
	next := m.pkiNext(rid)
	if err := os.RemoveAll(next); err != nil {
		return RotationPlan{}, err
	}
	if out, err := m.System.Run(ctx, m.PairBin, "pki", "--dir", next, "--host", r.Host); err != nil {
		return RotationPlan{}, fmt.Errorf("baft-pair pki: %v: %s", err, tailOf(out))
	}
	if err := writeFile(filepath.Join(next, "host"), []byte(r.Host+"\n"), 0o644); err != nil {
		return RotationPlan{}, err
	}
	if err := m.securePKI(next); err != nil {
		return RotationPlan{}, err
	}
	caPEM, err := os.ReadFile(filepath.Join(next, "ca.pem"))
	if err != nil {
		return RotationPlan{}, err
	}
	cas, err := parseCerts(caPEM)
	if err != nil || len(cas) != 1 || !cas[0].IsCA {
		return RotationPlan{}, errors.New("the new CA is not a single CA certificate")
	}
	leaf, err := readLeaf(filepath.Join(next, "server.pem"))
	if err != nil {
		return RotationPlan{}, err
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: poolOf(cas), DNSName: r.Host}); err != nil {
		return RotationPlan{}, fmt.Errorf("the new certificate does not verify: %w", err)
	}
	if _, err := tls.LoadX509KeyPair(filepath.Join(next, "server.pem"), filepath.Join(next, "server.key")); err != nil {
		return RotationPlan{}, fmt.Errorf("the new key does not match: %w", err)
	}
	if err := writeFile(filepath.Join(m.rotDir(rid), "new-ca.pem"), certPEM(cas[0].Raw), 0o644); err != nil {
		return RotationPlan{}, err
	}
	if err := writeFile(filepath.Join(m.rotDir(rid), "new-cert.pem"), certPEM(leaf.Raw), 0o644); err != nil {
		return RotationPlan{}, err
	}
	r.NewCASHA256, r.NewCertSHA256, r.NewPKI = shaHex(cas[0].Raw), shaHex(leaf.Raw), pkiDigests(next)
	r.Phase = RotStaged
	if err := m.writeRot(&r); err != nil {
		return RotationPlan{}, err
	}
	if err := m.fault("prepare:after-stage"); err != nil {
		return RotationPlan{}, err
	}
	return m.planOf(r)
}

func (m *Manager) planOf(r Rotation) (RotationPlan, error) {
	ca, err := readLeaf(filepath.Join(m.rotDir(r.ID), "new-ca.pem"))
	if err != nil {
		return RotationPlan{}, err
	}
	leaf, err := readLeaf(filepath.Join(m.rotDir(r.ID), "new-cert.pem"))
	if err != nil {
		return RotationPlan{}, err
	}
	if shaHex(ca.Raw) != r.NewCASHA256 || shaHex(leaf.Raw) != r.NewCertSHA256 {
		return RotationPlan{}, errors.New("the staged certificates differ from the rotation record")
	}
	enc := base64.RawURLEncoding.EncodeToString
	return RotationPlan{RotationID: r.ID, TunnelID: r.TunnelID, Epoch: r.Epoch, Host: r.Host, CADER: enc(ca.Raw), CASHA256: r.NewCASHA256,
		CertDER: enc(leaf.Raw), CertSHA256: r.NewCertSHA256, OldCertSHA256: r.OldCertSHA256}, nil
}

// ---- IR: DISTRIBUTE ----

// RotateTrustIR makes the IR trust the new CA in addition to the old one
// and restarts it. The EX still serves the old certificate, which must still
// verify afterwards; on any failure the previous CA file is restored.
func (m *Manager) RotateTrustIR(ctx context.Context, rid, tunnelID string, epoch int, caDER []byte, caSHA string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !hexRe.MatchString(caSHA) || shaHex(caDER) != caSHA {
		return "", errors.New("the new CA does not match its digest")
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil || !ca.IsCA || !ca.BasicConstraintsValid {
		return "", errors.New("the new CA is not a CA certificate")
	}
	if now := m.Now(); now.Before(ca.NotBefore) || now.After(ca.NotAfter) {
		return "", errors.New("the new CA is not valid now")
	}
	var cfg config.Config
	r, err := m.readRot(rid)
	switch {
	case err == nil:
		if r.Role != RoleIR || r.TunnelID != tunnelID || r.Epoch != epoch || r.NewCASHA256 != caSHA {
			return "", fmt.Errorf("rotation %s exists with other parameters", rid)
		}
		if r.Phase != RotTrusting {
			return "", fmt.Errorf("rotation %s is %s", rid, r.Phase)
		}
		if cfg, err = m.rotationTarget(tunnelID, RoleIR); err != nil {
			return "", err
		}
		if r.TrustLoaded {
			return m.irEvidence(ctx, cfg, r, "distribute", "already trusts old + new")
		}
		live, err := os.ReadFile(cfg.TLS.CAFile)
		if err != nil {
			return "", err
		}
		switch shaHex(live) {
		case r.InstalledCAFileSHA256:
		case r.OldCAFileSHA256:
			if err := m.writeBundle(cfg, r); err != nil {
				return "", err
			}
		default:
			return "", errors.New("the CA file was changed outside BAFT; nothing was changed")
		}
	case errors.Is(err, os.ErrNotExist):
		if cfg, err = m.beginRotation(rid, tunnelID, RoleIR, epoch); err != nil {
			return "", err
		}
		old, err := os.ReadFile(cfg.TLS.CAFile)
		if err != nil {
			return "", fmt.Errorf("pinned CA file: %w", err)
		}
		oldCerts, err := parseCerts(old)
		if err != nil {
			return "", fmt.Errorf("pinned CA file: %w", err)
		}
		if contains(certSHAs(oldCerts), caSHA) {
			return "", errors.New("the new CA is already trusted: it is not new")
		}
		bundle := append(append([]byte(strings.TrimRight(string(old), "\n")), '\n'), certPEM(caDER)...)
		r = Rotation{Version: 1, ID: rid, TunnelID: tunnelID, Role: RoleIR, Epoch: epoch, Phase: RotTrusting, Created: m.Now().UTC(),
			NewCASHA256: caSHA, OldCAFileSHA256: shaHex(old), InstalledCAFileSHA256: shaHex(bundle)}
		if err := writeFile(m.rotBackup(rid, "ca.pem"), old, 0o644); err != nil {
			return "", err
		}
		if err := writeFile(filepath.Join(m.rotDir(rid), "new-ca.pem"), certPEM(caDER), 0o644); err != nil {
			return "", err
		}
		if err := writeFile(filepath.Join(m.rotDir(rid), "bundle.pem"), bundle, 0o644); err != nil {
			return "", err
		}
		if err := m.writeRot(&r); err != nil {
			return "", err
		}
		if err := writeFile(m.rotActivePath(), []byte(rid+"\n"), 0o600); err != nil {
			return "", err
		}
		if err := m.fault("trust:before-write"); err != nil {
			return "", err
		}
		if err := m.writeBundle(cfg, r); err != nil {
			return "", err
		}
	default:
		return "", err
	}
	if err := m.fault("trust:after-write"); err != nil {
		return "", err
	}
	if err := m.restartService(ctx); err != nil {
		return "", m.irRestoreOld(ctx, cfg, &r, "the IR did not come up trusting old + new: "+err.Error())
	}
	pool, _, err := m.irPool(cfg)
	if err == nil {
		_, err = m.probeServed(ctx, cfg, r, pool)
	}
	if err != nil {
		return "", m.irRestoreOld(ctx, cfg, &r, "with old + new trusted the EX's current certificate did not verify: "+err.Error())
	}
	r.TrustLoaded = true
	if err := m.writeRot(&r); err != nil {
		return "", err
	}
	return m.irEvidence(ctx, cfg, r, "distribute", "trusts old + new; the current certificate still verifies")
}

func (m *Manager) writeBundle(cfg config.Config, r Rotation) error {
	b, err := os.ReadFile(filepath.Join(m.rotDir(r.ID), "bundle.pem"))
	if err != nil || shaHex(b) != r.InstalledCAFileSHA256 {
		return errors.New("the staged CA bundle is missing or changed")
	}
	return writeLike(cfg.TLS.CAFile, b)
}

// irRestoreOld puts the previous CA file back after a failed DISTRIBUTE.
func (m *Manager) irRestoreOld(ctx context.Context, cfg config.Config, r *Rotation, reason string) error {
	old, err := os.ReadFile(m.rotBackup(r.ID, "ca.pem"))
	if err == nil {
		err = writeLike(cfg.TLS.CAFile, old)
	}
	if err == nil {
		err = m.restartService(ctx)
	}
	if err != nil {
		return fmt.Errorf("%s; restoring the previous CA file failed: %v", reason, err)
	}
	r.Phase = RotRolledBack
	_ = m.writeRot(r)
	m.clearRotActive(r.ID)
	return fmt.Errorf("%s; the previous CA file was restored", reason)
}

func (m *Manager) irPool(cfg config.Config) (*x509.CertPool, []*x509.Certificate, error) {
	b, err := os.ReadFile(cfg.TLS.CAFile)
	if err != nil {
		return nil, nil, err
	}
	cs, err := parseCerts(b)
	if err != nil {
		return nil, nil, err
	}
	return poolOf(cs), cs, nil
}

func (m *Manager) irEvidence(ctx context.Context, cfg config.Config, r Rotation, step, detail string) (string, error) {
	e := m.evidence(ctx, r, step, detail)
	if b, err := os.ReadFile(cfg.TLS.CAFile); err == nil {
		e.CAFileSHA256 = shaHex(b)
		if cs, err := parseCerts(b); err == nil {
			e.TrustedCAs = certSHAs(cs)
			e.NewTrusted = contains(e.TrustedCAs, r.NewCASHA256)
			if old, err := os.ReadFile(m.rotBackup(r.ID, "ca.pem")); err == nil {
				if ocs, err := parseCerts(old); err == nil {
					e.OldTrusted = true
					for _, s := range certSHAs(ocs) {
						e.OldTrusted = e.OldTrusted && contains(e.TrustedCAs, s)
					}
				}
			}
		}
	}
	if pool, _, err := m.irPool(cfg); err == nil {
		if leaf, err := m.probeServed(ctx, cfg, r, pool); err == nil {
			e.ServedCertSHA256, e.ServesNew = leaf, leaf == r.NewCertSHA256 && leaf != ""
		}
	}
	return evidenceJSON(e), nil
}

// ---- VERIFY ----

// RotateVerifyEX checks that the staged set is intact, chains to the new CA
// for the certificate name, and that the live set is still the one the
// rotation started from.
func (m *Manager) RotateVerifyEX(ctx context.Context, rid string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, _, err := m.rotFor(rid, RoleEX)
	if err != nil {
		return "", err
	}
	if r.Phase != RotStaged {
		return "", fmt.Errorf("rotation %s is %s, expected staged", rid, r.Phase)
	}
	if !sameDigests(pkiDigests(m.pkiNext(rid)), r.NewPKI) {
		return "", errors.New("the staged certificates changed since PREPARE")
	}
	if !sameDigests(pkiDigests(m.pkiDir()), r.OldPKI) {
		return "", errors.New("the live certificates changed outside BAFT since PREPARE")
	}
	ca, err := readLeaf(filepath.Join(m.pkiNext(rid), "ca.pem"))
	if err != nil {
		return "", err
	}
	leaf, err := readLeaf(filepath.Join(m.pkiNext(rid), "server.pem"))
	if err != nil {
		return "", err
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: poolOf([]*x509.Certificate{ca}), DNSName: r.Host}); err != nil {
		return "", fmt.Errorf("the staged certificate does not verify: %w", err)
	}
	if _, err := tls.LoadX509KeyPair(filepath.Join(m.pkiNext(rid), "server.pem"), filepath.Join(m.pkiNext(rid), "server.key")); err != nil {
		return "", fmt.Errorf("the staged key does not match: %w", err)
	}
	r.Verified = true
	if err := m.writeRot(&r); err != nil {
		return "", err
	}
	e := m.evidence(ctx, r, "verify", "staged set intact and valid for "+r.Host+"; live set unchanged")
	if live, err := readLeaf(filepath.Join(m.pkiDir(), "server.pem")); err == nil {
		e.LiveCertSHA256 = shaHex(live.Raw)
	}
	return evidenceJSON(e), nil
}

// RotateVerifyIR proves, before the EX activates, that the IR's live trust
// accepts both the certificate served now and the new one.
func (m *Manager) RotateVerifyIR(ctx context.Context, rid string, certDER []byte, certSHA string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, cfg, err := m.rotFor(rid, RoleIR)
	if err != nil {
		return "", err
	}
	if r.Phase != RotTrusting || !r.TrustLoaded {
		return "", fmt.Errorf("rotation %s is %s, not trusting old + new", rid, r.Phase)
	}
	if !hexRe.MatchString(certSHA) || shaHex(certDER) != certSHA {
		return "", errors.New("the new certificate does not match its digest")
	}
	leaf, err := x509.ParseCertificate(certDER)
	if err != nil {
		return "", err
	}
	live, err := os.ReadFile(cfg.TLS.CAFile)
	if err != nil {
		return "", err
	}
	if shaHex(live) != r.InstalledCAFileSHA256 {
		return "", errors.New("the CA file is not the bundle BAFT installed")
	}
	pool, _, err := m.irPool(cfg)
	if err != nil {
		return "", err
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: cfg.Peer.ServerName}); err != nil {
		return "", fmt.Errorf("the new certificate would not be accepted: %w", err)
	}
	newCA, err := readLeaf(filepath.Join(m.rotDir(rid), "new-ca.pem"))
	if err != nil {
		return "", err
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: poolOf([]*x509.Certificate{newCA}), DNSName: cfg.Peer.ServerName}); err != nil {
		return "", fmt.Errorf("the new certificate is not issued by the new CA: %w", err)
	}
	served, err := m.probeServed(ctx, cfg, r, pool)
	if err != nil {
		return "", fmt.Errorf("the certificate served now does not verify: %w", err)
	}
	if state, _ := m.System.Systemctl(ctx, "is-active", m.Service); state != "active" {
		return "", fmt.Errorf("%s is %q", m.Service, state)
	}
	r.NewCertSHA256, r.Verified = certSHA, true
	if err := m.writeRot(&r); err != nil {
		return "", err
	}
	out, _ := m.irEvidence(ctx, cfg, r, "verify", "new certificate accepted by the live trust; served certificate "+served[:12]+" still accepted")
	return out, nil
}

// ---- EX: ACTIVATE ----

// RotateActivateEX swaps the staged set in, restarts the service and proves
// it serves the new certificate. On any failure the old set is put back and
// the service restarted on it.
func (m *Manager) RotateActivateEX(ctx context.Context, rid string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, cfg, err := m.rotFor(rid, RoleEX)
	if err != nil {
		return "", err
	}
	switch r.Phase {
	case RotActive:
		return m.exEvidence(ctx, cfg, r, "activate", "already serving the new certificate")
	case RotStaged:
		if !r.Verified {
			return "", fmt.Errorf("rotation %s was not verified", rid)
		}
	case RotActivating:
	default:
		return "", fmt.Errorf("rotation %s is %s", rid, r.Phase)
	}
	live := pkiDigests(m.pkiDir())
	switch {
	case sameDigests(live, r.OldPKI):
		if !sameDigests(pkiDigests(m.pkiNext(rid)), r.NewPKI) {
			return "", errors.New("the staged certificates changed since PREPARE")
		}
		r.Phase = RotActivating
		if err := m.writeRot(&r); err != nil {
			return "", err
		}
		if err := m.fault("activate:before-swap"); err != nil {
			return "", err
		}
		if err := exchangeDirs(m.pkiDir(), m.pkiNext(rid)); err != nil {
			return "", fmt.Errorf("swapping the certificate set: %w", err)
		}
		if err := m.fault("activate:after-swap"); err != nil {
			return "", err
		}
	case sameDigests(live, r.NewPKI) && r.Phase == RotActivating:
		// Swapped before an interruption.
	default:
		return "", errors.New("the live certificates changed outside BAFT; nothing was changed")
	}
	if _, err := os.Stat(m.pkiPrev(rid)); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(m.pkiNext(rid), m.pkiPrev(rid)); err != nil {
			return "", err
		}
	}
	if err := m.fault("activate:before-restart"); err != nil {
		return "", err
	}
	if err := m.restartService(ctx); err != nil {
		return "", m.exUndoActivate(ctx, cfg, &r, "the service did not come up with the new certificate: "+err.Error())
	}
	if err := m.fault("activate:after-restart"); err != nil {
		return "", err
	}
	newCA, err := readLeaf(filepath.Join(m.rotDir(rid), "new-ca.pem"))
	if err != nil {
		return "", m.exUndoActivate(ctx, cfg, &r, err.Error())
	}
	served, err := m.probeServed(ctx, cfg, r, poolOf([]*x509.Certificate{newCA}))
	if err != nil || served != r.NewCertSHA256 {
		if err == nil {
			err = fmt.Errorf("it serves %s", served)
		}
		return "", m.exUndoActivate(ctx, cfg, &r, "the listener does not serve the new certificate: "+err.Error())
	}
	r.Phase, r.Activated = RotActive, m.Now().UTC()
	if err := m.writeRot(&r); err != nil {
		return "", err
	}
	return m.exEvidence(ctx, cfg, r, "activate", "serving the new certificate; the old set is kept until RETIRE")
}

// exUndoActivate swaps the old set back after a failed activation and
// leaves the rotation staged, as before ACTIVATE.
func (m *Manager) exUndoActivate(ctx context.Context, cfg config.Config, r *Rotation, reason string) error {
	err := exchangeDirs(m.pkiDir(), m.pkiPrev(r.ID))
	if err == nil {
		err = os.Rename(m.pkiPrev(r.ID), m.pkiNext(r.ID))
	}
	if err == nil {
		err = m.restartService(ctx)
	}
	if err != nil {
		return fmt.Errorf("%s; restoring the previous certificate failed: %v", reason, err)
	}
	r.Phase = RotStaged
	_ = m.writeRot(r)
	return fmt.Errorf("%s; the previous certificate was restored", reason)
}

func (m *Manager) exEvidence(ctx context.Context, cfg config.Config, r Rotation, step, detail string) (string, error) {
	e := m.evidence(ctx, r, step, detail)
	if live, err := readLeaf(filepath.Join(m.pkiDir(), "server.pem")); err == nil {
		e.LiveCertSHA256 = shaHex(live.Raw)
	}
	if cas, err := os.ReadFile(filepath.Join(m.pkiDir(), "ca.pem")); err == nil {
		if cs, err := parseCerts(cas); err == nil {
			if leaf, err := m.probeServed(ctx, cfg, r, poolOf(cs)); err == nil {
				e.ServedCertSHA256, e.ServesNew = leaf, leaf == r.NewCertSHA256
			}
		}
	}
	return evidenceJSON(e), nil
}

// ---- IR: CONFIRM ----

// RotateConfirmIR proves over the real path that the EX serves the new
// certificate, that the IR's live trust accepts it, and that the tunnel is
// up.
func (m *Manager) RotateConfirmIR(ctx context.Context, rid, certSHA string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, cfg, err := m.rotFor(rid, RoleIR)
	if err != nil {
		return "", err
	}
	if !((r.Phase == RotTrusting && r.Verified) || r.Phase == RotConfirmed) {
		return "", fmt.Errorf("rotation %s is %s, not verified", rid, r.Phase)
	}
	if certSHA != r.NewCertSHA256 {
		return "", errors.New("that is not the certificate this rotation verified")
	}
	pool, _, err := m.irPool(cfg)
	if err != nil {
		return "", err
	}
	served, err := m.probeServed(ctx, cfg, r, pool)
	if err != nil {
		return "", fmt.Errorf("the EX's certificate does not verify: %w", err)
	}
	if served != certSHA {
		return "", fmt.Errorf("the EX serves %s, not the new certificate", served)
	}
	r0, err := m.stableSample(ctx)
	if err != nil {
		return "", err
	}
	if len(cfg.Routes) > 0 {
		c, err := net.DialTimeout("tcp", cfg.Routes[0].Listen, 3*time.Second)
		if err != nil {
			return "", fmt.Errorf("the tunnel route %s does not accept connections: %v", cfg.Routes[0].Listen, err)
		}
		c.Close()
	}
	if r1, err := m.stableSample(ctx); err != nil || r1 != r0 {
		return "", fmt.Errorf("%s restarted during the check", m.Service)
	}
	r.Phase = RotConfirmed
	if err := m.writeRot(&r); err != nil {
		return "", err
	}
	return m.irEvidence(ctx, cfg, r, "confirm", "the new certificate is served and accepted; the tunnel route accepts connections")
}

// ---- RETIRE_OLD ----

// RotateRetireIR makes the IR trust the new CA only. It runs only after
// CONFIRM and only while the EX still serves the new certificate; on
// failure the old + new bundle is put back.
func (m *Manager) RotateRetireIR(ctx context.Context, rid string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, cfg, err := m.rotFor(rid, RoleIR)
	if err != nil {
		return "", err
	}
	newPEM, err := os.ReadFile(filepath.Join(m.rotDir(rid), "new-ca.pem"))
	if err != nil {
		return "", err
	}
	newCerts, err := parseCerts(newPEM)
	if err != nil {
		return "", err
	}
	newPool := poolOf(newCerts)
	switch r.Phase {
	case RotRetired:
		return m.irEvidence(ctx, cfg, r, "retire", "already trusts the new CA only")
	case RotConfirmed, RotRetiring:
	default:
		return "", fmt.Errorf("rotation %s is %s, not confirmed", rid, r.Phase)
	}
	live, err := os.ReadFile(cfg.TLS.CAFile)
	if err != nil {
		return "", err
	}
	switch shaHex(live) {
	case r.InstalledCAFileSHA256:
		// The EX must still serve the new certificate, or dropping the old
		// CA would lock the IR out.
		served, err := m.probeServed(ctx, cfg, r, newPool)
		if err != nil || served != r.NewCertSHA256 {
			return "", fmt.Errorf("the EX does not serve the new certificate now (%v); the old CA stays trusted", err)
		}
		r.Phase = RotRetiring
		if err := m.writeRot(&r); err != nil {
			return "", err
		}
		if err := m.fault("retire-ir:before-write"); err != nil {
			return "", err
		}
		if err := writeLike(cfg.TLS.CAFile, newPEM); err != nil {
			return "", err
		}
	case shaHex(newPEM):
		if r.Phase != RotRetiring {
			return "", errors.New("the CA file already holds only the new CA, but the rotation is not retiring")
		}
	default:
		return "", errors.New("the CA file was changed outside BAFT; nothing was changed")
	}
	if err := m.fault("retire-ir:after-write"); err != nil {
		return "", err
	}
	err = m.restartService(ctx)
	if err == nil {
		var served string
		served, err = m.probeServed(ctx, cfg, r, newPool)
		if err == nil && served != r.NewCertSHA256 {
			err = fmt.Errorf("the EX serves %s", served)
		}
	}
	if err != nil {
		bundle, berr := os.ReadFile(filepath.Join(m.rotDir(rid), "bundle.pem"))
		if berr == nil {
			berr = writeLike(cfg.TLS.CAFile, bundle)
		}
		if berr == nil {
			berr = m.restartService(ctx)
		}
		if berr != nil {
			return "", fmt.Errorf("retire failed: %v; restoring old + new trust failed: %v", err, berr)
		}
		r.Phase = RotConfirmed
		_ = m.writeRot(&r)
		return "", fmt.Errorf("retire failed: %v; old + new trust was restored", err)
	}
	r.Phase, r.Retired = RotRetired, m.Now().UTC()
	if err := m.writeRot(&r); err != nil {
		return "", err
	}
	_ = writeFile(m.rotEpochPath(), []byte(strconv.Itoa(r.Epoch)+"\n"), 0o600)
	_ = os.RemoveAll(filepath.Join(m.rotDir(rid), "backup"))
	_ = os.Remove(filepath.Join(m.rotDir(rid), "bundle.pem"))
	m.clearRotActive(rid)
	return m.irEvidence(ctx, cfg, r, "retire", "trusts the new CA only")
}

// RotateRetireEX destroys the old certificate set. After this the rotation
// cannot be rolled back.
func (m *Manager) RotateRetireEX(ctx context.Context, rid string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, cfg, err := m.rotFor(rid, RoleEX)
	if err != nil {
		return "", err
	}
	switch r.Phase {
	case RotRetired:
		return m.exEvidence(ctx, cfg, r, "retire", "the old certificate set is already gone")
	case RotActive, RotRetiring:
	default:
		return "", fmt.Errorf("rotation %s is %s, not active", rid, r.Phase)
	}
	if !sameDigests(pkiDigests(m.pkiDir()), r.NewPKI) {
		return "", errors.New("the live certificates are not the rotated set; nothing was changed")
	}
	r.Phase = RotRetiring
	if err := m.writeRot(&r); err != nil {
		return "", err
	}
	if err := m.fault("retire-ex:before-delete"); err != nil {
		return "", err
	}
	for _, d := range []string{m.pkiPrev(rid), m.pkiNext(rid)} {
		if err := os.RemoveAll(d); err != nil {
			return "", err
		}
	}
	r.Phase, r.Retired = RotRetired, m.Now().UTC()
	if err := m.writeRot(&r); err != nil {
		return "", err
	}
	_ = writeFile(m.rotEpochPath(), []byte(strconv.Itoa(r.Epoch)+"\n"), 0o600)
	m.clearRotActive(rid)
	return m.exEvidence(ctx, cfg, r, "retire", "the old certificate set was destroyed")
}

// ---- ROLLBACK ----

// RotateRollback undoes a rotation on this node from any point before
// RETIRE. The EX goes first (back to the old certificate while the IR still
// trusts both); the IR drops the new CA only when it sees the EX serving a
// certificate its old trust accepts, or when BCC states the EX never
// reached ACTIVATE (exNeverActivated) and the EX is not seen serving the new
// one.
func (m *Manager) RotateRollback(ctx context.Context, rid string, exNeverActivated bool) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.readRot(rid)
	if err != nil {
		return "nothing to roll back", nil
	}
	switch r.Phase {
	case RotRolledBack:
		return "already rolled back", nil
	case RotRetiring, RotRetired:
		return "", fmt.Errorf("rotation %s is %s: the old certificate is being or was retired; rotate again instead", rid, r.Phase)
	}
	cfg, err := m.rotationTarget(r.TunnelID, r.Role)
	if err != nil {
		return "", err
	}
	if r.Role == RoleEX {
		return m.exRollback(ctx, cfg, r)
	}
	return m.irRollback(ctx, cfg, r, exNeverActivated)
}

func (m *Manager) exRollback(ctx context.Context, cfg config.Config, r Rotation) (string, error) {
	live := pkiDigests(m.pkiDir())
	restarted := false
	switch {
	case sameDigests(live, r.OldPKI):
		// Never swapped, or swapped back already: drop the new set.
		if r.Phase == RotActivating || r.Phase == RotActive {
			if err := m.restartService(ctx); err != nil {
				return "", fmt.Errorf("rollback incomplete: %v", err)
			}
			restarted = true
		}
	case sameDigests(live, r.NewPKI) && (r.Phase == RotActivating || r.Phase == RotActive):
		oldDir := ""
		for _, d := range []string{m.pkiPrev(r.ID), m.pkiNext(r.ID)} {
			if sameDigests(pkiDigests(d), r.OldPKI) {
				oldDir = d
			}
		}
		if oldDir == "" {
			return "", errors.New("rollback refused: the previous certificate set is missing")
		}
		if err := m.fault("rollback-ex:before-swap"); err != nil {
			return "", err
		}
		if err := exchangeDirs(m.pkiDir(), oldDir); err != nil {
			return "", fmt.Errorf("rollback incomplete: %v", err)
		}
		if err := m.restartService(ctx); err != nil {
			return "", fmt.Errorf("rollback incomplete: %v", err)
		}
		restarted = true
	default:
		return "", errors.New("rollback refused: the live certificates changed outside BAFT; nothing was changed")
	}
	if restarted {
		cs, err := parseCertsFile(filepath.Join(m.pkiDir(), "ca.pem"))
		if err == nil {
			var served string
			served, err = m.probeServed(ctx, cfg, r, poolOf(cs))
			if err == nil && served != r.OldCertSHA256 {
				err = fmt.Errorf("it serves %s", served)
			}
		}
		if err != nil {
			return "", fmt.Errorf("rollback incomplete: the listener does not serve the previous certificate: %v", err)
		}
	}
	// Both names carry this rotation's id; the live set is in pki.
	for _, d := range []string{m.pkiPrev(r.ID), m.pkiNext(r.ID)} {
		_ = os.RemoveAll(d)
	}
	r.Phase = RotRolledBack
	if err := m.writeRot(&r); err != nil {
		return "", err
	}
	m.clearRotActive(r.ID)
	return "rolled back: serving the previous certificate", nil
}

func parseCertsFile(path string) ([]*x509.Certificate, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseCerts(b)
}

func (m *Manager) irRollback(ctx context.Context, cfg config.Config, r Rotation, exNeverActivated bool) (string, error) {
	live, err := os.ReadFile(cfg.TLS.CAFile)
	if err != nil {
		return "", err
	}
	old, err := os.ReadFile(m.rotBackup(r.ID, "ca.pem"))
	if err != nil {
		return "", fmt.Errorf("rollback refused: the previous CA file is missing: %v", err)
	}
	switch shaHex(live) {
	case r.OldCAFileSHA256:
		if r.TrustLoaded {
			if err := m.restartService(ctx); err != nil {
				return "", fmt.Errorf("rollback incomplete: %v", err)
			}
		}
	case r.InstalledCAFileSHA256:
		// Never drop the new CA while the EX might serve the new certificate.
		oldCerts, err := parseCerts(old)
		if err != nil {
			return "", err
		}
		pool, _, err := m.irPool(cfg)
		if err != nil {
			return "", err
		}
		served, perr := m.probeServed(ctx, cfg, r, pool)
		switch {
		case perr == nil && r.NewCertSHA256 != "" && served == r.NewCertSHA256:
			return "", errors.New("rollback refused: the EX still serves the new certificate; roll the EX back first")
		case perr == nil:
			if _, err := m.probeServed(ctx, cfg, r, poolOf(oldCerts)); err != nil {
				return "", fmt.Errorf("rollback refused: the certificate the EX serves is not accepted by the previous trust: %v", err)
			}
		case !exNeverActivated:
			return "", fmt.Errorf("rollback refused: cannot see which certificate the EX serves (%v); old + new stay trusted", perr)
		}
		if err := m.fault("rollback-ir:before-write"); err != nil {
			return "", err
		}
		if err := writeLike(cfg.TLS.CAFile, old); err != nil {
			return "", err
		}
		if err := m.restartService(ctx); err != nil {
			return "", fmt.Errorf("rollback incomplete: %v", err)
		}
	default:
		return "", errors.New("rollback refused: the CA file was changed outside BAFT; nothing was changed")
	}
	r.Phase = RotRolledBack
	if err := m.writeRot(&r); err != nil {
		return "", err
	}
	m.clearRotActive(r.ID)
	return "rolled back: trusts the previous CA only", nil
}

// RotationState reads a rotation record (for tests and evidence).
func (m *Manager) RotationState(rid string) (Rotation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.readRot(rid)
}

// DecodeDER decodes a base64url certificate parameter.
func DecodeDER(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
