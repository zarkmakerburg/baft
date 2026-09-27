# Project Status

> Persian: [STATUS.md](STATUS.md)  
> Report date: 2026-09-27

Repository: `zarkmakerburg/baft`, branch `main`. Code head before the documentation overhaul: `b1ddb44512fa0f48ff4629e1faf2f37523a9fe85`.

## Summary

- Stage A: complete for its defined scope.
- Stage B: complete for the defined secure vertical slice.
- Stage C: in progress and not complete.
- Stage D and later: not complete.

## Stage B evidence

Implemented/tested behavior includes new-session HELLO/HELLO_ACK/READY, fixed authorized Routes, Flow control frames, half-close, fixed RESET codes, mTLS identity/authorization, active peer revocation, strict YAML configuration, production CLI path, and end-to-end TCP→BAFT/H2+mTLS→TCP transfer.

Latest COR-01 run `36338439622` on `b1ddb445...` passed with Go 1.27.1, 1,073,741,824 bytes each direction, and SHA-256 `1efd9d3aab21f9e312a2a0b5a6886b2a640c810ecb1fbe33f64614b26cfb27e3` in about 10.56 seconds on local/runner networking.

## Stage C

The global receive/replay allocator, replay release on ACK, memory/credit backpressure, byte-based DRR, bounded control scheduling, and multi-Flow integration work are in the data path.

Stage C remains incomplete because the slow-receiver liveness gate is not yet stable. CI run `36338439633` failed `TestSlowReceiverCreatesBackpressureWithoutGrowingBAFTMemory`. In that run source progress moved from 65536 to 98304 bytes after drain, but the test's liveness acceptance condition was not met.

This is intentionally recorded as an unresolved Stage-C result rather than being hidden behind the passing COR-01 gate.

## Not complete

Full resume/epoch/snapshot/replay/tombstone semantics, endpoint/relay production paths, formal benchmark campaign, final operations/package work, and real-path pilot testing remain future stages.
