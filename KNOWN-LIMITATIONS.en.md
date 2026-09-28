# Known Limitations

> Persian: [KNOWN-LIMITATIONS.md](KNOWN-LIMITATIONS.md)

- BAFT remains research software and is not declared production-ready.
- Stage C is incomplete; the slow-receiver liveness gate still has a failing result.
- Passing COR-01 is a correctness result on runner/local networking, not a public-network benchmark.
- allocator, DRR, and bounded control scheduling are in the data path, but long soak and multi-Shard stress are incomplete.
- same-process ECRL carrier replacement, same-process epoch fencing, and bounded replay are implemented and covered by Step 5.7 runtime tests.
- durable ECRL session snapshots, process-restart resume, and machine-reboot resume are not implemented or claimed.
- endpoint-pool/relay production paths are incomplete.
- H3 and Worker paths are not enabled in the baseline core.
- formal 60s × 5 benchmarking and profiling are incomplete.
- final systemd/installer/config rollback/certificate rotation/support-bundle operations are incomplete.
- no real Iran↔EX pilot has been run.
- no universal throughput, undetectability, or guaranteed-connectivity claim has been established.


## Step 5.7 recovery scope
ECRL runtime recovery is limited to **same-process carrier replacement** for an already-live session. Same-process epoch fencing and bounded replay are implemented and tested. Same-process epoch fencing and bounded replay from validated ECRL plans are implemented and tested. Recovery commit uses an explicit prepared/commit transaction with a canonical plan digest and idempotent commit identity. It does not persist ECRL session snapshots across process restart or machine reboot, and it does not claim process-restart resume. A peer BootID change during recovery fails closed. Subscription Engine work is out of scope. Record Shaping, Morphing, and Stealth behavior are unchanged by Step 5.7.
