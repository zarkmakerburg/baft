# Carrier transport: WebSocket (behind Cloudflare)

This adds a second carrier transport, `internal/carrier/ws`, alongside the
existing HTTP/2 one (`internal/carrier/h2`). It is the transport the Cloudflare
(orange-cloud) deployment path from `CARRIER-CAMOUFLAGE.md` requires: Cloudflare
proxies WebSocket full-duplex streams natively, whereas it does not reliably
forward a long-lived HTTP/2 POST streaming body.

## What it is

A minimal, scoped RFC 6455 implementation that presents a WebSocket connection
as a reliable, ordered, bidirectional **byte stream** (`ws.Conn` is an
`io.ReadWriteCloser`). The inner Noise IK handshake and the BAFT framing ride
inside, unchanged — the upper layers never see WebSocket message boundaries.

- `conn.go` — RFC 6455 framing as a byte stream. Binary + continuation data
  frames are reassembled into a contiguous stream; ping is answered with pong;
  close returns EOF. Per the spec, a client masks every frame it sends and a
  server never does; each side rejects a violation. Inbound single-frame
  payloads are capped at 1 MiB (legitimate carrier frames hold Noise records
  ≤ 65535 bytes) to refuse memory-exhaustion attempts.
- `client.go` — `Dialer.Dial`: TCP → TLS → HTTP/1.1 `Upgrade: websocket`,
  validates `Sec-WebSocket-Accept`, with a bounded handshake deadline and
  caller-supplied blend-in headers (User-Agent, Origin).
- `server.go` — `Upgrade`: hijacks the HTTP/1.1 connection and completes the
  101 handshake. `IsUpgrade` distinguishes a real carrier dial from a probe.
- `noise.go` — `HandlerWithNoise` / `Dialer.OpenNoise`: the same Noise-IK +
  cover-site contract as the HTTP/2 carrier, reusing its types
  (`NoiseOptions`, `PeerInfo`, `StreamHandler`, `RevocationWatcher`,
  `DefaultCover`). Any request that is not a carrier upgrade — including a
  probe — is served the ordinary cover page.

The WebSocket path is **unchanged for the session layer**: a `*securityinternal.Conn`
comes back from `OpenNoise`, exactly as with the HTTP/2 carrier.

## Security model behind Cloudflare

Cloudflare's orange cloud terminates the outer TLS at its edge, so the carrier's
outer-TLS mutual pinning (mTLS) cannot survive to the origin. This transport
does **not** depend on it. Authentication is the inner **Noise IK** static-key
handshake, which is end to end: Cloudflare sees only WebSocket frames carrying
Noise ciphertext. Confidentiality, integrity and peer authentication are
therefore preserved even though Cloudflare is on the TLS path.

- The **client** is authenticated by its pinned Noise static key (single-peer)
  or by the multi-peer allowlist — identical to the HTTP/2 Noise listener.
- The **server** is authenticated to the client by the outer TLS it trusts
  (Cloudflare's edge cert for the domain) *and* by the Noise responder static
  key the client has pinned. A MITM that terminates TLS (including Cloudflare
  itself) cannot complete the Noise handshake without the responder's private
  key, so it cannot read or forge carrier traffic.
- **Revocation** is enforced before and after the handshake, as in the HTTP/2
  listener (a revoked pinned peer is never even upgraded).

## Probe resistance

- Non-upgrade requests, and upgrades to any path other than the carrier path,
  get the byte-identical cover page (verified in `TestWSNoiseEchoAndProbeResistance`).
- `MaxPending` bounds concurrent in-flight handshakes; over the limit, the
  request is served the cover page rather than queued.
- An upgrade to the carrier path that then fails the Noise handshake is dropped
  within `HandshakeTimeout` (`TestWSHandshakeTimeoutBounded`).

Residual gap vs. the HTTP/2 carrier: once a WebSocket is upgraded (101 sent) we
cannot fall back to an HTML page for a client that completed the WS handshake but
then fails Noise — it sees a connection close instead. This is addressed by the
Reality-style fall-through work (camouflage plan item 2); behind Cloudflare it is
further masked because Cloudflare answers edge probes as itself.

## Risk assessment

| Risk | Severity | Mitigation |
|---|---|---|
| Hand-rolled framing bug (masking, length, fragmentation) | High | `FuzzReadFrame` (added to the daily fuzz matrix), race tests, explicit length/control-frame/RSV validation, 1 MiB inbound cap |
| Loss of outer mTLS behind Cloudflare | Medium | Inner Noise IK is the real authenticator and is end-to-end; documented as the intended model, not a regression |
| Probe distinguishes a dropped-after-upgrade connection | Low–Medium | Cover for all non-upgrade traffic; Cloudflare edge masks probes; Reality fall-through (follow-up) closes the rest |
| New attack surface in the listener | Medium | Bounded pending handshakes, bounded handshake time, reuse of the audited Noise/cover/revocation logic |
| Supply chain | Low | **No new dependency** — RFC 6455 implemented in-house, keeping `go.mod` lean and the wire format under our control (also needed later for fingerprint shaping) |

## Scope / what is deferred

This PR adds the transport and its tests only. Wiring it into node/config as a
selectable carrier, and failing over between the HTTP/2, Reality and WebSocket
carriers over the existing ECRL recovery, is the next mission (camouflage plan
item 4). The uTLS client-fingerprint work (item 1) and the Reality fall-through
listener (item 2) are tracked separately.

## Tests

- `TestWSNoiseEchoAndProbeResistance` — cover/probe behavior, authenticated echo
  across payload sizes that cross the write-frame and 16-bit-length boundaries,
  unknown-key rejection, revocation.
- `TestWSMultiPeerAllowlist` — allowlisted peers map to identities; off-list key
  refused.
- `TestWSHandshakeTimeoutBounded` — idle upgrade is dropped within the timeout.
- `TestWSRejectsBadScheme`, `TestCarrierPathMatchesH2`.
- `FuzzReadFrame` — coverage-guided fuzzing of the frame parser.
