# ADR-0004: Stage-B vertical flow semantics

Status: provisional
Date: 2026-09-27

The Stage-B vertical slice implements the BAFT/1 new-session ordering `HELLO → HELLO_ACK → READY` before application frames.

OPEN contains only `route_id` and a random 128-bit `open_nonce`. The listener resolves the route from a fixed in-memory allowlist and never accepts a target address from the peer.

The current flow path implements DATA/ACK/WINDOW and FIN/FIN_ACK sufficiently for a bounded single-carrier vertical test. Receive acknowledgement is emitted after data is accepted into bounded frame memory and validated, before the target socket write completes. Target-written progress is kept conceptually distinct. Duplicate DATA that is already below `rx_next` is acknowledged without being returned for another target write.

The Stage-B window is per-flow and sliding. It is not the final Stage-C global memory reservation allocator, and this distinction is recorded in `KNOWN-LIMITATIONS.md`.
