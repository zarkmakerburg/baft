# ADR-0005 — Stage-C resources

Status: stabilizing. Date: 2026-09-27.

Stage C uses one global allocator with independent receive and replay pools. The example baseline is 256 MiB total, split 128/128 MiB, with 16 MiB per-Flow receive/replay caps and no borrowing. Replay is reserved before additional source reads and released by ACK advancement. Receive credit is backed by reserved capacity. These primitives are now wired into Session, but the slow-receiver liveness gate is not yet fully green, so Stage C is not complete.
