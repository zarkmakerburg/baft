# Project Status

> Persian: [STATUS.md](STATUS.md)  
> Report date: 2026-10-03

Repository: `zarkmakerburg/baft`, branch `main`. Current qualification baseline: `29125392566021a286af20ff8f3a4907c28dbee8`.

## Summary

- Stage A: complete for its defined scope.
- Stage B: complete for the defined secure vertical slice.
- Stage C: the current multi-Flow/slow-receiver soak gate is green; this does not mean a public benchmark or production readiness.
- Stage D: same-process ECRL recovery through Step 5.7 is implemented and tested, but Stage D is not complete or production-ready; process-restart/machine-reboot resume and durable ECRL session snapshots remain unimplemented.


## P0-I — Step 5.7 final qualification (2026-10-03)

Exact qualification baseline on `main`:

- SHA: `29125392566021a286af20ff8f3a4907c28dbee8`
- PR #60: merged; logical-session cancellation-cause instrumentation retained and the temporary diagnostic workflow removed.
- Full CI on this SHA: **PASS**, including unit/integration, race detector, vet, and protocol fuzz smoke.
- Stage-C soak on this SHA: **PASS**.
- Step 5.7 Recovery Soak: **PASS in 5 independent attempts on the same SHA**.
- Each Recovery Soak covers unresolved Class-A exact transactions, hard Class-A live-data recut, Class-B fresh epoch, ambiguous replay contract, Topology × ECRL authority isolation, HTTP handler lifecycle, P0-C lifetime/race, exact-rebind lifecycle, FD/resource lifetime, multi-flow replacement, distributed finalization/replay/FIN matrix, and concurrent Flow churn + race.

Engineering result: the recent rare Class-A evidence gap did not reproduce on this baseline and current qualification is green. This records a **Step 5.7 freeze candidate**; formal ACCEPT/FREEZE remains an HQ governance decision.

Scope remains limited to **same-process live-session carrier replacement**. This does not add claims for process-restart resume, machine-reboot resume, durable session snapshots, or production readiness.

## Stage B evidence

Implemented/tested behavior includes new-session HELLO/HELLO_ACK/READY, fixed authorized Routes, Flow control frames, half-close, fixed RESET codes, mTLS identity/authorization, active peer revocation, strict YAML configuration, production CLI path, and end-to-end TCP→BAFT/H2+mTLS→TCP transfer.

Latest COR-01 run `36338439622` on `b1ddb445...` passed with Go 1.27.1, 1,073,741,824 bytes each direction, and SHA-256 `1efd9d3aab21f9e312a2a0b5a6886b2a640c810ecb1fbe33f64614b26cfb27e3` in about 10.56 seconds on local/runner networking.

## Stage C

The global receive/replay allocator, replay release on ACK, memory/credit backpressure, byte-based DRR, bounded control scheduling, and multi-Flow integration work are in the data path.

The historical slow-receiver failure triggered the TWRL redesign. TWRL now separates Accepted, Delivered, and Credit watermarks and passes the slow-receiver gate, race detector, and 1 GiB COR-01.

The active DATA scheduler is now PADL (Pressure-Aged Deficit Leasing), with classic DRR retained as a comparison baseline. Consolidated CI run `36341912070` and COR-01 run `36341912044` both pass on the PADL path.

Shared multi-Shard allocator exhaustion/reuse and conservation snapshots pass in CI run `36341666646`. Privacy-bounded conservation metrics pass in CI run `36341810504`.

### Stage-C soak — current status

The dedicated `stagec-soak` workflow repeats real multi-Flow and slow-receiver transfers 25 times and runs 5 rounds under the race detector.

Evidence:
- historical failure: run `36342169299` — **FAIL** with `TestConcurrentMultiFlowTransfer: unexpected EOF`.
- historical pass after the fixes: run `36342627897` — **PASS**.
- code-head evidence before the documentation commit: run `36519987991` on `6fcf41631dc963af6f9c124f245a3ce7a47bc2fe` — **PASS**, including the repeated soak and race sample.

The current Stage C gate is therefore green, but this is not a public benchmark, a real pilot, or a production-readiness claim.

## Not complete

Same-process carrier replacement, same-process epoch fencing, and bounded replay are implemented and tested in Step 5.7. Still incomplete are process-restart resume, machine-reboot resume, durable ECRL session snapshots/tombstones across restart, endpoint/relay production paths, the formal benchmark campaign, final operations/package work, and real-path pilot testing.

## R3.1 current review candidate

Probabilistic normal/Laplace padding, tunable jitter, pinned Noise runtime wiring and pre-authentication HTML fallback were recorded for the historical R3.1 review candidate. See [R3.1 guide](docs/en/21-stealth-pro.md) and [Hoosha report](reports/HOOSHA-R3.1.md). The statement that Stage D was paused describes that historical point; current ECRL status is the Step 5.7 release-branch section below. Fresh-VM provisioning and the broader release/operations validation remain open; v0.2-Pro is not released.


## Release branch Steps 5.1–5.7 status (2026-09-28)

On `release-v1-goldapp`, Steps 5.1 through 5.6 have passing CI regression gates for signed/idempotent telemetry, route monitoring, finance reporting, BCC hardening, backup/audit anchoring, and persistent telemetry reliability.

Step 5.7 integrates ECRL with live Runtime sessions for **same-process carrier replacement only**. Tested scope includes active-flow continuity, same-process epoch fencing, bounded replay, peer BootID fail-closed behavior, competing candidates, FIN/FIN_ACK recovery, multi-flow recovery, six-route identity isolation, and telemetry/finance continuity. Recovery commit safety uses pre-commit replay/materialization validation, a canonical plan digest, an explicit two-sided PREPARED/readiness/COMMIT barrier, committed/uncommitted results, idempotent COMMIT/COMMIT_ACK handling, old-epoch fencing after authority publication, forward recovery to the next epoch after post-commit carrier failure, and separate post-commit failure accounting.

This does **not** make BAFT production-ready. Process-restart resume, machine-reboot resume, and durable ECRL session snapshots are not implemented or claimed. Subscription Engine work is not part of Step 5.7.
