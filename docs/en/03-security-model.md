# 03 — Security, identity, and trust boundaries

BAFT assumes two operator-controlled Nodes. Security does not depend on hiding the HTTP path.

## PKI

The design uses a private CA, separate Node private keys, EX server-auth certificates with the configured hostname SAN, and IR client-auth certificates with a URI SAN such as `urn:baft:node:ir-01`. Chain, hostname, validity, and EKU checks remain enabled.

## Trust is not authorization

Three distinct checks are maintained:

1. TLS certificate trust;
2. peer identity allowlist;
3. per-Route authorization.

A CA-issued certificate does not automatically authorize every Route.

## HELLO is not an identity source

HELLO `node_id` is state metadata. The authenticated URI SAN remains the trust source and mismatches are rejected.

## Active revocation

Runtime revocation supports peer identity, certificate serial, and SHA-256 fingerprint. Established H2 carriers register watchers so emergency revocation cancels the active Carrier as well as rejecting future connections.

## Route security

OPEN carries `route_id` and `open_nonce`; it never carries an arbitrary target address. The receiving Node resolves the Route from local configuration.

## Parser safety

The BAFT/1 parser uses a fixed 24-byte header, validates length/type before payload allocation, rejects unknown flags/reserved fields, detects offset overflow, and does not assume socket-read or HTTP/2 frame boundaries equal BAFT frame boundaries.

## No downgrade paths

The default path forbids `InsecureSkipVerify`, plaintext fallback, automatic HTTP/1 fallback, redirects, environment proxy use for the Carrier, arbitrary targets, and unbounded queues/retries.
