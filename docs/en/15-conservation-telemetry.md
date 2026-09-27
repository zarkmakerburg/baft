# 15 — Conservation telemetry and shared multi-Shard budget

Stage C diagnostics should expose invariants, not merely independent counters. The internal conservation snapshot records Accepted (A), Delivered (D), Credit (C), receive reservation (R), ring occupancy, and replay outstanding per Flow.

It checks:

```text
D <= A <= C
C - D <= R
ring_bytes == A - D
```

A shared-budget test creates peers with different Shard IDs backed by one allocator. When the global receive pool is exhausted, another Shard must fail reservation; after a Flow releases capacity, that exact shared capacity must become reusable without leakage.

This is recorded as conservation-oriented engineering observability, not as a legal novelty claim.
