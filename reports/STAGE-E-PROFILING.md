# BAFT Stage E — Profiling Evidence

Status: **DIAGNOSTIC / informational only**

No production optimization is authorized or applied by this report. The purpose is to explain the Stage E B06 versus B08 loopback gap with measured evidence before any performance patch is proposed.

## Provenance

- Branch: `bench/stage-e-foundation`
- Profile commit: `6a40d6aa7185be2634e9e1fd6733a245eb6b669b`
- Workflow run: `37130713328` — **PASS**
- Artifact ID: `11275759887`
- Profiled scenario: B06, real BAFT Session + H2/mTLS loopback path
- Profiles: CPU, heap/allocation, block, mutex

The profiled B06 run produced about 390.21 Mbps per direction. That number must not be compared directly with the unprofiled prebaseline because profiling itself adds overhead.

## CPU evidence

The CPU profile collected 2.08 seconds of samples during a 730 ms profile window.

Primary flat CPU sample:

- `internal/runtime/syscall/linux.Syscall6`: **54.33% flat**

Important cumulative write-path observations:

- `net/http/internal/http2.writeWithByteTimeout`: **35.58% cumulative**
- `crypto/tls.(*Conn).Write`: **35.10% cumulative**
- `bufio.(*Writer).Flush`: **29.81% cumulative**
- socket write path: about **38.46% cumulative** through `net.(*conn).Write`

This is evidence that the measured loopback path spends substantial CPU/time crossing the H2/TLS/socket write stack rather than being dominated by BAFT mutex contention.

## Allocation evidence

Heap profile type: `alloc_space`, total observed allocation about 194.56 MiB.

Largest allocation sites:

| Site | Allocation | Share |
| --- | ---: | ---: |
| `internal/protocol.Decode` | 75.81 MiB | 38.97% |
| `internal/session.(*flow).commitSend` | 62.48 MiB | 32.12% |
| benchmark payload fixture | 32.01 MiB | 16.45% |
| `scheduler.(*PADL).Next` | 3 MiB | 1.54% |
| `outboundSender.sendControl` | 3 MiB | 1.54% |
| `outboundSender.sendDataWithProducer` | 2.5 MiB | 1.29% |

The benchmark fixture's 32 MiB allocation is expected test payload construction and is not a BAFT dataplane allocation.

### Code correspondence

`internal/protocol/frame.go` currently allocates a new payload slice for every non-empty decoded frame:

```go
f.Payload = make([]byte, plen)
io.ReadFull(r, f.Payload)
```

`internal/session/session.go` currently copies every committed outgoing payload into the replay ledger:

```go
payload := append([]byte(nil), data...)
f.replay = append(f.replay, replayChunk{..., data: payload})
```

These two code paths directly correspond to the two largest BAFT allocation sites in the profile.

## Flush/write granularity hypothesis

`internal/carrier/h2/h2.go` wraps the HTTP response writer with `flushingWriter`, whose `Write` calls `Flush()` after every write.

At the protocol layer, `protocol.Encode` writes the 24-byte header first and then writes payload separately.

Therefore one DATA frame can cause separate writer operations for header and payload, and on the server response path each operation can trigger an H2 flush. This code structure is consistent with the CPU profile showing substantial cumulative time in HTTP/2 writes, TLS writes, buffered flushes, and socket syscalls.

This is a **profiling hypothesis supported by code structure and profile correlation**, not yet a proof that coalescing writes will produce a specific speedup. A benchmark-only A/B experiment should falsify or confirm it before any production change.

## Blocking evidence

Block profile:

- `runtime.selectgo`: 92.11% of recorded blocking delay.
- `receiveRing.Peek`: about 40.69% cumulative.
- `flow.reserveReadCapacity`: about 33.24% cumulative.
- `pumpLocal`: about 40.62% cumulative.
- `pumpTarget`: about 43.25% cumulative.

Block profiles measure waiting time across goroutines, not CPU cost. These values show where flow-control/backpressure waits occur, but they do not by themselves prove a performance defect.

## Mutex evidence

Total recorded mutex delay was only about **20.81 ms** in the profile.

This run does not support the hypothesis that mutex contention is the primary cause of the B06/B08 throughput gap.

## Evidence-based next experiments

Before production optimization, Stage E should isolate three hypotheses:

1. **Write/flush granularity A/B**
   - same B06 payload profile;
   - baseline current write/flush behavior;
   - test-only frame-coalesced writer;
   - compare syscall count, CPU profile, and throughput.

2. **Decode allocation A/B**
   - benchmark current per-frame allocation;
   - test reusable/pooled destination buffer without changing wire semantics;
   - measure allocs/op and bytes/op;
   - correctness/fuzz must remain unchanged before any production proposal.

3. **Replay-copy cost A/B**
   - quantify `commitSend` bytes allocated per payload byte;
   - separate recovery-disabled and recovery-enabled requirements;
   - any ownership/reuse proposal must preserve ACK, replay, ECRL, and exact-transaction invariants.

Only after these measurements should HQ consider an optimization mission.
