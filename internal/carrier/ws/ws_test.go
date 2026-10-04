package ws

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	h2 "github.com/zarkmakerburg/baft/internal/carrier/h2"
	"github.com/zarkmakerburg/baft/internal/identity"
	"github.com/zarkmakerburg/baft/internal/recordshape"
	"github.com/zarkmakerburg/baft/internal/securityinternal"
)

// carrierPathMatchesH2 guards against the two transports drifting apart.
func TestCarrierPathMatchesH2(t *testing.T) {
	if CarrierPath != h2.CarrierPath {
		t.Fatalf("ws CarrierPath %q != h2 %q", CarrierPath, h2.CarrierPath)
	}
}

func newServer(t *testing.T, h http.Handler) (*httptest.Server, *tls.Config) {
	t.Helper()
	s := httptest.NewUnstartedServer(h)
	s.StartTLS() // HTTP/1.1 only; required for WebSocket hijacking
	t.Cleanup(s.Close)
	tr := s.Client().Transport.(*http.Transport).Clone()
	return s, tr.TLSClientConfig.Clone()
}

func echoHandler(calls *atomic.Int32, wantIdentity string, t *testing.T) StreamHandler {
	return func(ctx context.Context, in io.Reader, out io.Writer, p PeerInfo) error {
		calls.Add(1)
		if wantIdentity != "" && p.Identity != wantIdentity {
			t.Errorf("identity = %q, want %q", p.Identity, wantIdentity)
		}
		_, err := io.Copy(out, in)
		return err
	}
}

func TestWSNoiseEchoAndProbeResistance(t *testing.T) {
	ik, err := securityinternal.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	rk, err := securityinternal.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	shape := recordshape.DefaultConfig(true)
	serverCfg := securityinternal.HandshakeConfig{Static: rk, PeerStatic: ik.Public, RecordShaping: shape}
	revoked := identity.NewRevocationSet()
	var calls atomic.Int32
	h, err := HandlerWithNoise(echoHandler(&calls, "urn:baft:node:ir", t), NoiseOptions{
		Handshake:        serverCfg,
		PeerIdentity:     "urn:baft:node:ir",
		Revocations:      revoked,
		HandshakeTimeout: 500 * time.Millisecond,
		MaxPending:       4,
	})
	if err != nil {
		t.Fatal(err)
	}
	s, clientTLS := newServer(t, h)

	// Probes: a plain GET and a GET to the carrier path (no upgrade) both get
	// the ordinary cover page and never reach the authenticated stream.
	hc := s.Client()
	hc.Timeout = 2 * time.Second
	var baseline string
	for i, path := range []string{"/", CarrierPath, "/anything"} {
		resp, err := hc.Get(s.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/html; charset=utf-8" {
			t.Fatalf("probe %d: status %d ct %q", i, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		if i == 0 {
			baseline = string(b)
		} else if string(b) != baseline {
			t.Fatal("probe response differs from ordinary site")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("probe reached authenticated stream")
	}

	// Valid carrier dial: echo of several payload sizes, including sizes that
	// cross the write-frame boundary and the 16-bit length boundary.
	clientCfg := securityinternal.HandshakeConfig{Static: ik, PeerStatic: rk.Public, RecordShaping: shape}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	d := &Dialer{Endpoint: s.URL, TLSConfig: clientTLS}
	conn, sc, err := d.OpenNoise(ctx, clientCfg)
	if err != nil {
		t.Fatalf("OpenNoise: %v", err)
	}
	defer conn.Close()
	for _, n := range []int{1, 100, writeFramePayload - 1, writeFramePayload, writeFramePayload + 7, 70000} {
		msg := bytes.Repeat([]byte{byte(n), byte(n >> 8), 0xAB}, (n/3)+1)[:n]
		if _, err := sc.Write(msg); err != nil {
			t.Fatalf("write %d: %v", n, err)
		}
		got := make([]byte, n)
		if _, err := io.ReadFull(sc, got); err != nil {
			t.Fatalf("read %d: %v", n, err)
		}
		if !bytes.Equal(got, msg) {
			t.Fatalf("echo mismatch at size %d", n)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("stream calls = %d, want 1", calls.Load())
	}

	// Unknown static key must be rejected (no authenticated session).
	bad, _ := securityinternal.GenerateKeyPair()
	badCfg := clientCfg
	badCfg.Static = bad
	if _, _, err := d.OpenNoise(ctx, badCfg); err == nil {
		t.Fatal("unknown Noise peer accepted")
	}

	// Revoked peer must be rejected and never reach the stream.
	revoked.RevokeIdentity("urn:baft:node:ir")
	if _, _, err := d.OpenNoise(ctx, clientCfg); err == nil {
		t.Fatal("revoked peer accepted")
	}
	if calls.Load() != 1 {
		t.Fatalf("rejected identity reached stream (calls=%d)", calls.Load())
	}
}

func TestWSMultiPeerAllowlist(t *testing.T) {
	rk, _ := securityinternal.GenerateKeyPair()
	ikA, _ := securityinternal.GenerateKeyPair()
	ikB, _ := securityinternal.GenerateKeyPair()
	shape := recordshape.DefaultConfig(true)
	serverCfg := securityinternal.HandshakeConfig{Static: rk, RecordShaping: shape}
	var calls atomic.Int32
	seen := make(chan string, 2)
	h, err := HandlerWithNoise(func(ctx context.Context, in io.Reader, out io.Writer, p PeerInfo) error {
		calls.Add(1)
		seen <- p.Identity
		_, err := io.Copy(out, in)
		return err
	}, NoiseOptions{
		Handshake: serverCfg,
		AllowedPeers: map[string][]byte{
			"urn:baft:node:a": ikA.Public,
			"urn:baft:node:b": ikB.Public,
		},
		HandshakeTimeout: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	s, clientTLS := newServer(t, h)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, peer := range []securityinternal.KeyPair{ikA, ikB} {
		d := &Dialer{Endpoint: s.URL, TLSConfig: clientTLS}
		cfg := securityinternal.HandshakeConfig{Static: peer, PeerStatic: rk.Public, RecordShaping: shape}
		conn, sc, err := d.OpenNoise(ctx, cfg)
		if err != nil {
			t.Fatalf("OpenNoise: %v", err)
		}
		if _, err := sc.Write([]byte("hi")); err != nil {
			t.Fatal(err)
		}
		b := make([]byte, 2)
		if _, err := io.ReadFull(sc, b); err != nil || string(b) != "hi" {
			t.Fatalf("echo: %v %q", err, b)
		}
		conn.Close()
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}

	// A key not on the allowlist is refused.
	other, _ := securityinternal.GenerateKeyPair()
	d := &Dialer{Endpoint: s.URL, TLSConfig: clientTLS}
	cfg := securityinternal.HandshakeConfig{Static: other, PeerStatic: rk.Public, RecordShaping: shape}
	if _, _, err := d.OpenNoise(ctx, cfg); err == nil {
		t.Fatal("off-allowlist peer accepted")
	}
}

func TestWSHandshakeTimeoutBounded(t *testing.T) {
	rk, _ := securityinternal.GenerateKeyPair()
	ik, _ := securityinternal.GenerateKeyPair()
	h, err := HandlerWithNoise(nil, NoiseOptions{
		Handshake:        securityinternal.HandshakeConfig{Static: rk, PeerStatic: ik.Public},
		PeerIdentity:     "test",
		HandshakeTimeout: 50 * time.Millisecond,
		MaxPending:       1,
	})
	if err != nil {
		t.Fatal(err)
	}
	s, clientTLS := newServer(t, h)

	// Upgrade but never send a Noise message: the server must close within the
	// handshake timeout, not hang.
	d := &Dialer{Endpoint: s.URL, TLSConfig: clientTLS}
	conn, err := d.Dial(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	start := time.Now()
	buf := make([]byte, 1)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err = conn.Read(buf)
	if err == nil {
		t.Fatal("expected server to close idle handshake")
	}
	if time.Since(start) > time.Second {
		t.Fatalf("handshake not bounded: %v", time.Since(start))
	}
}

func TestWSRejectsBadScheme(t *testing.T) {
	d := &Dialer{Endpoint: "http://example.com/x", TLSConfig: &tls.Config{}}
	if _, err := d.Dial(context.Background()); err == nil {
		t.Fatal("expected scheme rejection")
	}
}

func maskedTestFrame(fin bool, opcode byte, payload []byte) []byte {
	if len(payload) > 125 {
		panic("maskedTestFrame only supports short test payloads")
	}
	first := opcode
	if fin {
		first |= 0x80
	}
	key := [4]byte{1, 2, 3, 4}
	out := []byte{first, 0x80 | byte(len(payload)), key[0], key[1], key[2], key[3]}
	for i, b := range payload {
		out = append(out, b^key[i&3])
	}
	return out
}

func TestWSFragmentedMessageStreamsWithoutAggregateAssembly(t *testing.T) {
	// Only the first fragment is present. A byte-stream Conn must return those
	// bytes immediately instead of waiting for (and accumulating) every
	// continuation frame in the logical WebSocket message.
	raw := maskedTestFrame(false, opBinary, []byte("first"))
	c := newConn(&fakeConn{r: bytes.NewReader(raw)}, nil, false)
	buf := make([]byte, 16)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatalf("first fragment read: %v", err)
	}
	if got := string(buf[:n]); got != "first" {
		t.Fatalf("first fragment = %q", got)
	}
	if !c.fragmented {
		t.Fatal("fragmentation state was not retained for the continuation")
	}
	if len(c.readBuf) != 0 {
		t.Fatalf("unexpected aggregate/read buffer: %d bytes", len(c.readBuf))
	}
}

func TestWSContinuationStateIsValidated(t *testing.T) {
	t.Run("orphan continuation", func(t *testing.T) {
		raw := maskedTestFrame(true, opContinuation, []byte("x"))
		c := newConn(&fakeConn{r: bytes.NewReader(raw)}, nil, false)
		if _, err := c.Read(make([]byte, 8)); err == nil || !strings.Contains(err.Error(), "continuation without") {
			t.Fatalf("orphan continuation err=%v", err)
		}
	})

	t.Run("new binary mid fragment", func(t *testing.T) {
		raw := append(maskedTestFrame(false, opBinary, []byte("a")), maskedTestFrame(true, opBinary, []byte("b"))...)
		c := newConn(&fakeConn{r: bytes.NewReader(raw)}, nil, false)
		if n, err := c.Read(make([]byte, 8)); err != nil || n != 1 {
			t.Fatalf("first read n=%d err=%v", n, err)
		}
		if _, err := c.Read(make([]byte, 8)); err == nil || !strings.Contains(err.Error(), "new binary frame") {
			t.Fatalf("mid-fragment binary err=%v", err)
		}
	})

	t.Run("valid continuation completes state", func(t *testing.T) {
		raw := append(maskedTestFrame(false, opBinary, []byte("a")), maskedTestFrame(true, opContinuation, []byte("b"))...)
		c := newConn(&fakeConn{r: bytes.NewReader(raw)}, nil, false)
		buf := make([]byte, 1)
		if n, err := c.Read(buf); err != nil || n != 1 || buf[0] != 'a' {
			t.Fatalf("first read n=%d err=%v byte=%q", n, err, buf[0])
		}
		if n, err := c.Read(buf); err != nil || n != 1 || buf[0] != 'b' {
			t.Fatalf("continuation read n=%d err=%v byte=%q", n, err, buf[0])
		}
		if c.fragmented {
			t.Fatal("fragmentation state remained set after final continuation")
		}
	})
}
