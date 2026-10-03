# Test Results

> Persian: [TEST-RESULTS.md](TEST-RESULTS.md)  
> Date: 2026-10-03

CI uses GitHub Actions on Linux amd64 with Go 1.27.1 and locked module metadata.

The standard gate runs module tidy/diff verification, unit/integration tests, race detector, vet, and a protocol fuzz smoke.


## P0-I / Step 5.7 freeze-candidate evidence — 2026-10-03

Baseline: `29125392566021a286af20ff8f3a4907c28dbee8` on `main`.

- CI run `37125867545`: **PASS**, including the standard test/race/vet/fuzz coverage and install/run E2E jobs.
- Stage-C soak run `37125867530`: **PASS**.
- Step 5.7 Recovery Soak run `37125867525`: **PASS in 5 independent run attempts on the same SHA**.
- Every attempt covers Topology × ECRL authority isolation plus Class-A/Class-B/replay/finalization/lifetime/resource/flow-churn gates.
- PR #60 retains cancellation-cause instrumentation for future rare-failure attribution.

This evidence makes the current qualification green and records Step 5.7 as a **freeze candidate pending HQ acceptance**. Scope remains same-process recovery; restart/reboot resume remains out of scope.

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

## Slow receiver / TWRL

The historical slow-receiver failure is closed by the TWRL receive-path redesign. CI run `36340860552` passes the slow-receiver integration, race detector, vet, and fuzz smoke. COR-01 run `36340860568` also passes with the same 1 GiB hash.

## PADL / shared budgets / metrics

Consolidated PADL gate:
- CI `36341912070`: PASS
- COR-01 `36341912044`: PASS
- high-volume PADL liveness: PASS

Shared multi-Shard allocator/conservation gate:
- CI `36341666646`: PASS

Privacy-bounded conservation metrics:
- CI `36341810504`: PASS

The remaining Stage-C gate is the dedicated repeated soak workflow.

Long soak, multi-Shard stress, recovery-state tests, formal benchmarks, operations tests, and real-path pilot work are still outstanding.
