# COR-T1 / Issue #65 — COR-01 1 GiB liveness triage

Status: **ROOT-CAUSE BOUNDARY IDENTIFIED / PRODUCTION FIX NOT APPLIED**

Target SHA: `dcc6d92efce03e149b110e7785541913916758af`

## Required three workflow_dispatch attempts

All three attempts were independent workflow dispatches. Each ref pointed to exactly the target SHA. No rerun was used.

| Attempt | Run ID | SHA | Result | Test duration | Terminal evidence |
|---|---:|---|---|---:|---|
| A1 | 37140456718 | dcc6d92 | FAIL | 955.43 s | `cor01_test.go:221 ... write: broken pipe` |
| A2 | 37140460253 | dcc6d92 | FAIL | 949.61 s | `cor01_test.go:221 ... write: broken pipe` |
| A3 | 37140463296 | dcc6d92 | PASS | 7.78 s | 1 GiB each direction, expected SHA-256 |

Observed result in the ordered sample: **2/3 stalls/failures**. The defect is intermittent but readily reproducible on the target SHA.

Supplementary main-ref dispatch evidence exists independently:
- `37140248216` PASS
- `37140291932` PASS
- `37140339366` FAIL by the 20-minute Go test timeout

These supplementary runs are not substituted for A1–A3.

## Instrumentation

Triage branch: `triage/cor-t1-issue65`

The canonical COR-01 gate remains unchanged. Diagnostic-only files are:

- `internal/session/cor01_debug.go` — guarded by `//go:build cor01debug`
- `tests/correctness/cor01_stall_debug_test.go` — guarded by `//go:build cor01debug`
- `.github/workflows/cor-t1-issue65-triage.yml`

Normal production builds do not compile the debug hook.

The watchdog records:
- application send/receive progress;
- `txNext`, `txAcked`, `peerMax`;
- `rxNext`, `rxWritten`, `rxMax`;
- receive-ring and replay sizes;
- control/data sender queue lengths;
- highest WINDOW emitted;
- wire-observed ACK/WINDOW frontiers in both directions;
- all goroutine stacks.

## Reproduced instrumented stall

Run: `37141384441`

The watchdog fired after 30 seconds with no application progress. The same test remained stalled until its diagnostic 4-minute timeout.

Application frontier:

```text
sent_bytes = 232259584
recv_bytes = 222147340
delta      = 10112244
```

### IR state

```text
peerMax   = 229782783
txNext    = 229782783
txAcked   = 229782783

rxNext    = 222147340
rxWritten = 222147340
rxMax     = 222212876
ring      = 0
replay    = 0

sender control queue = 0
sender data queue    = 0
last WINDOW sent     = 222212876
```

IR's local pump is blocked in `flow.reserveReadCapacity`. This is an exact credit stop:

```text
txNext == peerMax
```

### EX state

```text
peerMax   = 222212876
txNext    = 222147340
txAcked   = 222147340

rxNext    = 229782783
rxWritten = 229717247
rxMax     = 229782783
ring      = 65536
replay    = 0

sender control queue = 0
sender data queue    = 0
last WINDOW sent     = 229782783
```

EX has accepted exactly one full receive reservation beyond delivered bytes:

```text
rxNext - rxWritten = 65536
rxNext == rxMax
```

The EX target pump is blocked in:

```text
pumpTarget
  -> writeConnFull
     -> net.(*conn).Write
        -> pollDesc.waitWrite
```

while attempting to deliver the receive-ring segment to the target TCP socket.

The test target's echo writer is also blocked in a TCP write. The EX local pump remains in a read on the target socket. Endpoint-identity capture is being used to exclude a duplicate/wrong-socket hypothesis; this does not change the BAFT-level liveness conclusion below.

## ACK / WINDOW analysis

There is **no evidence of a lost BAFT ACK**:

- IR: `txAcked == txNext == 229782783`
- EX: `txAcked == txNext == 222147340`

There is **no evidence of a WINDOW stuck in the BAFT sender queue or H2 carrier**.

EX:
- `rxMax = 229782783`
- sender `windowHigh[1] = 229782783`
- wire-observed last EX→IR WINDOW = `229782783`
- control queue = 0

IR:
- `rxMax = 222212876`
- sender `windowHigh[1] = 222212876`
- wire-observed last IR→EX WINDOW = `222212876`
- control queue = 0

Therefore the WINDOW IR is waiting for was **not lost**. It was never legitimately generated, because EX cannot advance delivered bytes while its target socket write is blocked.

Both H2/session frame readers are idle waiting for the next frame and both outbound sender queues are empty at the captured stall. This does not support an H2/control-queue deadlock as the primary cause.

## Root cause

The immediate liveness root cause is **unbounded target-write backpressure coupled directly to delivered-byte receive credit**.

The sequence is:

1. EX accepts DATA until its reserved receive window is full.
2. EX `pumpTarget` blocks indefinitely in the target TCP `Write`.
3. `rxWritten` cannot advance.
4. TWRL correctly prevents `rxMax` / WINDOW from advancing without delivered/reserved capacity.
5. IR reaches `txNext == peerMax` and stops reading more application bytes.
6. Application progress becomes zero in both directions.
7. There is no bounded liveness escape on the blocked target write, so the Flow remains stuck until an outer timeout/socket failure eventually produces the terminal broken-pipe symptom.

The terminal `broken pipe` is therefore an **effect after the stall**, not the initial cause.

The diagnostic evidence does **not** justify weakening TWRL, fabricating credit, or treating ACK as delivery proof.

### Regression assessment

Between the previously fast SHA `2912539` and target `dcc6d92`, the relevant `data_sender.go` behavior change in `stopErrorLocked` affects only `recoverable` senders. COR-01 runs with recovery disabled, so that change is inactive for this test.

Together with earlier historical COR-01 failures, current evidence supports a **latent non-recovery liveness defect**, not a proven regression introduced by #64.

## Proposed production fix — HQ approval required

No production change has been made.

Proposed scope:

- `internal/session/session.go`
  - make target writes context/liveness-aware instead of allowing an unbounded `net.Conn.Write` wait;
  - enforce a no-progress bound at the Flow target-delivery boundary;
  - on sustained target-write no-progress, terminate/reset **that Flow only**, release its receive reservation and slot, and preserve the Session.
- focused unit/integration tests:
  - deterministic target that accepts but stops draining;
  - deterministic bidirectional backpressure;
  - assert bounded Flow termination and no Session-wide liveness loss;
  - assert receive credit never advances beyond real reserved/delivered capacity.
- retain COR-01 as the large full-duplex regression gate.

A timeout must be **progress-based**, not a fixed total-transfer deadline. Any implementation must avoid false termination of a slow but progressing target.

A separate follow-up experiment should determine why the local echo target TCP pair enters persistent zero-progress under this workload; endpoint/TCP-state evidence can narrow whether a fixture/socket interaction is involved. That deeper socket subcause does not change the proven BAFT invariant: a target write currently has no bounded escape.

## Required correctness gates for any future fix

At minimum, on one candidate SHA:

1. deterministic blocked-target liveness test PASS;
2. COR-01 1 GiB repeated PASS with every attempt counted;
3. regular CI + race/vet;
4. existing flow-control/TWRL conservation tests PASS;
5. Class-A / recovery soak remains green even though COR-01 itself is recovery-disabled.

## Remaining risks / pending evidence

- exact underlying reason for persistent zero-progress on the loopback target TCP pair is still being narrowed with endpoint identity / socket-state evidence;
- no production fix is authorized or applied;
- Step 5.7 freeze and RC/release remain blocked by Issue #65;
- COR-01 must still be run on the final P0-R1 follow-up PR SHA as ordered by HQ.
