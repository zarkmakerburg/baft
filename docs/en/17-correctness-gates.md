# 17 — Single-flight correctness gates

COR-01 is expensive, so its workflow uses a per-branch concurrency group with `cancel-in-progress: true`. A newer commit supersedes an older 1 GiB run instead of accumulating stale correctness jobs.

The trigger explicitly covers protocol, Carrier, Session, resources, and scheduler code. A deterministic high-volume PADL test also drains 64 flows × 256 items while replay pressure changes. That test is a liveness/state-conservation gate, not a throughput benchmark.
