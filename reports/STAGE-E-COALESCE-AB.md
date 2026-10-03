# BAFT Stage E — Frame Coalescing A/B

Status: **DIAGNOSTIC / test-only / no production change**

## Provenance

- Branch: `bench/stage-e-foundation`
- A/B commit: `ef439e46ac55570f5f53d1b94f2e3453428271f9`
- A/B workflow run: `37131117237` — **PASS**
- Validation run for B09 registration: `37131090548` — **PASS**
- Artifact ID: `11277350404`
- Go: `go1.27.1 linux/amd64`
- Runner host: `runnervm8df0l`

B06, B09, and B08 were executed on the same runner with the same 8-flow, 4 MiB-per-flow payload profile.

## Scenarios

- **B06:** current BAFT Session + H2/mTLS write behavior.
- **B09:** identical BAFT test path, but a test-only writer coalesces one BAFT frame's header + payload into one underlying writer call before H2.
- **B08:** direct TCP loopback control.

B09 does not modify production code.

## Per-direction throughput

| Scenario | Minimum | Median | Mean | Maximum |
| --- | ---: | ---: | ---: | ---: |
| B06 current BAFT | 452.40 Mbps | **479.76 Mbps** | 476.77 Mbps | 495.29 Mbps |
| B09 frame-coalesced | 591.43 Mbps | **684.68 Mbps** | 655.81 Mbps | 707.64 Mbps |
| B08 direct TCP | 13805.51 Mbps | **16400.08 Mbps** | 15832.39 Mbps | 17607.02 Mbps |

## A/B effect

Median B09 versus B06:

- B06 median: **479.76 Mbps**
- B09 median: **684.68 Mbps**
- Median uplift: **+42.71%**

Relative to direct TCP on the same runner:

- B06 / direct median: **2.93%**
- B09 / direct median: **4.17%**
- B06 relative gap to direct: **97.07%**
- B09 relative gap to direct: **95.83%**

## Interpretation

The A/B result supports the profiling hypothesis that current frame write/flush granularity is a material contributor to the loopback throughput bottleneck.

It does **not** support the conclusion that frame coalescing alone solves Stage E performance. Even after the test-only coalescing experiment, the BAFT path remains far below the direct loopback control.

The remaining measured evidence still points to additional cost centers:

- per-frame payload allocation in `protocol.Decode`;
- replay-ledger copy in `flow.commitSend`;
- H2/TLS/socket stack cost;
- flow-control/backpressure waits.

## Production boundary

No change to the following is made or authorized by this experiment:

- `internal/protocol`
- `internal/session`
- `internal/carrier/h2`
- ECRL or recovery semantics
- production write/flush behavior

Any future production optimization should be a separate HQ-authorized mission with correctness, recovery, race, and fuzz gates preserved.
