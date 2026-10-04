# Carrier camouflage: uTLS client fingerprint

This closes the passive TLS-fingerprint gap measured in
`reports/CARRIER-CAMOUFLAGE.md`. It adds `internal/carrier/utlsdial`, a dial that
presents a browser-fidelity ClientHello (via `refraction-networking/utls`) while
still authenticating — and optionally pinning — the server.

## The gap (measured, #92)

Go's `crypto/tls` ClientHello, which the carrier used, is cheap to flag:

```
JA3: 771,4865-4866-4867,0-11-65281-23-18-5-10-13-50-16-43-51,4588-4587-4589-29-23-24-25,0
ciphers: 4865-4866-4867   (only the 3 TLS 1.3 AEAD suites)
GREASE : absent
```

Two near-free distinguishers: **no GREASE** (browsers always emit it) and **only
three cipher suites** (browsers advertise ~15, TLS 1.2 + 1.3).

## After uTLS (measured, this change)

`TestUTLSClientHelloClosesFingerprintGap` captures the ClientHello the dial
sends and computes its JA3:

```
771,4865-4866-4867-49195-49199-49196-49200-52393-52392-49171-49172-156-157-47-53,5-65281-17613-23-18-27-16-51-65037-0-11-35-13-45-10-43,4588-29-23-24,0
ciphers: 15 suites (TLS 1.2 + 1.3)
GREASE : present (cipher, extension, group, and ECH-grease 65037)
extensions: browser set (key_share 51, supported_versions 43, compress_certificate 27, ALPS 17613, …)
```

This is a current-Chrome fingerprint (`HelloChrome_Auto`). The two cheap
distinguishers are gone; the test fails the build if either regresses.

## Design

`utlsdial.Dial(ctx, network, addr, Config)` returns a `net.Conn` carrying the
negotiated TLS session, so it can back **either** carrier transport (HTTP/2 or
WebSocket). The inner Noise IK handshake is unchanged and remains the end-to-end
authenticator; this only reshapes the **outer** TLS fingerprint.

`Config` carries `ServerName`, `RootCAs` (required — the server is always
verified), an optional `ClientCert` for mutual TLS on the direct carrier, ALPN
`NextProtos`, an optional certificate `Pin`, and the `Hello` profile (defaults to
Chrome).

`CaptureClientHello` builds the ClientHello bytes with no network I/O, for the
JA3 measurement.

### Verification is not weakened

uTLS can be misused in a way that silently skips verification. This dial does
**not**: it runs with `InsecureSkipVerify: false`, so the standard chain +
hostname verification runs during the handshake, and the optional pin is an
*additional* check. Three negative tests lock this in:

- `TestUTLSDialRejectsUntrustedServer` — empty root pool ⇒ handshake fails.
- `TestUTLSDialRejectsBadServerName` — wrong SNI/hostname ⇒ handshake fails.
- `TestUTLSDialPinEnforced` — a rejecting pin aborts a chain that otherwise
  verifies; an accepting pin lets it through.

## Risk assessment

| Risk | Severity | Mitigation |
|---|---|---|
| uTLS skips server verification (classic footgun) | High | `InsecureSkipVerify:false`; three negative tests assert rejection of bad root, bad hostname, and pin failure |
| New dependency (supply chain) | Medium | `refraction-networking/utls` is the de-facto library (xray/sing-box/v2ray); BSD-3; covered by `govulncheck`; pinned in go.sum |
| Parroted profile drifts from real Chrome over time | Low–Medium | `HelloChrome_Auto` tracks utls releases; the JA3 test guards the two key properties (GREASE + suite count) rather than an exact string |
| Added runtime surface | Low | Standalone dial; not yet wired into node — no behavior change to shipping carriers |

## Scope / deferred

This PR adds the dial and its measurement only. Wiring it under the carrier
dialers (HTTP/2 and WebSocket) behind a config flag, and the multi-carrier
failover, is the next mission (camouflage plan item 4). The Reality-style
listener fall-through is item 2.

## Tests

- `internal/carrier/utlsdial`: authenticated HTTP round-trip, untrusted-server
  rejection, bad-server-name rejection, pin enforced both ways, ClientHello
  capture.
- `tests/camouflage`: `TestUTLSClientHelloClosesFingerprintGap` (GREASE present,
  ≥10 cipher suites, no longer the three-suite set).
