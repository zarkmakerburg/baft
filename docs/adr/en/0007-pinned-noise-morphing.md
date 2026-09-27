# ADR-0007 — Opt-in pinned Noise carrier and probabilistic records

Status: implemented review candidate for R3.1, authorized by the current user
mission; baseline ADR-0003 remains unchanged when `noise` is absent.

Public HTML fallback cannot work before TLS if a mandatory client certificate
rejects every ordinary visitor. An explicitly configured Noise mode therefore
uses server-authenticated TLS 1.3 plus mutually pinned Noise IK, rather than
client-certificate authentication. This is an explicit alternative, with no
unauthenticated downgrade, enrollment or automatic mode detection.

A single pinned public key maps to a single configured allowed identity.
Session/Route authorization and identity revocation use that mapping. HELLO
cannot establish or replace identity. Client certificate serial/fingerprint
revocation applies only to baseline mTLS; Noise deployments revoke identity.
TLS hostname/chain validation remains mandatory on the client. Invalid probes
never reach Session. HTTP/1.1 can serve the site but is not a Carrier fallback.

Shaping operates after Noise encryption, before HTTP/2-body IO. It cannot modify
TWRL watermarks, PADL accounting or ECRL plans. A prologue marker authenticates
shaping mode/version. Different directional budgets use the same bounded format.

This scope does not claim TLS/TCP packet impersonation, HTTPS distribution
matching, global novelty, classifier evasion or production performance.
The full mechanism, parameters, tests and limitations are in
[21 — Stealth Pro](../../en/21-stealth-pro.md).
