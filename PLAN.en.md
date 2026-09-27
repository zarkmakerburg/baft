# Development Plan

> Persian: [PLAN.md](PLAN.md)

## Stage A
Completed: repository contract, Go pin, BAFT/1 codec/vectors, strict configuration, test PKI, full-duplex H2+mTLS/cancellation, independent Shard transports, and pinned-toolchain CI.

## Stage B
Completed for the defined vertical slice: new-session handshake, authorized Routes, Flow framing, half-close, certificate negative tests, duplicate handling, active revocation, fixed RESET codes, 1 GiB COR-01, strict YAML/dependency lock, and real IR/EX CLI path.

## Stage C

Completed/integrated:
- [x] global data allocator
- [x] independent receive/replay pools
- [x] per-Flow reservation
- [x] replay release on ACK
- [x] credit/memory backpressure
- [x] byte-based DRR in DATA path
- [x] bounded control queue
- [x] control priority with finite anti-starvation burst
- [x] concurrent multi-Flow integration test

Remaining:
- [x] stabilize slow-receiver liveness gate with TWRL
- [ ] longer slow-receiver/soak tests
- [x] multi-Shard shared-budget exhaustion/reuse gate
- [x] privacy-bounded conservation metrics
- [ ] close Stage C after the independent Stage-C soak workflow passes without OOM/race/deadlock

## Stage D
Boot/session/epoch fencing, snapshots, replay, tombstones, duplicate-free Carrier replacement, state-machine fuzz, and recovery correctness gates.

## D2/E/F/G/H
Endpoint/relay work, reproducible performance benchmarking, operations/packaging, bounded research transports, and real-server pilot work remain future stages.
