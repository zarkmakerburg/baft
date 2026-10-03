# BAFT Stage E Benchmark Lab

This directory starts the isolated Stage E benchmark track defined by `PLAN.md` and the repository layout.

## Scope

The benchmark lab is intentionally non-invasive:

- no production dataplane changes;
- no changes to ECRL, Session, Carrier, topology, or Record Shaping behavior;
- no automatic PR/push benchmark workflow;
- current scenarios consume existing tests/benchmarks as external evidence;
- all initial scenarios are **informational** and do not change current acceptance gates.

This keeps benchmark work separate from the active Worker mission while creating a reproducible evidence path.

## Runner

List scenarios:

```bash
go run ./cmd/baft-bench -list
```

Validate without executing:

```bash
go run ./cmd/baft-bench -scenario B03 -dry-run
```

Run one scenario:

```bash
go run ./cmd/baft-bench -scenario B03
```

Run the complete initial set:

```bash
go run ./cmd/baft-bench -scenario all
```

Results are written under `bench/results/<UTC>-<git-sha>/` with per-attempt stdout/stderr, `summary.json`, and a machine-readable `metrics.json`. Tests emit structured `BAFT_BENCH_METRIC` records which the runner extracts without changing production code.

## Initial scenario set

| ID | Purpose | Current interpretation |
| --- | --- | --- |
| B01 | Record-shape CPU/allocation baseline, formal 60s x5 | microbenchmark only; not public-network throughput |
| B02 | active TCP flow through carrier replacement | recovery/survival evidence |
| B03 | multi-flow carrier replacement | duplicate/loss regression evidence |
| B04 | Topology x ECRL authority matrix | correctness stress evidence |
| B05 | runtime pair FD/resource lifetime | leak/resource evidence |
| B06 | warmed multi-flow loopback BAFT throughput | measured TX/RX/aggregate Mbps + resource snapshot |
| B07 | abrupt Carrier cut on existing flows | session survival + recovery p50/p95/p99 + resource snapshot |

## Evidence rules

A passing scenario means only that the named scenario passed on the recorded commit and machine. It does **not** prove production readiness, Internet-wide throughput, universal detectability properties, or restart/reboot resume.

The runner records command output and environment metadata rather than inventing derived numbers. B06 measures application payload after all flows are warmed, through the real Session + H2/mTLS loopback path. B07 measures application-visible recovery from the instant the active Carrier is cut until each existing flow completes a probe. These are local repeatable measurements, not public-network claims. Profiler capture and baseline regression thresholds remain future work.

## Workflow policy

`.github/workflows/stage-e-benchmark.yml` is `workflow_dispatch` only. It never runs automatically on Worker pushes or pull requests. Results are uploaded as an artifact for inspection.
