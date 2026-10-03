# BAFT Stage E — Prebaseline Evidence

Status: **PREBASELINE / informational only**

This report records the first repeated measurements from the isolated benchmark branch. It does not change BAFT acceptance gates and is not a production-readiness or public-Internet performance claim.

## Provenance

- Branch: `bench/stage-e-foundation`
- Measured commit: `bd1dac6a9ceba7aa1a38cf77750b2fcd15c26a17`
- GitHub Actions run: `37129874583`
- Workflow: `stage-e-prebaseline`
- Go: `go1.27.1 linux/amd64`
- Artifact: `baft-stage-e-prebaseline-bd1dac6a9ceba7aa1a38cf77750b2fcd15c26a17`
- Artifact ID: `11276890618`
- Repeated run conclusion: **PASS**

## B06 — warmed multi-flow loopback throughput

Measurement path:

`application TCP -> BAFT Session -> H2/mTLS Carrier -> BAFT Session -> target TCP -> echo back`

Each repeat used:

- 8 warmed flows;
- 4 MiB payload per flow;
- 32 MiB transmitted application payload;
- 32 MiB echoed application payload;
- fixture setup and payload generation excluded from the timed region;
- 5 repeats.

Observed TX/RX throughput per direction:

| Statistic | Mbps |
| --- | ---: |
| Minimum | 355.96 |
| Median | 512.73 |
| Mean | 512.10 |
| Maximum | 632.05 |

Observed aggregate application payload throughput (TX + RX):

| Statistic | Mbps |
| --- | ---: |
| Minimum | 711.92 |
| Median | 1025.47 |
| Mean | 1024.21 |
| Maximum | 1264.11 |

These values are loopback measurements on a GitHub-hosted runner. They are not WAN throughput and must not be presented as an Internet-speed claim.

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
| per-run p50 | 5.857 ms | 6.228 ms | 6.390 ms | 7.725 ms |
| per-run p95 | 6.181 ms | 6.635 ms | 6.680 ms | 7.835 ms |
| per-run p99 | 6.181 ms | 6.635 ms | 6.680 ms | 7.835 ms |

### Statistical limitation

B07 currently has only 8 flow samples inside each cut, so a per-run p99 has low statistical resolution and collapses to the upper observed samples. A later Stage E revision should use a larger flow population before treating p99 as a formal regression threshold.

## Interpretation

The prebaseline proves that the Stage E harness can:

1. execute the real BAFT data path without production-code instrumentation;
2. produce repeated throughput measurements;
3. inject an abrupt Carrier failure on existing flows;
4. measure application-visible recovery latency;
5. verify that existing target TCP sockets are not reopened;
6. emit machine-readable metrics and preserve raw evidence as a workflow artifact.

No performance threshold is accepted from this prebaseline yet. Direct-TCP comparison, host normalization, profiler capture, WAN/impairment profiles, and cross-commit regression thresholds remain separate Stage E work.
