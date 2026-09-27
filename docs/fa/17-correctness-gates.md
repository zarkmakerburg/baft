<div dir="rtl" align="right" lang="fa">

# 17 — Single-flight correctness gates

COR-01 is intentionally expensive. The workflow therefore uses a per-branch concurrency group with `cancel-in-progress: true`: a newer commit supersedes an older 1 GiB correctness run instead of allowing stale runs to accumulate.

The COR-01 trigger includes the protocol, Carrier, Session, resource allocator, and scheduler paths. This matters because PADL can change byte scheduling without modifying the wire codec.

A separate high-volume deterministic PADL test drains 64 flows × 256 queued items under changing replay pressure. It is not a throughput benchmark; it is a liveness/state-conservation gate that catches scheduler loops, queue leaks, or starvation failures quickly in ordinary CI.

</div>
