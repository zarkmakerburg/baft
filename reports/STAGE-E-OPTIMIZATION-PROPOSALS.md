# Stage E D-008B item 4: three optimization proposals

Status: PROPOSALS ONLY. No production code in this change. Each proposal becomes its own mission after owner/HQ approval, on its own branch, with the gates listed.

Evidence base:
- CPU/alloc profile: `reports/STAGE-E-PROFILING.md` on `bench/stage-e-foundation` (run 37130713328).
- Coalescing A/B: `reports/STAGE-E-COALESCE-AB.md` (run 37131090548).
- Window autotune (#83) is merged. The per-Flow window no longer caps WAN paths, so CPU cost per byte is now the next limit on fast paths.

Recommended order: P3 first (largest measured gain, smallest code), then P1, then P2.

---

## P1 — Decode buffer pooling

**Cost today.** `protocol.Decode` allocates a fresh payload for every non-empty frame:

```go
f.Payload = make([]byte, plen)
io.ReadFull(r, f.Payload)
```

This is the largest BAFT allocation site: 75.8 MiB, 39% of observed allocation in the B06 profile. The payload is copied into the receive ring right away (`acceptData` → `rxRing.Write`), so in the DATA path the decoded slice lives only until that copy.

**Proposal.**
- Add `DecodeInto(r, buf *[]byte)`, or a `Decoder` that owns a reusable payload buffer of size `MaxPayloadSize`, for the carrier reader loop.
- The returned `Frame.Payload` is valid only until the next decode.
- Callers that keep a payload (control frames decoded into structs, recovery frames, tests) copy it explicitly.
- `Decode` keeps its current semantics for every other caller.

**Expected gain.** About 39% fewer bytes allocated on the receive path and less GC work. In the 4-vCPU B06 case the GC share was small, so the throughput gain will likely be modest (single-digit %). The main value is lower CPU and memory churn per byte at high rates and many Flows.

**Risk.** Aliasing: any code path that keeps `fr.Payload` after the next decode would see its bytes change. Known places to audit:
- handlers that pass `fr.Payload` to goroutines;
- recovery diagnostics;
- `HandleRecoveryControlFrame`;
- control decoding (`DecodeControl` copies into structs, which is safe).

**Test plan.**
- A test that fills the reuse buffer with a sentinel after each frame, run across the full integration suite, so any retained alias fails loudly.
- `go test -race` on session, integration and correctness.
- Allocation benchmark before/after (`testing.AllocsPerRun` on the reader loop).
- Gates: B06 loopback A/B (≥10 runs, CI), COR-01 ×10, step57 and stagec soaks, r3.1.

---

## P2 — commitSend: avoid the replay copy

**Cost today.** Every committed outgoing chunk is copied into the replay ledger:

```go
payload := append([]byte(nil), data...)
f.replay = append(f.replay, replayChunk{start: off, end: f.txNext, data: payload})
```

This is the second-largest allocation site: 62.5 MiB, 32%. The copy is needed today because `data` is the pump's read buffer, which is reused for the next read.

**Proposal.**
- Read application bytes directly into a replay-owned buffer and hand ownership to the ledger instead of copying. The pump reads into a chunk from a per-Flow pool sized by its replay reservation, and that chunk becomes `replayChunk.data`.
- When ACKs release a fully acknowledged chunk, return it to the pool.
- The DATA frame payload points at the same memory, which is safe because the replay chunk is immutable until released.

**Expected gain.** Removes about 32% of allocation and one memcpy per sent byte. Combined with P1, roughly 70% of BAFT dataplane allocation goes away.

**Risk.** Higher than P1, because replay memory ownership changes:
- a chunk must not return to the pool while a carrier write or a recovery replay still references it (use-after-release);
- the ordering between ACK release and in-flight writes must be explicit (a reference count or a release generation);
- recovery replay (`replayFramesFrom`) reads these chunks.

**Test plan.**
- Poison released chunks in test builds (fill with a pattern) and run the full suite.
- The recovery churn and topology tests under `-race` at high counts (≥100 runs), since they are the ones that replay.
- Memory accounting checks: allocator replay usage returns to zero after flows close.
- Gates: COR-01 ×10, step57 soak (all recovery scenarios), stagec soak, B06 A/B.

---

## P3 — write/flush coalescing

**Cost today.**
- `protocol.Encode` writes the 24-byte header and then the payload as two writes.
- On the EX (server) side, `h2.flushingWriter` calls `Flush()` after every write.
- So one DATA frame causes two H2 writes and two flushes, each going through HTTP/2 framing, a TLS record and a socket syscall.

The CPU profile shows 54% flat in syscalls, and about 30–36% cumulative in `bufio.Flush`, `crypto/tls.Write` and `http2.writeWithByteTimeout`.

**Measured.** A test-only writer that coalesces each frame's header and payload into one write (B09) raised B06 loopback from 479.8 to 684.7 Mbps median: **+42.7%** (run 37131090548, 8 Flows).

**Proposal, two steps.**
1. `Encode` builds header and payload into one buffer (or uses `net.Buffers` / a single `Write` of a pooled frame buffer), so each frame is one write. This is what B09 measured.
2. Flush per batch, not per write. The sender writes all frames already queued (DRR/PADL output) and then flushes once, bounded by bytes (for example 64 KiB) and by a short time budget, so latency stays low for interactive Flows. This applies to both the client body writer and `flushingWriter`.

**Expected gain.** Step 1: about +40% on loopback (measured). Step 2: more on multi-Flow bulk traffic; to be measured.

**Risk.**
- Step 1 is low risk: the bytes on the wire are identical and only the write grouping changes.
- Step 2 changes latency: a batch must never hold a control frame (ACK/WINDOW/FIN) behind bulk DATA longer than the control-priority rules allow. The existing control-queue priority must flush immediately.
- Record Shaping / Stealth Pro pads per record. Coalescing changes record boundaries, so shaping tests must still pass and the size distribution must be re-checked by R-003. This must happen before step 2 is enabled when shaping is on.

**Test plan.**
- Byte-exact wire tests: Encode output identical before/after.
- Latency test: small request/response RTT through the tunnel, p50/p99 unchanged within noise.
- Shaping tests and the R-003 offline fingerprint comparison.
- Gates: B06/B09 A/B (≥10 runs, CI), netem single-Flow matrix (no regression), COR-01 ×10, soaks, r3.1.
