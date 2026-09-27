# 10 — Repository layout and module responsibilities

Key directories:

- `cmd/baft`: CLI entry point;
- `configs`: schema and examples;
- `internal/config`: parsing and validation;
- `internal/identity`: TLS identity and revocation;
- `internal/carrier/h2`: baseline HTTP/2 Carrier;
- `internal/protocol`: BAFT/1 codec;
- `internal/session`: Session/Flow state and outbound sending;
- `internal/scheduler`: DRR;
- `internal/resources`: allocator and bounded queues;
- `internal/routes`: fixed Route resolution/authorization;
- `tests/integration`: component-level integration;
- `tests/correctness`: large correctness gates;
- `docs/fa` and `docs/en`: mirrored user/developer guides.

Dependency boundaries matter: protocol does not dial targets, routes does not parse arbitrary wire payloads, scheduler does not interpret payload semantics, carrier does not decide Route authorization, and policy must never disable TLS verification.

Operational evidence is tracked in PLAN, STATUS, BLOCKERS, TEST-RESULTS, KNOWN-LIMITATIONS, dependency-lock, and ADR files.
