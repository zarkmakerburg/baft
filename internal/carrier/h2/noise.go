package h2

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"time"

	"github.com/zarkmakerburg/baft/internal/recordshape"
	"github.com/zarkmakerburg/baft/internal/securityinternal"
)

// NoiseOptions is an explicit, pinned-peer mode. OneTimePairingPSK is not
// accepted: enrollment/persistence must finish before enabling this listener.
// Outer TLS MUST authenticate the server; the pinned Noise key authenticates
// the client. The same ordinary site handles all pre-authentication failures.
type NoiseOptions struct {
	Handshake        securityinternal.HandshakeConfig
	PeerIdentity     string
	AllowedPeers     map[string][]byte
	Cover            http.Handler
	HandshakeTimeout time.Duration
	MaxPending       int
	Revocations      RevocationWatcher
	OnHandshakeError   func()
	// OnHandshakeFailure receives a bounded phase label only; no peer address,
	// key material, payload, or endpoint is included.
	OnHandshakeFailure func(phase string)
}

func DefaultCover() http.Handler {
	const page = "<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\"><title>Welcome</title></head><body><main><h1>Welcome</h1><p>This website is online.</p></main></body></html>\n"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = io.WriteString(w, page)
		}
	})
}

func HandlerWithNoise(stream StreamHandler, o NoiseOptions) (http.Handler, error) {
	if !o.Handshake.Static.Valid() || len(o.Handshake.OneTimePairingPSK) != 0 {
		return nil, errors.New("h2: Noise listener requires a static key and no pairing PSK")
	}
	if len(o.AllowedPeers) == 0 {
		if len(o.Handshake.PeerStatic) != 32 || o.PeerIdentity == "" {
			return nil, errors.New("h2: Noise listener requires a pinned peer or a multi-peer allowlist")
		}
	} else {
		if len(o.Handshake.PeerStatic) != 0 || o.PeerIdentity != "" {
			return nil, errors.New("h2: multi-peer Noise listener must not configure legacy pinned peer fields")
		}
	}
	if _, err := recordshape.New(o.Handshake.RecordShaping); err != nil {
		return nil, err
	}
	if o.HandshakeTimeout == 0 {
		o.HandshakeTimeout = 5 * time.Second
	}
	if o.MaxPending == 0 {
		o.MaxPending = 64
	}
	if o.HandshakeTimeout < time.Millisecond || o.HandshakeTimeout > 30*time.Second || o.MaxPending < 1 || o.MaxPending > 4096 {
		return nil, errors.New("h2: invalid handshake limits")
	}
	if o.Cover == nil {
		o.Cover = DefaultCover()
	}

	identityByKey := map[string]string{}
	if len(o.AllowedPeers) != 0 {
		o.Handshake.AllowedPeerStatics = make([][]byte, 0, len(o.AllowedPeers))
		for identity, pub := range o.AllowedPeers {
			if identity == "" || len(pub) != 32 {
				return nil, errors.New("h2: invalid multi-peer Noise allowlist entry")
			}
			k := string(pub)
			if _, exists := identityByKey[k]; exists {
				return nil, errors.New("h2: duplicate Noise public key in allowlist")
			}
			identityByKey[k] = identity
			o.Handshake.AllowedPeerStatics = append(o.Handshake.AllowedPeerStatics, append([]byte(nil), pub...))
		}
	}

	slots := make(chan struct{}, o.MaxPending)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || r.ProtoMajor != 2 || r.Method != http.MethodPost || r.URL.Path != CarrierPath || r.Header.Get("Content-Type") != "application/octet-stream" {
			o.Cover.ServeHTTP(w, r)
			return
		}
		// Preserve the legacy single-peer behavior: the identity is known before
		// the handshake, so a revoked peer must never receive a Noise response.
		if len(identityByKey) == 0 && o.Revocations != nil && o.Revocations.IsRevoked(o.PeerIdentity, "", "") {
			o.Cover.ServeHTTP(w, r)
			return
		}
		select {
		case slots <- struct{}{}:
		default:
			o.Cover.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		controller := http.NewResponseController(w)
		if err := controller.SetReadDeadline(time.Now().Add(o.HandshakeTimeout)); err != nil {
			<-slots
			o.Cover.ServeHTTP(w, r)
			return
		}
		if err := controller.SetWriteDeadline(time.Now().Add(o.HandshakeTimeout + time.Second)); err != nil {
			<-slots
			o.Cover.ServeHTTP(w, r)
			return
		}
		cfg := o.Handshake
		cfg.Context = ctx
		out := &noiseResponseWriter{w: w}
		conn, peerStatic, err := securityinternal.Responder(r.Body, out, cfg)
		_ = controller.SetReadDeadline(time.Time{})
		_ = controller.SetWriteDeadline(time.Time{})
		<-slots
		if err != nil {
			phase := "noise_ik_msg1_read"
			if out.started {
				phase = "noise_ik_msg2_write"
			}
			if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "i/o timeout") {
				phase = "noise_timeout"
			}
			if o.OnHandshakeFailure != nil {
				o.OnHandshakeFailure(phase)
			} else if o.OnHandshakeError != nil {
				o.OnHandshakeError()
			}
			if !out.started {
				o.Cover.ServeHTTP(w, r)
			}
			return
		}

		peerIdentity := o.PeerIdentity
		if len(identityByKey) != 0 {
			peerIdentity = identityByKey[string(peerStatic)]
			if peerIdentity == "" {
				return
			}
		}
		if o.Revocations != nil && o.Revocations.IsRevoked(peerIdentity, "", "") {
			return
		}
		if o.Revocations != nil {
			revoked, unregister := o.Revocations.Watch(peerIdentity, "", "")
			defer unregister()
			go func() {
				select {
				case <-revoked:
					cancel()
					_ = r.Body.Close()
				case <-ctx.Done():
				}
			}()
		}
		if ctx.Err() != nil {
			return
		}
		if stream != nil {
			_ = stream(ctx, conn, conn, PeerInfo{Identity: peerIdentity, RemoteAddr: r.RemoteAddr})
		}
	}), nil
}

type noiseResponseWriter struct {
	w       http.ResponseWriter
	started bool
}

func (w *noiseResponseWriter) Write(p []byte) (int, error) {
	if !w.started {
		w.started = true
		w.w.Header().Set("Content-Type", "application/octet-stream")
		w.w.WriteHeader(http.StatusOK)
	}
	n, err := w.w.Write(p)
	if err == nil {
		err = http.NewResponseController(w.w).Flush()
	}
	return n, err
}

type noisePhaseEvent struct {
	name string
	at   time.Duration
	info string
}

type noisePhaseTrace struct {
	mu     sync.Mutex
	start  time.Time
	phase  string
	events []noisePhaseEvent
}

func newNoisePhaseTrace() *noisePhaseTrace {
	return &noisePhaseTrace{start: time.Now(), phase: "start"}
}

func (t *noisePhaseTrace) note(name, info string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.phase = name
	if len(t.events) < 24 {
		t.events = append(t.events, noisePhaseEvent{name: name, at: time.Since(t.start), info: info})
	}
}

func (t *noisePhaseTrace) clientTrace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GetConn: func(string) {
			t.note("connection_acquire", "")
		},
		ConnectStart: func(_, _ string) {
			t.note("tcp_connect_start", "")
		},
		ConnectDone: func(_, _ string, err error) {
			if err != nil {
				t.note("tcp_connect_error", "")
				return
			}
			t.note("tcp_connect_complete", "")
		},
		GotConn: func(info httptrace.GotConnInfo) {
			detail := ""
			if info.Reused {
				detail = "reused"
			}
			t.note("connection_ready", detail)
		},
		TLSHandshakeStart: func() {
			t.note("tls_handshake_start", "")
		},
		TLSHandshakeDone: func(cs tls.ConnectionState, err error) {
			if err != nil {
				t.note("tls_handshake_error", "")
				return
			}
			t.note("tls_handshake_complete", "alpn="+cs.NegotiatedProtocol)
		},
		WroteHeaders: func() {
			t.note("h2_headers_sent", "")
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err != nil {
				t.note("h2_request_write_error", "")
				return
			}
			t.note("h2_request_written", "")
		},
		GotFirstResponseByte: func() {
			t.note("h2_first_response_byte", "")
		},
	}
}

func (t *noisePhaseTrace) wrap(err error, timedOut, sanitizeTransport bool) error {
	if err == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var b strings.Builder
	for i, e := range t.events {
		if i != 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%s@%dms", e.name, e.at.Milliseconds())
		if e.info != "" {
			fmt.Fprintf(&b, "(%s)", e.info)
		}
	}
	if timedOut && errors.Is(err, context.Canceled) {
		err = context.DeadlineExceeded
	}
	// net/http and net.OpError strings may contain the request URL and local/
	// remote socket addresses. Sanitize only transport-originated failures.
	// Protocol/Noise errors are already bounded and retaining them preserves the
	// existing error contract for callers and diagnostics.
	cause := err
	if sanitizeTransport {
		cause = errors.New("transport error")
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		cause = context.DeadlineExceeded
	case errors.Is(err, context.Canceled):
		cause = context.Canceled
	}
	return fmt.Errorf("h2: OpenNoise failed phase=%s elapsed=%dms trace=%s: %w",
		t.phase, time.Since(t.start).Milliseconds(), b.String(), cause)
}

type traceFirstWriter struct {
	w     io.Writer
	trace *noisePhaseTrace
	once  sync.Once
}

func (w *traceFirstWriter) Write(p []byte) (int, error) {
	first := false
	w.once.Do(func() {
		first = true
		w.trace.note("noise_ik_msg1_write", "")
	})
	n, err := w.w.Write(p)
	if first {
		if err != nil {
			w.trace.note("noise_ik_msg1_write_error", "")
		} else {
			w.trace.note("noise_ik_msg1_written", "")
		}
	}
	return n, err
}

// OpenNoise begins IK concurrently with the HTTP request: waiting for HTTP
// headers before sending IK would deadlock a probe-resistant responder.
// The returned request writer and response body must both be closed by caller.
func (c *Client) OpenNoise(ctx context.Context, cfg securityinternal.HandshakeConfig) (*http.Response, *io.PipeWriter, *securityinternal.Conn, error) {
	ctx, cancel := context.WithCancel(ctx)
	timedOut := make(chan struct{})
	timeout := time.AfterFunc(10*time.Second, func() {
		close(timedOut)
		cancel()
	})
	defer timeout.Stop()

	trace := newNoisePhaseTrace()
	ctx = httptrace.WithClientTrace(ctx, trace.clientTrace())
	success := false
	defer func() {
		if !success {
			cancel()
		}
	}()

	reqR, reqW := io.Pipe()
	tracedW := &traceFirstWriter{w: reqW, trace: trace}
	// No copy goroutine or queue: after response headers, the Noise reader
	// consumes the HTTP body directly. Before headers it waits on a channel.
	reader := &responseReader{ctx: ctx, ready: make(chan io.Reader, 1), trace: trace}
	cfg.Context = ctx
	type result struct {
		conn *securityinternal.Conn
		err  error
	}
	done := make(chan result, 1)
	go func() {
		conn, err := securityinternal.Initiator(reader, tracedW, cfg)
		if err != nil {
			_ = reqW.CloseWithError(err)
		}
		done <- result{conn, err}
	}()

	didTimeout := func() bool {
		select {
		case <-timedOut:
			return true
		default:
			return false
		}
	}
	wrap := func(err error, sanitizeTransport bool) error {
		return trace.wrap(err, didTimeout(), sanitizeTransport)
	}
	fail := func(err error) {
		cancel()
		_ = reqR.CloseWithError(err)
		_ = reqW.CloseWithError(err)
	}

	resp, err := c.Open(ctx, reqR)
	if err != nil {
		err = wrap(err, true)
		fail(err)
		<-done
		return nil, nil, nil, err
	}
	if resp.Header.Get("Content-Type") != "application/octet-stream" {
		err = wrap(errors.New("h2: endpoint did not accept Noise carrier"), false)
		_ = resp.Body.Close()
		fail(err)
		<-done
		return nil, nil, nil, err
	}
	reader.ready <- resp.Body

	var res result
	select {
	case res = <-done:
	case <-ctx.Done():
		err = wrap(ctx.Err(), false)
		_ = resp.Body.Close()
		fail(err)
		<-done
		return nil, nil, nil, err
	}
	if res.err != nil {
		err = wrap(res.err, false)
		_ = resp.Body.Close()
		fail(err)
		return nil, nil, nil, err
	}
	trace.note("noise_complete", "")

	// Stop the handshake timer before handing ownership of the context to body.
	if !timeout.Stop() {
		err = wrap(context.Canceled, false)
		_ = resp.Body.Close()
		fail(err)
		return nil, nil, nil, err
	}
	resp.Body = &noiseBody{ReadCloser: resp.Body, cancel: cancel}
	success = true
	return resp, reqW, res.conn, nil
}

type responseReader struct {
	ctx      context.Context
	ready    chan io.Reader
	r        io.Reader
	trace    *noisePhaseTrace
	started  bool
	received bool
}

func (r *responseReader) Read(p []byte) (int, error) {
	if !r.started {
		r.started = true
		if r.trace != nil {
			r.trace.note("noise_ik_msg2_read", "")
		}
	}
	if r.r == nil {
		select {
		case r.r = <-r.ready:
		case <-r.ctx.Done():
			return 0, r.ctx.Err()
		}
	}
	n, err := r.r.Read(p)
	if n > 0 && !r.received {
		r.received = true
		if r.trace != nil {
			r.trace.note("noise_ik_msg2_received", "")
		}
	}
	return n, err
}

type noiseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *noiseBody) Close() error { b.cancel(); return b.ReadCloser.Close() }
