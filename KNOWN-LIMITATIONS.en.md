# Known Limitations

> Persian: [KNOWN-LIMITATIONS.md](KNOWN-LIMITATIONS.md)

- BAFT remains research software and is not declared production-ready.
- Stage C is incomplete; the slow-receiver liveness gate still has a failing result.
- Passing COR-01 is a correctness result on runner/local networking, not a public-network benchmark.
- allocator, DRR, and bounded control scheduling are in the data path, but long soak and multi-Shard stress are incomplete.
- full resume, epoch fencing, snapshots, replacement-Carrier replay, and tombstones are not implemented.
- resume across process restart is not promised.
- endpoint-pool/relay production paths are incomplete.
- H3 and Worker paths are not enabled in the baseline core.
- formal 60s × 5 benchmarking and profiling are incomplete.
- final systemd/installer/config rollback/certificate rotation/support-bundle operations are incomplete.
- no real Iran↔EX pilot has been run.
- no universal throughput, undetectability, or guaranteed-connectivity claim has been established.
