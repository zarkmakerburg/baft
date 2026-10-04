# Carrier camouflage: measurement and design

Goal: the carrier must not be blockable the moment it is used. This is the
measurement phase for two carrier transports the owner approved building (with
failover between them):

- **A) a Reality-style carrier** (borrow a real site's TLS handshake; fall
  through to the real site on an unauthenticated probe);
- **B) a WebSocket carrier behind Cloudflare** (orange cloud), hiding the origin
  IP and blending into normal Cloudflare HTTPS.

Measurement only; no production code changed yet.

## 1. Measured gap: BAFT's carrier TLS fingerprint

`tests/camouflage` captures the real ClientHello that
`internal/identity.ClientTLS` produces and computes its JA3.

Measured (Go 1.27 `crypto/tls`, the current carrier):

```
JA3: 771,4865-4866-4867,0-11-65281-23-18-5-10-13-50-16-43-51,4588-4587-4589-29-23-24-25,0
ciphers: 4865-4866-4867   (only the 3 TLS 1.3 AEAD suites)
GREASE : absent
```

A real Chrome ClientHello, for contrast, offers ~15 cipher suites (TLS 1.2 +
1.3), a different extension order, and **always includes GREASE** values.

Concrete, cheap DPI distinguishers today:

1. **No GREASE.** Chrome and Firefox always insert GREASE cipher/extension/group
   values; Go never does. A censor can flag "TLS 1.3 ClientHello with no GREASE"
   almost for free.
2. **Only 3 cipher suites.** A browser advertises many (it still supports
   TLS 1.2). Three-suite ClientHellos are a narrow, tool-like population.
3. **Extension set and order** differ from any browser.
4. The server side also presents a **locally issued certificate**, which is
   itself identifying.

Conclusion: the carrier is distinguishable by passive fingerprinting alone,
before any active probing. This is what a Reality/uTLS carrier closes.

## 2. Reality vs Cloudflare: a hard mutual exclusivity

**Reality and Cloudflare's orange cloud cannot be combined.** Cloudflare's proxy
terminates TLS at its edge; Reality requires the TLS handshake to reach the
origin untouched (it mimics/borrows a real site's handshake and falls through to
it). If Cloudflare terminates TLS, there is no handshake left for Reality to
work with. So the deployment must pick one per carrier:

| | A) Reality carrier | B) Cloudflare WebSocket carrier |
|---|---|---|
| Fingerprint | borrows a real site's handshake (best) | blends into Cloudflare HTTPS |
| Active probe | falls through to the real site | Cloudflare answers as itself |
| Origin IP | exposed (rotate IPs to mitigate) | hidden behind Cloudflare |
| Dependency | none | Cloudflare (sometimes throttled in-region) |
| Transport needed | direct TLS to the origin | **WebSocket (or gRPC) over 443** |
| Cert | none of your own | a Cloudflare-accepted origin cert |

### What Cloudflare orange cloud requires (for carrier B)

- Cloudflare's proxy only forwards specific ports (443 among them) and only
  HTTP/WebSocket/gRPC. BAFT's current carrier is a long-lived HTTP/2 streaming
  body, which Cloudflare's free proxy does not reliably forward; **WebSocket is
  the transport Cloudflare proxies natively** for bidirectional streams. So
  carrier B needs a new WebSocket carrier transport in BAFT.
- DNS: an A record to the origin, orange cloud **on**.
- SSL/TLS mode **Full (Strict)** with a Cloudflare **Origin Certificate** on the
  EX listener.
- The inner Noise handshake rides inside the WebSocket payload, so Cloudflare
  terminating the outer TLS does not weaken BAFT's own authentication or
  encryption — the inner layer stays end to end.

## 3. Design: two carriers with failover (uses existing ECRL)

BAFT already carries one logical session over replaceable physical carriers and
recovers across carrier loss (ECRL, Stage D). The proposal is to make the
carrier **transport** pluggable and register more than one:

- carrier 1: Reality-style direct TLS;
- carrier 2: WebSocket behind Cloudflare.

If the active carrier is blocked, the session fails over to the other with the
recovery machinery that already exists and is soak-tested. This turns "blocked
the moment it is used" into "switches to the path that still works," and it is
the differentiator BAFT's architecture already supports.

## 4. Implementation plan (each its own mission, risk-assessed)

1. **uTLS client ClientHello** (closes the fingerprint gap for the direct
   carrier): the dialer builds its ClientHello with a browser profile
   (refraction-networking/utls). Medium risk (transport/crypto); behind a flag;
   full gates; re-measure JA3 here afterwards.
2. **Reality-style listener fall-through**: on an unauthenticated/failed inner
   handshake, the EX transparently proxies the connection to a configured real
   upstream so a prober sees that site. Higher risk; needs careful anti-replay.
3. **WebSocket carrier transport** (enables carrier B behind Cloudflare):
   a new transport next to the H2 one; the inner Noise session is unchanged.
   Lower risk; mostly additive.
4. **Multi-carrier registration + failover** wiring over ECRL.
5. Re-run `tests/camouflage` and the adversary probe suite after each, and
   extend R-003 DPI fingerprinting.

Recommended order: 3 (unblocks the Cloudflare path the owner asked about and is
lowest risk) and 1 (biggest passive-fingerprint win) first, then 4, then 2.
