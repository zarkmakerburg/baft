# ADR-0003: Baseline H2 + mTLS carrier

Status: accepted
Date: 2026-09-27

## Decision

Use Go standard-library TLS and `net/http` HTTP/2 for the baseline carrier spike.

- TLS minimum is 1.3.
- mTLS is mandatory; no plaintext or HTTP/1 fallback exists in the BAFT carrier client.
- Server certificate hostname validation remains enabled.
- Client identity is one URI SAN; the verified URI must also be present in the explicit peer allowlist.
- Redirects and environment HTTP proxies are disabled for the carrier client.
- `/baft/v1/carrier` is a fixed path and is not treated as a secret.
- Each Shard owns its own `http.Transport`, limiting that carrier to one TCP/TLS connection. Four Shards therefore use four independent TCP connections in the Stage-A smoke test.
- Server response headers are flushed before the long-lived request body completes, and each server-side carrier write is flushed.
- Cancellation is part of the carrier contract.

## Rationale

This keeps the baseline dependency-minimal and makes the security boundary explicit. H3 remains experimental and cannot be enabled until the H2 gate is complete.
