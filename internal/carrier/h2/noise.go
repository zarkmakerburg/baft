package h2

import (
	"context"
	"errors"
	"io"
	"net/http"
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

// OpenNoise begins IK concurrently with the HTTP request: waiting for HTTP
// headers before sending IK would deadlock a probe-resistant responder.
// The returned request writer and response body must both be closed by caller.
func (c *Client) OpenNoise(ctx context.Context, cfg securityinternal.HandshakeConfig) (*http.Response, *io.PipeWriter, *securityinternal.Conn, error) {
	ctx, cancel := context.WithCancel(ctx)
	timeout := time.AfterFunc(10*time.Second, cancel)
	defer timeout.Stop()
	success := false
	defer func() {
		if !success {
			cancel()
		}
	}()
	reqR, reqW := io.Pipe()
	// No copy goroutine or queue: after response headers, the Noise reader
	// consumes the HTTP body directly. Before headers it waits on a channel.
	reader := &responseReader{ctx: ctx, ready: make(chan io.Reader, 1)}
	cfg.Context = ctx
	type result struct {
		conn *securityinternal.Conn
		err  error
	}
	done := make(chan result, 1)
	go func() {
		conn, err := securityinternal.Initiator(reader, reqW, cfg)
		if err != nil {
			_ = reqW.CloseWithError(err)
		}
		done <- result{conn, err}
	}()
	fail := func(err error) { cancel(); _ = reqR.CloseWithError(err); _ = reqW.CloseWithError(err) }
	resp, err := c.Open(ctx, reqR)
	if err != nil {
		fail(err)
		<-done
		return nil, nil, nil, err
	}
	if resp.Header.Get("Content-Type") != "application/octet-stream" {
		err = errors.New("h2: endpoint did not accept Noise carrier")
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
		err = ctx.Err()
		_ = resp.Body.Close()
		fail(err)
		<-done
		return nil, nil, nil, err
	}
	if res.err != nil {
		_ = resp.Body.Close()
		fail(res.err)
		return nil, nil, nil, res.err
	}
	// Stop the handshake timer before handing ownership of the context to body.
	if !timeout.Stop() {
		err = context.DeadlineExceeded
		_ = resp.Body.Close()
		fail(err)
		return nil, nil, nil, err
	}
	resp.Body = &noiseBody{ReadCloser: resp.Body, cancel: cancel}
	success = true
	return resp, reqW, res.conn, nil
}

type responseReader struct {
	ctx   context.Context
	ready chan io.Reader
	r     io.Reader
}

func (r *responseReader) Read(p []byte) (int, error) {
	if r.r == nil {
		select {
		case r.r = <-r.ready:
		case <-r.ctx.Done():
			return 0, r.ctx.Err()
		}
	}
	return r.r.Read(p)
}

type noiseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *noiseBody) Close() error { b.cancel(); return b.ReadCloser.Close() }
