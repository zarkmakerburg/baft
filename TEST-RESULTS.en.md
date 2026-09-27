# Test Results

> Persian: [TEST-RESULTS.md](TEST-RESULTS.md)  
> Date: 2026-09-27

CI uses GitHub Actions on Linux amd64 with Go 1.27.1 and locked module metadata.

The standard gate runs module tidy/diff verification, unit/integration tests, race detector, vet, and a protocol fuzz smoke.

## COR-01

Initial Stage-B run `36316645627` passed 1 GiB each direction with SHA-256 `1efd9d3aab21f9e312a2a0b5a6886b2a640c810ecb1fbe33f64614b26cfb27e3`.

After Stage-C allocator/DRR wiring, run `36338439622` on `b1ddb445...` again passed:
- Go 1.27.1;
- 1,073,741,824 bytes each direction;
- same SHA-256;
- approximately 10.56 seconds on runner/local networking.

This is a correctness regression gate, not a public-network performance result.

## Multi-Flow

Commit `0d70f1f...` added concurrent multi-Flow integration coverage; CI run `36317438771` passed.

## Open slow-receiver failure

CI run `36338439633` failed `TestSlowReceiverCreatesBackpressureWithoutGrowingBAFTMemory`. Source progress moved from 65536 to 98304 bytes after drain but did not satisfy the liveness condition within the test window. Stage C therefore remains incomplete.

Long soak, multi-Shard stress, recovery-state tests, formal benchmarks, operations tests, and real-path pilot work are still outstanding.
