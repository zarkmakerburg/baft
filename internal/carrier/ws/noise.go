package ws

import (
	"context"
	"errors"
	"net/http"
	"time"

	h2 "github.com/zarkmakerburg/baft/internal/carrier/h2"
	"github.com/zarkmakerburg/baft/internal/recordshape"
	"github.com/zarkmakerburg/baft/internal/securityinternal"
)

// The WebSocket carrier reuses the HTTP/2 carrier's higher-level contracts so a
// deployment can swap transports without changing the session wiring.
type (
	// PeerInfo identifies the authenticated remote peer.
	PeerInfo = h2.PeerInfo
	// StreamHandler receives the authenticated bidirectional stream.
	StreamHandler = h2.StreamHandler
	// RevocationWatcher gates and watches peer revocation.
	RevocationWatcher = h2.RevocationWatcher
	// NoiseOptions configures the Noise-authenticated listener.
	NoiseOptions = h2.NoiseOptions
)

// DefaultCover is the benign page served to probes and non-carrier requests.
var DefaultCover = h2.DefaultCover

// HandlerWithNoise returns an http.Handler for an HTTP/1.1 server that upgrades
// a WebSocket carrier on CarrierPath and runs the Noise IK responder over it.
// Any request that is not a carrier upgrade — including a probe — is served the
// cover page, exactly as the HTTP/2 carrier does. The outer TLS authenticates
// the server (behind Cloudflare, that is Cloudflare's edge certificate); the
// pinned Noise static key authenticates the client end to end.
func HandlerWithNoise(stream StreamHandler, o NoiseOptions) (http.Handler, error) {
	if !o.Handshake.Static.Valid() || len(o.Handshake.OneTimePairingPSK) != 0 {
		return nil, errors.New("ws: Noise listener requires a static key and no pairing PSK")
	}
	if len(o.AllowedPeers) == 0 {
		if len(o.Handshake.PeerStatic) != 32 || o.PeerIdentity == "" {
			return nil, errors.New("ws: Noise listener requires a pinned peer or a multi-peer allowlist")
		}
	} else {
		if len(o.Handshake.PeerStatic) != 0 || o.PeerIdentity != "" {
			return nil, errors.New("ws: multi-peer Noise listener must not configure legacy pinned peer fields")
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
		return nil, errors.New("ws: invalid handshake limits")
	}
	if o.Cover == nil {
		o.Cover = DefaultCover()
	}

	identityByKey := map[string]string{}
	if len(o.AllowedPeers) != 0 {
		o.Handshake.AllowedPeerStatics = make([][]byte, 0, len(o.AllowedPeers))
		for identity, pub := range o.AllowedPeers {
			if identity == "" || len(pub) != 32 {
				return nil, errors.New("ws: invalid multi-peer Noise allowlist entry")
			}
			k := string(pub)
			if _, exists := identityByKey[k]; exists {
				return nil, errors.New("ws: duplicate Noise public key in allowlist")
			}
			identityByKey[k] = identity
			o.Handshake.AllowedPeerStatics = append(o.Handshake.AllowedPeerStatics, append([]byte(nil), pub...))
		}
	}

	slots := make(chan struct{}, o.MaxPending)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Anything that is not a carrier upgrade is ordinary web traffic.
		if r.URL.Path != CarrierPath || !IsUpgrade(r) {
			o.Cover.ServeHTTP(w, r)
			return
		}
		// Preserve single-peer behavior: a revoked pinned peer is known before
		// the handshake, so never upgrade for it — serve the cover instead.
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
		releaseSlot := func() { <-slots }

		conn, err := Upgrade(w, r)
		if err != nil {
			releaseSlot()
			// Upgrade failed before switching protocols; serve the cover.
			o.Cover.ServeHTTP(w, r)
			return
		}
		// From here the connection is hijacked; the cover can no longer be
		// served. A failed Noise handshake simply drops the connection.
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		_ = conn.SetReadDeadline(time.Now().Add(o.HandshakeTimeout))
		_ = conn.SetWriteDeadline(time.Now().Add(o.HandshakeTimeout + time.Second))
		cfg := o.Handshake
		cfg.Context = ctx
		sc, peerStatic, err := securityinternal.Responder(conn, conn, cfg)
		_ = conn.SetReadDeadline(time.Time{})
		_ = conn.SetWriteDeadline(time.Time{})
		releaseSlot()
		if err != nil {
			if o.OnHandshakeError != nil {
				o.OnHandshakeError()
			}
			_ = conn.Close()
			return
		}

		peerIdentity := o.PeerIdentity
		if len(identityByKey) != 0 {
			peerIdentity = identityByKey[string(peerStatic)]
			if peerIdentity == "" {
				_ = conn.Close()
				return
			}
		}
		if o.Revocations != nil && o.Revocations.IsRevoked(peerIdentity, "", "") {
			_ = conn.Close()
			return
		}
		if o.Revocations != nil {
			revoked, unregister := o.Revocations.Watch(peerIdentity, "", "")
			defer unregister()
			go func() {
				select {
				case <-revoked:
					cancel()
					_ = conn.Close()
				case <-ctx.Done():
				}
			}()
		}
		if ctx.Err() != nil {
			_ = conn.Close()
			return
		}
		if stream != nil {
			_ = stream(ctx, sc, sc, PeerInfo{Identity: peerIdentity, RemoteAddr: r.RemoteAddr})
		}
		_ = conn.Close()
	}), nil
}

// OpenNoise dials the WebSocket carrier and performs the Noise IK initiator
// handshake over it. It returns the underlying ws Conn (close it to tear down
// the carrier) and the authenticated securityinternal.Conn (the session's
// bidirectional byte stream). Unlike the HTTP/2 carrier, WebSocket is already
// full-duplex, so no request/response pipe dance is needed.
func (d *Dialer) OpenNoise(ctx context.Context, cfg securityinternal.HandshakeConfig) (*Conn, *securityinternal.Conn, error) {
	conn, err := d.Dial(ctx)
	if err != nil {
		return nil, nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	cfg.Context = ctx
	sc, err := securityinternal.Initiator(conn, conn, cfg)
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, sc, nil
}
