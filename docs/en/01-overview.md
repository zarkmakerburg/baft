# 01 — Overview, goals, and scope

## Short definition

BAFT is a secure, authenticated, bounded TCP relay between two operator-controlled Nodes. The design goal is a testable data plane with strict transport security, explicit state, real backpressure, and staged acceptance gates.

## Baseline scenario

- IR is the `dialer`.
- EX is the `listener`.
- IR accepts an application connection on a local address such as `127.0.0.1:1443`.
- That TCP connection becomes a BAFT Flow.
- The Flow is carried by one Shard.
- EX resolves only a preconfigured Route such as `service-main`.
- EX connects to that Route's fixed target such as `127.0.0.1:2443`.
- Bytes move in both directions.

## Route IDs instead of arbitrary destinations

The wire protocol does not accept `OPEN host=... port=...`. A peer supplies only a Route ID. The receiving Node owns the actual target mapping. This prevents BAFT from becoming an arbitrary-destination proxy and keeps authorization explicit.

## Technical goals

- TLS 1.3 and mTLS;
- certificate-derived Node identity;
- bounded fuzzable binary parser;
- bidirectional Flow semantics and half-close;
- explicit ACK/WINDOW flow control;
- global memory budgets and backpressure;
- bounded multi-Flow / multi-Shard scheduling;
- precise future resume semantics without promising process-restart persistence;
- reproducible benchmarking and research evidence.

## Non-goals

BAFT is not intended to become a public VPN, arbitrary proxy, automatic Xray configurator, plaintext fallback system, or a source of universal throughput/undetectability claims. User payload is not an analytics/debugging data source.

## Current implementation versus final design

Stage B provides the secure vertical slice. Stage C's allocation, scheduling, bounded control, and slow-receiver soak gate is green. Stage D is partly implemented: same-process ECRL carrier replacement, epoch fencing, and bounded replay exist behind `recovery.enabled` (Step 5.7). Durable snapshots and resume across process restart or machine reboot are not current capabilities.
