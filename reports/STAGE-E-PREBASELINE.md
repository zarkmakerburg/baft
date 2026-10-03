# BAFT Stage E — Prebaseline Evidence

Status: **PREBASELINE / informational only**

This report records repeated measurements from the isolated benchmark branch. It does not change BAFT acceptance gates and is not a production-readiness or public-Internet performance claim.

## Provenance

- Branch: `bench/stage-e-foundation`
- Comparison commit: `74b3f6bbc241f35a4bc3cdfc519f462a9c699af1`
- Repeated comparison run: `37130465793`
- Validation run after workflow repair: `37130504841` — **PASS**
- Go: `go1.27.1 linux/amd64`
- Runner host for B06/B07/B08: `runnervm8df0l`
- Artifact: `baft-stage-e-prebaseline-74b3f6bbc241f35a4bc3cdfc519f462a9c699af1`
- Artifact ID: `11277005119`
- Repeated run conclusion: **PASS**

## B06 — warmed multi-flow BAFT loopback throughput

Measurement path:

`application TCP -> BAFT Session -> H2/mTLS Carrier -> BAFT Session -> target TCP -> echo back`

Each repeat used:

- 8 warmed flows;
- 4 MiB payload per flow;
- 32 MiB transmitted application payload;
- 32 MiB echoed application payload;
- fixture setup and payload generation excluded from the timed region;
- 5 repeats.

Observed BAFT TX/RX throughput per direction:

| Statistic | Mbps |
| --- | ---: |
| Minimum | 268.63 |
| Median | 786.68 |
| Mean | 698.72 |
| Maximum | 870.12 |

Observed aggregate application payload throughput (TX + RX):

| Statistic | Mbps |
| --- | ---: |
| Minimum | 537.26 |
| Median | 1573.36 |
| Mean | 1397.45 |
| Maximum | 1740.25 |

## B08 — direct TCP loopback control

B08 uses the same 8-flow count, 4 MiB per-flow payload, warm-up, hashing, and timed bulk region, but removes BAFT Session and H2/mTLS from the path.

Observed direct-TCP TX/RX throughput per direction:

| Statistic | Mbps |
| --- | ---: |
| Minimum | 17329.53 |
| Median | 20757.24 |
| Mean | 20349.87 |
| Maximum | 21688.43 |

Observed direct-TCP aggregate application payload throughput:

| Statistic | Mbps |
| --- | ---: |
| Minimum | 34659.07 |
| Median | 41514.47 |
| Mean | 40699.75 |
| Maximum | 43376.86 |

### B06 versus B08

Using the median per-direction throughput from the same runner and same payload profile:

- BAFT median: **786.68 Mbps**
- Direct TCP median: **20757.24 Mbps**
- BAFT/direct ratio: **3.79%**
- Relative throughput gap: **96.21%**

This large loopback gap is evidence of substantial current processing overhead in the measured BAFT path. It does **not** mean a WAN user would necessarily lose 96.21% of Internet throughput: direct loopback TCP operates at tens of gigabits per second and is primarily a host/CPU control. The result is a Stage E profiling signal and should be decomposed before setting a performance acceptance threshold.

## B07 — abrupt Carrier cut recovery

Each repeat used:

- one live BAFT logical session;
- 8 existing application flows;
- abrupt cut of the active Carrier;
- recovery to epoch 2 on both IR and EX;
- probe completion measured from the instant of Carrier cut;
- 10 repeats.

Correctness/survival evidence:

- Flow survival: **80 / 80 (100%)**
- Logical-session survival: **10 / 10**
- Target TCP reopen: **0 observed**
- IR/EX recovered epoch agreement: **10 / 10**
- Failed recovery probes: **0**

Application-visible recovery timing across the 10 repeated runs:

| Metric | Minimum | Median | Mean | Maximum |
| --- | ---: | ---: | ---: | ---: |
| per-run p50 | 4.241 ms | 4.826 ms | 5.016 ms | 6.689 ms |
| per-run p95 | 4.258 ms | 5.062 ms | 5.199 ms | 6.795 ms |
| per-run p99 | 4.258 ms | 5.062 ms | 5.199 ms | 6.795 ms |

### Statistical limitation

B07 currently has only 8 flow samples inside each cut, so a per-run p99 has low statistical resolution and collapses to the upper observed samples. A later Stage E revision should use a larger flow population before treating p99 as a formal regression threshold.

## Interpretation and next measurement gate

The prebaseline proves that the Stage E harness can:

1. execute the real BAFT data path without production-code instrumentation;
2. produce repeated throughput measurements;
3. compare BAFT against a same-host direct-TCP control;
4. inject an abrupt Carrier failure on existing flows;
5. measure application-visible recovery latency;
6. verify that existing target TCP sockets are not reopened;
7. emit machine-readable metrics and preserve raw evidence as a workflow artifact.

The direct-control comparison also creates a new Stage E profiling question: identify where the B06 loopback processing cost is spent before introducing a hard throughput gate. CPU profiling, allocation profiling, component-level baselines, WAN/impairment profiles, and cross-commit regression thresholds remain separate Stage E work.
