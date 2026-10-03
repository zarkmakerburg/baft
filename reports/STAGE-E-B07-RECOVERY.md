# Stage E — B07 Carrier-Cut Recovery Statistics

Status: **MEASUREMENT / informational only**

## Provenance

- Branch: `bench/stage-e`
- Workflow run: `37134260399` — **PASS**
- Measured HEAD: `c287ea68907912893c0499139a0a67ae7011941f`
- Artifact ID: `11277669771`
- Cuts: **25**
- Active flows per cut: **64**
- Total flow recovery samples: **1600**

The 64-flow population is the existing production shard ceiling. Stage E did not change that limit.

## Survival

- Flow survival: **1600 / 1600 = 100.0000%**
- Wilson 95% confidence interval for flow survival: **99.7605% .. 100.0000%**
- Logical-session survival: **25 / 25 cuts**
- Target TCP reopens: **0**

## Application-visible recovery latency

Aggregate distribution across all 1600 flow probes:

| Metric | Estimate | 95% bootstrap CI |
|---|---:|---:|
| p50 | **34.356 ms** | **33.252 .. 35.012 ms** |
| p99 | **49.833 ms** | **49.534 .. 51.011 ms** |

Observed range: **10.850 .. 51.990 ms**.

Confidence method: deterministic non-parametric bootstrap, 5000 resamples, fixed seed 7007. Survival interval uses Wilson 95%.

## Limitation

A single cut contains at most 64 active flows because of the current shard limit, so a single-cut p99 is intrinsically coarse. The formal p99 above is therefore the aggregate distribution across 25 repeated cuts on the same runner and probe payload profile. It is not a WAN recovery-time claim.
