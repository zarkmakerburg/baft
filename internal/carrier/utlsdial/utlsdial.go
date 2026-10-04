// Package utlsdial dials a TLS connection that presents a browser-fidelity
// ClientHello (via refraction-networking/utls) instead of Go's distinctive
// crypto/tls ClientHello, while still authenticating — and optionally pinning —
// the server. It closes the passive-fingerprint gap measured in
// reports/CARRIER-CAMOUFLAGE.md: Go's ClientHello advertises only three cipher
// suites and never emits GREASE, which a censor can flag almost for free; a
// parroted Chrome ClientHello blends into ordinary HTTPS.
//
// The dialer is transport-agnostic: it returns a net.Conn carrying the
// negotiated TLS session, so it can back either the HTTP/2 carrier or the
// WebSocket carrier. The inner Noise IK handshake is unchanged and remains the
// end-to-end authenticator; this only reshapes the outer TLS fingerprint.
package utlsdial

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"time"

	utls "github.com/refraction-networking/utls"
)

// Config configures a uTLS dial.
type Config struct {
	// ServerName is the SNI and the name verified against the server cert.
	ServerName string
	// RootCAs authenticates the server. Required.
	RootCAs *x509.CertPool
	// ClientCert, when set, is offered for mutual TLS (direct/Reality carrier).
	// Under TLS 1.3 the client Certificate is encrypted, so offering it does not
	// alter the observable ClientHello fingerprint.
	ClientCert *tls.Certificate
	// NextProtos is the ALPN list (e.g. {"h2"} or {"http/1.1"}).
	NextProtos []string
	// Pin, when set, is called with the verified server leaf; a non-nil return
	// aborts the connection. Use it to pin the responder certificate for the
	// direct carrier.
	Pin func(leaf *x509.Certificate) error
	// Hello selects the browser ClientHello profile. The zero value parrots the
	// current Chrome.
	Hello utls.ClientHelloID
	// HandshakeTimeout bounds TCP + TLS. Zero means 15s.
	HandshakeTimeout time.Duration
	// NetDial, when set, dials the raw TCP connection (tests inject this).
	NetDial func(ctx context.Context, network, addr string) (net.Conn, error)
}

func (c Config) hello() utls.ClientHelloID {
	if c.Hello.Client == "" {
		// Chrome 120's classic ClientHello (GREASE + full browser cipher/extension
		// set, X25519 key share) negotiates cleanly with a TLS 1.3 server. The
		// newest Auto profile sends post-quantum key shares that a TLS 1.3-only
		// Go server rejects, so it is not the default.
		return utls.HelloChrome_120
	}
	return c.Hello
}

// Dial establishes a uTLS connection to addr and returns it after the server is
// authenticated. The returned conn is a *utls.UConn (a net.Conn).
func Dial(ctx context.Context, network, addr string, cfg Config) (net.Conn, error) {
	if cfg.ServerName == "" || cfg.RootCAs == nil {
		return nil, errors.New("utlsdial: server name and root CAs are required")
	}
	timeout := cfg.HandshakeTimeout
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	dialRaw := cfg.NetDial
	if dialRaw == nil {
		dialRaw = (&net.Dialer{}).DialContext
	}
	raw, err := dialRaw(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = raw.Close()
		}
	}()
	if dl, has := ctx.Deadline(); has {
		_ = raw.SetDeadline(dl)
	}

	tconn, err := Handshake(ctx, raw, cfg)
	if err != nil {
		return nil, err
	}
	_ = raw.SetDeadline(time.Time{})
	ok = true
	return tconn, nil
}

// Handshake performs the uTLS (browser-fidelity ClientHello) handshake over an
// existing connection and returns the TLS connection once the server is
// authenticated. The caller owns conn and must close it on error. This lets a
// transport that already holds a raw connection (such as the WebSocket carrier)
// reshape its TLS fingerprint without utlsdial doing the dialing.
func Handshake(ctx context.Context, conn net.Conn, cfg Config) (net.Conn, error) {
	if cfg.ServerName == "" || cfg.RootCAs == nil {
		return nil, errors.New("utlsdial: server name and root CAs are required")
	}
	uconn := utls.UClient(conn, utlsConfig(cfg), cfg.hello())
	if err := applyRequestedALPN(uconn, cfg.NextProtos); err != nil {
		return nil, fmt.Errorf("utlsdial: build client hello: %w", err)
	}
	if err := uconn.HandshakeContext(ctx); err != nil {
		return nil, fmt.Errorf("utlsdial: handshake: %w", err)
	}
	// Standard verification ran during the handshake (InsecureSkipVerify is
	// false); the optional pin is an additional, explicit check.
	if cfg.Pin != nil {
		cs := uconn.ConnectionState()
		if len(cs.PeerCertificates) == 0 {
			return nil, errors.New("utlsdial: no peer certificate")
		}
		if err := cfg.Pin(cs.PeerCertificates[0]); err != nil {
			return nil, fmt.Errorf("utlsdial: pin: %w", err)
		}
	}
	return uconn, nil
}

// applyRequestedALPN keeps the selected browser preset but replaces only its
// ALPN extension when BAFT explicitly requests a protocol list. uTLS presets
// apply their own ALPN and write it back into Config.NextProtos, so setting
// Config.NextProtos alone is not sufficient.
func applyRequestedALPN(uconn *utls.UConn, next []string) error {
	if err := uconn.BuildHandshakeState(); err != nil {
		return err
	}
	if len(next) == 0 {
		return nil
	}
	want := append([]string(nil), next...)
	found := false
	for _, ext := range uconn.Extensions {
		if alpn, ok := ext.(*utls.ALPNExtension); ok {
			alpn.AlpnProtocols = want
			found = true
			break
		}
	}
	if !found {
		return errors.New("utlsdial: selected ClientHello preset has no ALPN extension")
	}
	// Re-apply the edited extension to both the public ClientHello and the
	// internal TLS config. HandshakeContext may call BuildHandshakeState again;
	// the edited extension remains authoritative on subsequent calls.
	return uconn.BuildHandshakeState()
}

func utlsConfig(cfg Config) *utls.Config {
	u := &utls.Config{
		ServerName: cfg.ServerName,
		RootCAs:    cfg.RootCAs,
		NextProtos: cfg.NextProtos,
		// Browsers advertise TLS 1.2 + 1.3; the parroted ClientHello reflects
		// that. Verification still requires a valid chain for ServerName.
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: false,
	}
	if cfg.ClientCert != nil {
		u.Certificates = []utls.Certificate{toUTLSCert(*cfg.ClientCert)}
	}
	return u
}

func toUTLSCert(c tls.Certificate) utls.Certificate {
	// Only the fields needed to present a client certificate for mutual TLS; the
	// rest (OCSP, SCTs) are not used by the BAFT carrier and carry utls-specific
	// element types.
	return utls.Certificate{
		Certificate: c.Certificate,
		PrivateKey:  c.PrivateKey,
		Leaf:        c.Leaf,
	}
}

// CaptureClientHello builds the ClientHello bytes the dial would send, without
// performing any network I/O. It is used by the camouflage measurement to
// compute the JA3 of the parroted fingerprint.
func CaptureClientHello(cfg Config) ([]byte, error) {
	if cfg.ServerName == "" {
		return nil, errors.New("utlsdial: server name is required")
	}
	// A pipe gives UClient a net.Conn without opening a socket; only the
	// in-memory handshake state is built.
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	ucfg := utlsConfig(cfg)
	if ucfg.RootCAs == nil {
		// Capture does not verify; a nil pool is fine here.
		ucfg.InsecureSkipVerify = true
	}
	uconn := utls.UClient(c1, ucfg, cfg.hello())
	if err := applyRequestedALPN(uconn, cfg.NextProtos); err != nil {
		return nil, err
	}
	raw := uconn.HandshakeState.Hello.Raw
	out := make([]byte, len(raw))
	copy(out, raw)
	return out, nil
}
