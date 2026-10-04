# uTLS browser fingerprint under the WebSocket carrier dial

Wires the uTLS dial (`internal/carrier/utlsdial`, #94) under the WebSocket
carrier (#93/#96) so the IR→carrier (IR→Cloudflare) TLS ClientHello is
browser-fidelity, closing the passive-fingerprint gap from
`reports/CARRIER-CAMOUFLAGE.md`.

## What changed

- `internal/carrier/utlsdial`: a new `Handshake(ctx, conn, Config)` performs the
  uTLS handshake over an **existing** connection (so a transport that already
  holds the raw conn can reshape its TLS fingerprint); `Dial` delegates to it.
  The default ClientHello is `HelloChrome_120` — a classic (X25519, non-PQ)
  Chrome profile that negotiates cleanly with a TLS 1.3 server; the newest
  `Auto`/PQ profiles send post-quantum key shares a TLS 1.3-only Go server
  rejects.
- `internal/carrier/ws`: `Dialer.TLSHandshake` hook lets the node route the ws
  TLS handshake through uTLS instead of `crypto/tls`.
- `internal/node`: when `transport.utls` is set (ws only), the dial uses the
  uTLS hook (server still verified against the configured roots).
- `config`: `transport.utls` (bool); valid only with `transport.primary: ws`.
- `baft-pair`: `--utls` flag; and `pki --key-type ecdsa` to issue an
  ECDSA P-256 CA/leaf.

## The Ed25519 constraint (important)

A browser-fidelity ClientHello **cannot** use an **Ed25519** server certificate:
Chrome does not advertise the `ed25519` signature algorithm, so a TLS 1.3 server
holding an Ed25519 cert answers `handshake_failure` ("peer doesn't support any
of the certificate's signature algorithms"). This is inherent to mimicking a
browser — real Chrome cannot connect to such a server either.

BAFT's `baft-pair pki` issues **Ed25519** by default, which is fine for the `h2`
and non-uTLS `ws` paths. For uTLS the EX cert must be **ECDSA P-256 or RSA**:

- Behind Cloudflare the origin cert is a Cloudflare Origin Certificate
  (ECDSA/RSA) — automatic.
- With BAFT-issued certs, use `baft-pair pki --key-type ecdsa`.

`TestUTLSNegotiatesTLS13ECDSA` and `TestUTLSFailsTLS13Ed25519` lock this in.

## Verified

- `tests/e2e/ws_carrier.sh` with `BAFT_WS_UTLS=1` (added to the `e2e-binaries`
  CI job): pairs with `--transport ws --utls` and `pki --key-type ecdsa`, and
  pushes byte-exact traffic over the carrier with a browser ClientHello. The
  plain `ws` (Ed25519) path still passes.
- `tests/camouflage` JA3 test: the measured ClientHello is browser-like
  (GREASE + full Chrome cipher/extension set), not the Go three-suite set.
- `go test -race` on node/config/carrier green.

## Risk

| Risk | Severity | Mitigation |
|---|---|---|
| uTLS silently skips verification | High | `InsecureSkipVerify:false`; negative tests (bad root, bad name, pin, Ed25519) |
| Chrome profile drift breaks negotiation | Medium | pinned `HelloChrome_120`; e2e + JA3 test gate it |
| Operator enables utls with an Ed25519 cert | Medium | documented; handshake fails loudly; `--key-type ecdsa` provided |
| h2 path affected | None | uTLS applies only to the ws dial when `transport.utls` is set |

## Deferred

uTLS under the **h2** carrier (needs HTTP/2 over a uTLS conn) and multi-carrier
failover remain their own missions.
