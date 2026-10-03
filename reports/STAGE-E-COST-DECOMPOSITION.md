# Stage E — Loopback Cost Decomposition

Status: **MEASUREMENT / informational only / no production changes**

## Provenance

- Branch: `bench/stage-e`
- Workflow run: `37134789993` — **PASS**
- Measured HEAD: `4f074b86ef5f25ef9806e3c78b3071eb68126fe0`
- Artifact ID: `11278606302`
- Validation run: `37134764665` — **PASS**

B06, B08, B10 and B11 were executed in one workflow job on one GitHub-hosted runner.

## Fixed-payload elapsed-time decomposition

All throughput scenarios use the same 8 warmed flows and 4 MiB payload per flow.

| Scenario | Repeats | Median TX | Median elapsed |
|---|---:|---:|---:|
| B08 direct TCP | 5 | **15100.55 Mbps** | **17.777 ms** |
| B10 BAFT Session over raw TCP | 5 | **2103.40 Mbps** | **127.620 ms** |
| B06 BAFT Session over H2/TLS | 5 | **709.75 Mbps** | **378.211 ms** |

For this fixed payload, B06 adds **360.434 ms** above the direct-TCP median.

- BAFT Session/framing/replay/flow-control over raw TCP, B10−B08: **109.843 ms = 30.48%** of the B06 excess-time denominator.
- H2+TLS carrier increment, B06−B10: **250.591 ms = 69.52%** of the same excess-time denominator.

This A/B decomposition changes only the test carrier; production code is unchanged.

## Allocation decomposition

Fresh B06 `alloc_space` profile on the same workflow runner:

- `flow.commitSend` replay-ledger copy: **38.32%**
- `protocol.Decode`: **33.84%**

B11 directly measured a 32 KiB DATA decode:

- median allocations per decode: **2.00**
- median allocated bytes per decode: **32792.24 bytes**

The Decode and commitSend figures are percentages of allocation space, **not elapsed-time shares**.

## Flow-control / backpressure

Fresh B06 block profile cumulative delay:

- `receiveRing.Peek`: **41.39%**
- `flow.reserveReadCapacity`: **31.78%**

These are cumulative blocking-delay shares. They can overlap call stacks and are neither CPU percentages nor additive with allocation or elapsed-time shares.

## Interpretation

Evidence supports four distinct cost centers:

1. the H2+TLS carrier is the largest measured contributor to **excess elapsed time** in this loopback profile;
2. replay-ledger copying is the largest measured single **allocation-space** site;
3. per-frame Decode allocation is another major **allocation-space** site;
4. flow-control/read-side waits are prominent in the **blocking-delay** profile.

No optimization is applied by Stage E. Frame coalescing and any future allocation/replay changes remain proposals requiring a separate HQ-authorized production mission and correctness gates.
