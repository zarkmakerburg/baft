package ws

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// wsGUID is the RFC 6455 magic value used to derive Sec-WebSocket-Accept.
const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// Dialer opens a WebSocket carrier connection to an endpoint. One Dialer may be
// reused; each Dial creates an independent TCP/TLS connection.
type Dialer struct {
	// Endpoint is wss://host[:port]/path. The path defaults to CarrierPath when
	// only a scheme+host is given.
	Endpoint string
	// TLSConfig authenticates the server (the outer TLS layer). Behind
	// Cloudflare this trusts Cloudflare's edge certificate; the inner Noise
	// handshake provides the real end-to-end authentication.
	TLSConfig *tls.Config
	// Header carries extra request headers (e.g. a browser-like User-Agent and
	// Origin) so the upgrade blends into ordinary WebSocket traffic.
	Header http.Header
	// HandshakeTimeout bounds the TCP+TLS+upgrade handshake. Zero means 15s.
	HandshakeTimeout time.Duration
	// NetDial, when set, dials the raw TCP connection (tests inject this).
	NetDial func(ctx context.Context, network, addr string) (net.Conn, error)
	// TLSHandshake, when set, performs the TLS handshake over the raw
	// connection instead of crypto/tls (e.g. a uTLS browser-fingerprint
	// handshake). It must authenticate the server for serverName and return the
	// established TLS connection; on error the caller closes the raw conn.
	TLSHandshake func(ctx context.Context, conn net.Conn, serverName string) (net.Conn, error)
}

// Dial performs the WebSocket handshake and returns a client Conn.
func (d *Dialer) Dial(ctx context.Context) (*Conn, error) {
	if d.Endpoint == "" || d.TLSConfig == nil {
		return nil, errors.New("ws: endpoint and TLS config are required")
	}
	u, err := url.Parse(d.Endpoint)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "wss" && u.Scheme != "https" {
		return nil, fmt.Errorf("ws: endpoint must be wss://, got %q", u.Scheme)
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "443")
	}
	path := u.Path
	if path == "" {
		path = CarrierPath
	}
	timeout := d.HandshakeTimeout
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	dialRaw := d.NetDial
	if dialRaw == nil {
		dialRaw = (&net.Dialer{}).DialContext
	}
	raw, err := dialRaw(ctx, "tcp", host)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = raw.Close()
		}
	}()
	if dl, hasDL := ctx.Deadline(); hasDL {
		_ = raw.SetDeadline(dl)
	}

	serverName := d.TLSConfig.ServerName
	if serverName == "" {
		serverName = u.Hostname()
	}
	var tlsConn net.Conn
	if d.TLSHandshake != nil {
		tlsConn, err = d.TLSHandshake(ctx, raw, serverName)
		if err != nil {
			return nil, err
		}
	} else {
		tlsCfg := d.TLSConfig.Clone()
		tlsCfg.ServerName = serverName
		tc := tls.Client(raw, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		tlsConn = tc
	}

	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)

	var sb strings.Builder
	fmt.Fprintf(&sb, "GET %s HTTP/1.1\r\n", path)
	fmt.Fprintf(&sb, "Host: %s\r\n", u.Host)
	sb.WriteString("Upgrade: websocket\r\n")
	sb.WriteString("Connection: Upgrade\r\n")
	fmt.Fprintf(&sb, "Sec-WebSocket-Key: %s\r\n", key)
	sb.WriteString("Sec-WebSocket-Version: 13\r\n")
	for k, vs := range d.Header {
		// Never allow the caller to override the upgrade-defining headers.
		switch http.CanonicalHeaderKey(k) {
		case "Upgrade", "Connection", "Sec-Websocket-Key", "Sec-Websocket-Version", "Host":
			continue
		}
		for _, v := range vs {
			fmt.Fprintf(&sb, "%s: %s\r\n", k, v)
		}
	}
	sb.WriteString("\r\n")
	if _, err := tlsConn.Write([]byte(sb.String())); err != nil {
		return nil, err
	}

	br := bufio.NewReaderSize(tlsConn, 16*1024)
	req, _ := http.NewRequest(http.MethodGet, d.Endpoint, nil)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return nil, fmt.Errorf("ws: handshake failed: HTTP %d", resp.StatusCode)
	}
	if !strings.EqualFold(resp.Header.Get("Upgrade"), "websocket") ||
		!headerContainsToken(resp.Header.Get("Connection"), "upgrade") {
		return nil, errors.New("ws: server did not upgrade")
	}
	if resp.Header.Get("Sec-WebSocket-Accept") != acceptKey(key) {
		return nil, errors.New("ws: bad Sec-WebSocket-Accept")
	}
	// Clear the handshake deadline before handing the stream to the caller.
	_ = raw.SetDeadline(time.Time{})
	ok = true
	return newConn(tlsConn, br, true), nil
}

func acceptKey(key string) string {
	h := sha1.New() //nolint:gosec // RFC 6455 mandates SHA-1 for the accept token
	h.Write([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func headerContainsToken(header, token string) bool {
	for _, part := range strings.Split(header, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}
