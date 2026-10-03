# R-003 — Detectability Study Harness

Status: MEASUREMENT HARNESS / no production nodes / no production code changes

This directory defines the reproducible 14-day detectability experiment requested by BAFT HQ. It measures reachability and blocking outcomes; it does not claim or optimize censorship evasion.

## Experiment contract

- Dedicated test servers only.
- One fresh foreign VPS IP per tunnel; never reuse an IP between T1..T10.
- Keep provider, ASN class, region, CPU/RAM class, and OS image as equal as practical.
- At least three Iran-side access networks, preferably in one city.
- Same traffic profiles and schedule for every tunnel.
- Run for 14 days or until the tunnel meets the full-block definition.
- NTP/chrony clock sync at every endpoint.
- Raw data stays private. Shared reports must redact test-server IPs, credentials, keys, source probe IPs, and domain secrets.

## Tunnel matrix

| ID | Tunnel | Measurement identity |
|---|---|---|
| T1 | GRE / IPIP | record exact encapsulation; if 6to4 is used record separately |
| T2 | plain reverse TCP | no application encryption |
| T3 | WS | WebSocket without TLS |
| T4 | WSS direct to IP | self-signed certificate |
| T5 | WSS domain + CDN | record CDN and edge mode |
| T6 | Rathole / FRP / Backhaul | record product/version and TLS/Noise on/off |
| T7 | VLESS Reality | canonical reference implementation/version must be pinned |
| T8 | Hashem tunnel | GRE Layer-3 + FRP reverse relay; record Hashem + FRP versions, FRP transport and TLS settings |
| T9 | BAFT default | pinned Noise over H2/TLS1.3, port 8443, record shaping OFF |
| T10 | BAFT shaped | same as T9, record shaping ON; record exact shaping config hash |

T8 protocol resolution is based on the Hashem upstream architecture: GRE is the L3 link and FRP is the reverse relay inside/alongside that link. Do not collapse T8 into generic FRP in analysis; keep Hashem's exact installed configuration as metadata.

## Operational definitions

Sampling interval: 60 seconds unless an ISP cannot support it reliably.

**First disruption**: first point in a run of 3 consecutive tunnel-health failures after at least 10 minutes of prior healthy samples. A single transient is not counted.

**Full block**: 30 consecutive minutes of tunnel-health failure, or a stable classification-specific failure lasting 30 minutes, while foreign-side health confirms that the test server/service itself is alive.

The 30-minute rule is deliberately conservative so ordinary packet loss or a server restart is not mislabeled as filtering.

## Block classification

Classification is evidence-based and may be `unknown` when tests conflict.

- `ip-wide`: tunnel port, TCP/443 control, unrelated HTTPS on the same IP, and usually ICMP fail from the ISP while foreign health is good.
- `port-only`: tunnel port fails while unrelated HTTPS on the same IP remains healthy.
- `sni`: TCP connect succeeds, but the selected SNI/hostname fails while the control HTTPS hostname/path on the same address remains healthy.
- `protocol-reset`: TCP establishment succeeds, then the tunnel handshake/data path is reset or terminated while ordinary HTTPS remains healthy.
- `throttling`: connectivity survives but the sustained profile falls below the pre-event baseline for repeated 30-minute blocks; report the measured ratio, never infer motive from throughput alone.
- `server-side` / `isp-outage`: use when foreign health or broad ISP controls show the failure is not tunnel-specific.
- `unknown`: evidence is insufficient.

## Traffic profiles

Each tunnel must expose the same test application through the tunnel.

Profile A — light:
- small HTTP requests at randomized, predeclared intervals;
- target bytes/day fixed before the study.

Profile B — sustained:
- fixed 5–10 Mbit/s target for 30-minute blocks;
- same block schedule for every tunnel/ISP pair;
- record attempted and delivered bytes.

Do not adapt the traffic pattern after a tunnel is disrupted; doing so makes survival times incomparable.

## Files

- `study.example.json`: study manifest/template.
- `iran_probe.py`: Iran-side reachability sampler; emits NDJSON.
- `foreign_capture.sh`: passive pcap capture for unsolicited inbound connections.
- `summarize.py`: turns raw sampler logs into redacted tunnel×ISP outcome rows.
- `R003_REPORT_TEMPLATE.md`: shared-report template.

## Raw data layout

Recommended private layout (never commit IP-bearing raw files):

```
raw/R003/<study-id>/
  manifest.private.json
  iran/<isp-id>/<tunnel-id>.ndjson
  foreign/<tunnel-id>/capture-*.pcapng
  foreign/<tunnel-id>/service.log
  fingerprint/*.pcapng
  fingerprint/*.json
```

Shared artifacts contain tunnel IDs, ISP aliases, relative hours, provider/ASN class, fingerprints and aggregate probe statistics, but not real server IPs or credentials.
