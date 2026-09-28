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

The historical slow-receiver failure triggered the TWRL redesign. TWRL now separates Accepted, Delivered, and Credit watermarks and passes the slow-receiver gate, race detector, and 1 GiB COR-01.

The active DATA scheduler is now PADL (Pressure-Aged Deficit Leasing), with classic DRR retained as a comparison baseline. Consolidated CI run `36341912070` and COR-01 run `36341912044` both pass on the PADL path.

Shared multi-Shard allocator exhaustion/reuse and conservation snapshots pass in CI run `36341666646`. Privacy-bounded conservation metrics pass in CI run `36341810504`.

Stage C is still formally open only because the dedicated repeated soak workflow must pass.

## Not complete

Full resume/epoch/snapshot/replay/tombstone semantics, endpoint/relay production paths, formal benchmark campaign, final operations/package work, and real-path pilot testing remain future stages.

## R3.1 current review candidate

Probabilistic normal/Laplace padding, tunable jitter, pinned Noise runtime wiring and pre-authentication HTML fallback are implemented. See [R3.1 guide](docs/en/21-stealth-pro.md) and [Hoosha report](reports/HOOSHA-R3.1.md). Full local tests/race/vet and ECRL differential pass. Stage D remains paused. Fresh-VM provisioning, jitter performance acceptance and real-path detectability validation remain open; v0.2-Pro is not released.


## Release branch Steps 5.1–5.7 status (2026-09-28)

On `release-v1-goldapp`, Steps 5.1 through 5.6 have passing CI regression gates for signed/idempotent telemetry, route monitoring, finance reporting, BCC hardening, backup/audit anchoring, and persistent telemetry reliability.

Step 5.7 integrates ECRL with live Runtime sessions for **same-process carrier replacement only**. Tested scope includes active-flow continuity, same-process epoch fencing, bounded replay, peer BootID fail-closed behavior, competing candidates, FIN/FIN_ACK recovery, multi-flow recovery, six-route identity isolation, and telemetry/finance continuity. Recovery commit safety uses pre-commit replay/materialization validation, a canonical plan digest, an explicit two-sided prepared/commit barrier, committed/uncommitted results, idempotent commit identity, and separate post-commit failure accounting.

This does **not** make BAFT production-ready. Process-restart resume, machine-reboot resume, and durable ECRL session snapshots are not implemented or claimed. Subscription Engine work is not part of Step 5.7.
