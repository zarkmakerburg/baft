# Stage E D-008B item 2: BAFT vs FRP / Rathole / Xray / direct TCP

Status: DONE (measurement only). Confidence: LIKELY. Five runs per cell; the 95% CIs are wide in some cells.

## Provenance

- Workflow `stage-e-comparators`, run 37189635464, head `9bfd398` (`hq/stage-e-comparators`). BAFT is main as of `b5df6d0`, with RTT-gated window autotune (#83).
- One GitHub-hosted runner for every tool. Tool versions are the latest upstream releases, with tag and SHA-256 in the run's `provenance.txt` artifact.
- Probe `bench/comparators/tput.py`:
  - one connection, 16 MiB each way at the same time through an echo target;
  - SHA-256 verified;
  - Mbps per direction = 8 · 16 MiB / wall time.
- Same echo target for every tool.
- netem delays/loses both directions of each tool's **carrier port** only. The entry and target legs stay on plain loopback.
- Xray: VLESS over plain TCP (no TLS). FRP and Rathole: default settings with a token. BAFT: paired with `baft-pair` exactly as `install.sh` does (Noise + H2/mTLS).
- Earlier runs 37185074533 and 37185403169 failed or are not comparable: the FRP warm-up was not retried, and the probe half-closed, which FRP does not forward. Fixed in 3d1b062 and 9bfd398.

## Result (median Mbps per direction, ± 95% CI half-width, n = 5)

| RTT | loss | direct | **BAFT** | FRP | Rathole | Xray |
|---|---|---|---|---|---|---|
| loopback | 0 | 1545 ±39 | **410 ±20** | 1050 ±21 | 932 ±135 | 1533 ±99 |
| 50 ms | 0 | 102 ±6 | **76 ±9** | 175 ±29 | 16.7 ±0.1 | 98 ±15 |
| 150 ms | 0 | 38.2 ±1.9 | **26.0 ±1.6** | 78 ±6 | 5.6 ±0.0 | 27.5 ±11.5 |
| 300 ms | 0 | 18.6 ±1.5 | **15.2 ±1.0** | 43.7 ±5.4 | 2.8 ±0.3 | 16.4 ±5.0 |
| 150 ms | 1% | 39.7 ±3.0 | **17.2 ±1.7** | 39.8 ±8.2 | 6.0 ±2.5 | 30.6 ±3.1 |

## Findings

1. **"direct" is not a ceiling here.** One plain TCP connection over the netem'd loopback port reaches only about 0.7 MB in flight (the same cap appears in the Stage E Go harness). FRP beats it on every WAN cell because FRP's carrier is a long-lived, already warmed connection with yamux's large windows, while every direct run starts a cold TCP connection.
2. **BAFT is behind FRP on WAN (about 2.9× at 50–300 ms) and level with Xray.** It is far ahead of Rathole, which is capped by a small fixed window of about 100 KB.
   - BAFT's receive window starts at 64 KiB and doubles once per two RTTs. Reaching 4 MiB takes 6 doublings, which is 12 RTT (1.8 s at 150 ms).
   - A 16 MiB transfer at those rates lasts only a few seconds, so a large part of it runs on a small window.
   - The Go harness with a longer, warmed transfer measured 35.6 Mbps at 150 ms. This probe's 26 Mbps fits a ramp-limited transfer. LIKELY, not proven.
3. **BAFT loses most under loss (17 vs about 40 for FRP and direct).** Two causes are plausible and not separated yet:
   - the window ramp restarts slowly;
   - TCP on the carrier backs off and the BAFT window does not compensate.
4. **Loopback: BAFT 410 vs 930–1550 for the others.** This is the per-frame CPU cost the profile measured (two writes and flushes per frame, decode allocation, replay copy). It is the target of P3 and P1 (#88, #89: +12–14% each locally) and P2.

## Recommendations

- **R1 (measure, next):** repeat with timed 30 s transfers and with 4 parallel connections, to separate ramp from steady state; add a cell with 1% loss at 50 ms.
- **R2 (proposal, needs approval):** a faster initial ramp. Start the window at 256 KiB (yamux's default) or quadruple while the turn time is far below 2·RTT. Memory risk is bounded by the existing half-pool headroom.
- **R3:** land P3/P1/P2 and re-run this matrix. Loopback and the 50 ms cell should move the most.
