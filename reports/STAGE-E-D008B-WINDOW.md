# Stage E D-008B item 1: single-flow throughput vs window/RTT

Status: DONE (measurement only, no production code). Confidence: PROVEN.

## Provenance

- Workflow `stage-e-d008b-window-bdp`, run 37149652629 (attempt 1, success)
- Head `a4da668903274f9c4cd7c6639b9f7a5484c56c22` (`bench/stage-e-foundation`)
- GitHub-hosted Linux runner. netem delay on loopback, loss 0%.
- 1 flow, 2 MiB per direction, 10 repetitions per cell
- Commands: see `.github/workflows/stage-e-d008b-window.yml`
- `receive_window_bytes` = 65536, which is `defaultWindow` in `internal/session/session.go:23`

## Result (per-direction Mbps, median of 10)

| RTT | Ceiling = 64 KiB / RTT | BAFT measured | BAFT / ceiling | Direct TCP (same payload) |
|---|---|---|---|---|
| 50 ms | 10.49 | 10.07 | 96% | 14.54 |
| 150 ms | 3.50 | 3.37 | 96% | 4.85 |
| 300 ms | 1.75 | 1.69 | 97% | 2.43 |

The spread is tight. At 150 ms and 300 ms, 9 of the 10 BAFT runs fall within 1% of the median and one run was about 3% slower. At 50 ms, 7 runs are within 1% of the median and 3 runs were about 3% slower (9.77 Mbps).

## Findings

1. **A single BAFT flow sits exactly on the window ceiling.** At every RTT, throughput is 96–97% of `window / RTT`. The 3–4% gap is framing and credit-update latency. CPU is not a factor at these rates. So on WAN paths, per-flow throughput is set by the 64 KiB flow-control window, not by the codec or TLS.
2. **The direct-TCP control limits this comparison.** A 2 MiB transfer ends while the kernel is still in slow start, so the direct column shows slow start, not steady-state TCP. Steady-state direct TCP would reach the netem/loopback limit. The real gap at high RTT is therefore larger than the 1.4× in the table. A follow-up needs a payload of at least 32 MiB or a timed run to compare steady state.
3. A larger window scales throughput linearly until the next limit: the path bandwidth, the app-conn socket buffers (fixed at 4 MiB since #77), or CPU (about 550 Mbps aggregate on loopback, E1).

## Proposal (not code; needs HQ approval as a separate mission)

The window needed for a target rate R at round-trip time RTT is W = R × RTT:

| Target per flow | 50 ms | 150 ms | 300 ms |
|---|---|---|---|
| 10 Mbps | 64 KiB (today) | 192 KiB | 384 KiB |
| 50 Mbps | 320 KiB | 960 KiB | 1.9 MiB |
| 100 Mbps | 640 KiB | 1.9 MiB | 3.75 MiB |

Recommendation: make the per-flow receive window grow from 64 KiB up to a 4 MiB cap, doubling while the receiver drains promptly, similar to TCP receive-window autotuning. Add a per-session memory budget (e.g. 64 MiB) so that many idle-but-open flows cannot pin memory. A fixed 1 MiB window would remove most of the gap at 150 ms (up to about 56 Mbps per flow), but its memory cost scales with the number of flows.

Risks:
- The window feeds into COR-T1-style backpressure, so the 4 MiB app-conn buffers must stay at least as large as the maximum window.
- Recovery replay buffers grow with the window. Recovery soaks (step57) and COR-01 must be re-run.

Test plan for the future mission:
- Repeat this matrix with the new window and expect it to reach `min(new ceiling, path rate)`.
- Re-run the B06 loopback baseline and check for no regression beyond noise.
- Run COR-01 at least 10 times, step57 soak, r3.1, plus a memory test with many flows against the session budget.
