# R-003 — Detectability / Blocking Measurement Study

Status: **measurement harness ready; field data pending dedicated test nodes**

This study measures disruption/blocking behavior. It does not alter BAFT transport behavior, record shaping, TLS fingerprints, cover behavior, or production nodes.

## Safety and evidence boundary

- dedicated non-production servers only;
- one fresh foreign VPS IP per tunnel; never reuse an IP between T1–T10;
- same provider/ASN class and region for all foreign VPSs;
- at least three Iran-side access networks, anonymized in shared output as ISP-A/B/C;
- real IPs, credentials, keys, domains that identify the test hosts, and raw probe payloads stay under `studies/r003/private/` and are gitignored;
- shared reports contain only tunnel IDs, anonymized ISP IDs, timing, byte counts, block classifications, source-ASN summaries, and cryptographic fingerprints.

## Tunnel matrix

| ID | Stack under test |
| --- | --- |
| T1 | GRE / IPIP (record exact variant; 6to4 only if actually used) |
| T2 | plain reverse TCP, no encryption |
| T3 | WebSocket, no TLS |
| T4 | WSS direct to IP, self-signed certificate |
| T5 | WSS via domain + CDN |
| T6 | Rathole / FRP / Backhaul; record implementation and TLS/Noise on/off |
| T7 | VLESS Reality reference baseline |
| T8 | Hashem default: GRE L3 + FRP reverse relay with FRP TLS enabled; record any non-default carrier separately |
| T9 | BAFT default: Noise over H2/TLS 1.3, port 8443, record shaping OFF |
| T10 | BAFT same setup with record shaping ON |

T8 is intentionally not treated as a black-box protocol: the upstream Hashem documentation describes GRE plus FRP, with FRP TLS enabled in its standard setup.

## Field duration

Each tunnel × ISP cell runs for 14 days or until a confirmed full block. Clocks on both ends must be NTP-synchronized.

Connectivity snapshots should be recorded at a fixed cadence (recommended: every 5 minutes) and additionally immediately after a tunnel-health transition.

## Traffic profiles

Use identical application payload traces for every tunnel.

### Profile A — light browsing-like

Replay the same fixed object-size/think-time trace from `traffic-profile.json`. The content origin should be owned by the study and reached through the tunnel. Do not use third-party websites as load generators.

### Profile B — sustained

A rate-limited application transfer at 8 Mbit/s for exactly 30 minutes per block. Record start/end time and bytes transferred. The selected rate sits inside the requested 5–10 Mbit/s range and is fixed to avoid tunnel-to-tunnel bias.

## Required connectivity observations

For every tunnel × ISP snapshot record:

1. tunnel application health;
2. ICMP reachability to the foreign test IP;
3. TCP connect to 443 on that IP;
4. TCP connect to the tunnel port;
5. an unrelated HTTPS control vhost hosted on the same foreign IP;
6. if relevant, TLS handshake result for the tunnel SNI;
7. bytes transferred since study start.

Do not infer a block from one failed probe. Mark `disrupted` only after the study controller's confirmation rule is met; mark `blocked` only after the diagnostics below establish a stable failure mode.

## Block classification

Use the following labels in observations:

- `ip-wide`: tunnel fails and the unrelated same-IP HTTPS control plus TCP controls fail from the affected ISP while the server is healthy from independent vantage points.
- `port-only`: tunnel port fails while unrelated HTTPS/TCP controls on the same IP remain reachable.
- `sni`: TCP reachability remains, but the affected SNI/TLS hostname fails while the unrelated control hostname on the same IP succeeds.
- `protocol-reset`: TCP establishment succeeds but the tunnel session is reset/fails consistently at the protocol stage, while same-IP controls remain healthy.
- `throttling`: tunnel remains usable but sustained measured throughput falls below the predeclared degradation threshold for repeated blocks while same-IP controls remain healthy.
- `unknown`: evidence is insufficient or contradictory.

The report must preserve `unknown` instead of forcing a censorship attribution.

## Fresh-IP recovery experiment

When a cell reaches confirmed full block, replace only that tunnel's foreign VPS with a fresh IP from the same provider/ASN class and region. Preserve the tunnel configuration and traffic profile. Record the second time-to-disruption and time-to-block separately; never recycle the old IP into another tunnel cell.

## Active-probe evidence

For every foreign listener, capture inbound connections that are not the expected Iran client. Raw PCAP remains private. The shared report stores:

- tunnel ID;
- timestamp;
- HMAC/hash token for the remote source, not its IP;
- source ASN if resolved;
- whether application bytes were sent;
- high-level first-message class (TLS ClientHello, HTTP request, empty SYN/connect, malformed/other).

For BAFT, use a controlled unauthenticated request from a dedicated probe node to verify the configured cover-page response. Record the observation, then correlate later inbound probes and block timing. Do not treat temporal proximity alone as proof of causation.

## TLS fingerprint capture

Capture each TLS client's ClientHello to PCAP. Wireshark/TShark 4.2+ exposes both `tls.handshake.ja3` and `tls.handshake.ja4`; `scripts/fingerprint.sh` extracts them. Keep IP-bearing PCAPs private.

Compare:

- BAFT Go `crypto/tls`;
- Chrome;
- Firefox;
- T4;
- T5;
- T7/Reality where a conventional TLS ClientHello is observable.

Certificate observation must include only certificate type/issuer class in the shared report (self-signed, public CA, CDN edge, etc.); private hostnames/IPs stay out.

## Data flow

```text
Iran test nodes / foreign sensors
          |
          v
studies/r003/private/events.jsonl
          |
          v
go run ./cmd/baft-r003
          |
          +--> studies/r003/out/summary.json
          +--> final shared table/report
```

The summarizer never emits raw IP addresses or credentials because those fields are intentionally absent from its shared schema.

## Current evidence status

No Iran field observations have been collected by this repository yet. Therefore time-to-disruption, time-to-block, probe counts, and JA3/JA4 values must remain **PENDING** until dedicated test nodes feed raw observations into the harness.
