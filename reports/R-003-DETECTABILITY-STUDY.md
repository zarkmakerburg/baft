# R-003 — Detectability Study Status Report

Status: **HARNESS READY / FIREFOX + BAFT COVER CLOSED / T4–T8 OFFLINE PINS BLOCKED / IRAN FIELD DATA PENDING**

Branch: `bench/stage-e-foundation`

## Measurement boundary

R-003 is measurement-only. No production BAFT node, transport behavior, record-shaping behavior, TLS fingerprint, or cover-page behavior has been changed.

The requested 14-day tunnel×ISP survival table cannot be populated until dedicated test VPSs and at least three Iran-side access networks are actually feeding observations into the harness. Missing values remain `PENDING`; no synthetic blocking time is reported.

## Tunnel matrix clarification

T8 is resolved as **Hashem GRE Layer-3 + FRP reverse relay with FRP TLS enabled in the standard setup**. Record the exact Hashem/FRP version and any non-default carrier option in the private manifest.

## Tunnel × ISP outcomes

| Tunnel | ISP | First disruption | Full block | Block type | Volume relation | Fresh-IP follow-up |
|---|---|---:|---:|---|---|---|
| T1 | ISP-A | PENDING | PENDING | PENDING | PENDING | PENDING |
| T1 | ISP-B | PENDING | PENDING | PENDING | PENDING | PENDING |
| T1 | ISP-C | PENDING | PENDING | PENDING | PENDING | PENDING |
| T2 | ISP-A | PENDING | PENDING | PENDING | PENDING | PENDING |
| T2 | ISP-B | PENDING | PENDING | PENDING | PENDING | PENDING |
| T2 | ISP-C | PENDING | PENDING | PENDING | PENDING | PENDING |
| T3 | ISP-A | PENDING | PENDING | PENDING | PENDING | PENDING |
| T3 | ISP-B | PENDING | PENDING | PENDING | PENDING | PENDING |
| T3 | ISP-C | PENDING | PENDING | PENDING | PENDING | PENDING |
| T4 | ISP-A | PENDING | PENDING | PENDING | PENDING | PENDING |
| T4 | ISP-B | PENDING | PENDING | PENDING | PENDING | PENDING |
| T4 | ISP-C | PENDING | PENDING | PENDING | PENDING | PENDING |
| T5 | ISP-A | PENDING | PENDING | PENDING | PENDING | PENDING |
| T5 | ISP-B | PENDING | PENDING | PENDING | PENDING | PENDING |
| T5 | ISP-C | PENDING | PENDING | PENDING | PENDING | PENDING |
| T6 | ISP-A | PENDING | PENDING | PENDING | PENDING | PENDING |
| T6 | ISP-B | PENDING | PENDING | PENDING | PENDING | PENDING |
| T6 | ISP-C | PENDING | PENDING | PENDING | PENDING | PENDING |
| T7 | ISP-A | PENDING | PENDING | PENDING | PENDING | PENDING |
| T7 | ISP-B | PENDING | PENDING | PENDING | PENDING | PENDING |
| T7 | ISP-C | PENDING | PENDING | PENDING | PENDING | PENDING |
| T8 | ISP-A | PENDING | PENDING | PENDING | PENDING | PENDING |
| T8 | ISP-B | PENDING | PENDING | PENDING | PENDING | PENDING |
| T8 | ISP-C | PENDING | PENDING | PENDING | PENDING | PENDING |
| T9 | ISP-A | PENDING | PENDING | PENDING | PENDING | PENDING |
| T9 | ISP-B | PENDING | PENDING | PENDING | PENDING | PENDING |
| T9 | ISP-C | PENDING | PENDING | PENDING | PENDING | PENDING |
| T10 | ISP-A | PENDING | PENDING | PENDING | PENDING | PENDING |
| T10 | ISP-B | PENDING | PENDING | PENDING | PENDING | PENDING |
| T10 | ISP-C | PENDING | PENDING | PENDING | PENDING | PENDING |

## Active-probe summary

No public-Internet listener has been attached to R-003 field collection yet, so unsolicited-probe counts are **PENDING**.

The harness contains private PCAP capture/summarization support and the shared-output contract excludes raw source IPs. For BAFT, the pre-auth/cover behavior has a separate validation gate; temporal proximity between a probe and a later block will be reported as correlation, not causation.

## Offline TLS fingerprints

### BAFT Go TLS/H2 integration path

Workflow run: `37132795196` — PASS

- TShark: Wireshark 4.2.2
- JA3: `1bcbceb7d6c06f583eeb65246997739f`
- JA4: `t13d0312h2_55b375c5d22e_b36ed9cfacdc`
- SNI in fixture: `ex.test`
- ALPN: `h2`
- Certificate observation: integration fixture uses test PKI; production-field certificate observation remains PENDING.

T9 and T10 share this outer TLS implementation in the current study definition. Record shaping changes application-record behavior, not the TLS ClientHello fingerprint itself, unless implementation changes later.

### Chrome control

Workflow run: `37132696163` — PASS

- Version: Google Chrome 154.0.8037.57
- Representative JA4: `t13d1517h2_8daaf6152771_cb7bf5808d99`
- Additional observed resumed/variant JA4: `t13d1518h2_8daaf6152771_e2d80978ab2e`
- Observed JA3 digests across four ClientHellos:
  - `0deb44c3f4fb3b57e210c5a220b5c9d8`
  - `48b709b1f5e73b255460aa0a5a677cf7`
  - `cd65375d36dfd6b2b1f0f18b49c1c5b2`
  - `6c4b46562d3a31e98b8f9e9e4e7d133f`

Chrome's JA3 digest varies here because extension ordering/connection state varies, while the primary JA4 form is substantially more stable.

### Firefox control

The first attempt, run `37132696163` at `d07f173141dd92307449d1eb57d2b08e3757cef8`, completed successfully as a workflow but produced no Firefox ClientHello. It is retained as an invalid measurement attempt, not overwritten by the reruns.

Retest run `37132902824` at `c7d321be47ce82911304c8bc050acb3aadbeacfe` produced valid Firefox ClientHellos. An exact-current-head confirmation was then dispatched:

- Run: `37147117325` — PASS
- Job: `111273253517`
- SHA: `5a6b818cc1d589b8752b6de5b381402b262ad508`
- Artifact: `11282486405`
- Version: Mozilla Firefox 156.0
- JA3: `a6d822a3f8c8542526830987918a61f9`
- JA4: `t13i1516h2_8daaf6152771_3cbfd9057e0d`
- Captured ClientHello: valid; raw PCAP remains private.

The same JA3/JA4 pair was also observed in the preceding successful retest at `c7d321b…`, providing cross-run reproducibility.

Status: **FIREFOX RETEST CLOSED / PROVEN for the measured Linux headless Firefox 156 fixture**.

## Immediate offline finding

In the measured fixtures, BAFT's Go `crypto/tls` ClientHello is **not fingerprint-equivalent to Chrome**:

- BAFT JA4: `t13d0312h2_55b375c5d22e_b36ed9cfacdc`
- Chrome primary JA4: `t13d1517h2_8daaf6152771_cb7bf5808d99`

This is a fingerprint difference, not proof that an ISP detects or blocks BAFT because of it. The 14-day field study is required for that causal question.


## Offline closure status

| Item | Status | Evidence | Confidence |
|---|---|---|---|
| Firefox retest | DONE | Runs `37132902824` and `37147117325`; exact-head JA3/JA4 reproduced | PROVEN |
| BAFT pre-auth cover behavior | DONE | Exact-head local tests `TestNoiseHTMLFallbackAndAuthenticatedHTTP2` and `TestNoiseSlowProbeBounded` PASS; prior harness validation `37132577705` PASS | PROVEN |
| Certificate observer tool | READY | `bench/r003/cert_observe.py` compiles on exact head and records TLS/certificate metadata to a private observation | PROVEN |
| T4–T8 client JA3/JA4 | BLOCKED | Exact implementation/version fields are not frozen in `bench/r003/FROZEN_MATRIX.md` rows T4–T8 | UNKNOWN until pinned and captured |
| T4–T8 presented-certificate classes | BLOCKED | Endpoints/configs do not exist yet and exact TLS/CDN/Reality/FRP variants are not frozen | UNKNOWN until pinned and captured |

### BAFT cover check on exact current head

At `5a6b818cc1d589b8752b6de5b381402b262ad508`, the following measurement-only local check passed:

```text
TestNoiseHTMLFallbackAndAuthenticatedHTTP2   PASS
TestNoiseSlowProbeBounded                    PASS
```

The code path in `internal/carrier/h2/noise.go` routes ordinary/non-carrier pre-authentication requests to the configured cover. This proves the tested application invariant only; it does **not** prove browser-equivalent TLS/wire fingerprinting.

### T4–T8 freeze blocker

The study cannot publish defensible client JA3/JA4 values for T4–T8 until the actual client implementations are defined. The current frozen matrix still contains placeholders for:

- T4: exact WSS tool/version;
- T5: exact WSS tool/version plus CDN/edge mode;
- T6: exact Rathole/FRP/Backhaul choice, version and TLS/Noise mode;
- T7: exact Xray/VLESS Reality version/config hash;
- T8: exact Hashem + FRP versions, `transport.protocol`, `transport.tls.enable` and related TLS settings.

Selecting a convenient local implementation now would create fingerprints for a different experiment. These fields must be frozen first, then the exact binaries/configs captured.

### Traffic-contract inconsistency to freeze before day 0

Two current study files disagree on sustained Profile B:

- `bench/r003/study.example.json`: **7.5 Mbit/s** for 30 minutes;
- `studies/r003/traffic-profile.json`: **8 Mbit/s** for 1800 seconds.

The rate and cadence must be frozen once before field collection; otherwise volume-dependent comparisons and ISP data-cost estimates are not comparable.

## R-005 infrastructure plan — #field DECISION NEEDED

**Plan only. No server, IP, CDN plan, SIM, data package, domain, or other paid resource has been purchased.**

### Proposed topology

1. **10 active foreign test VPSs** — one dedicated fresh IPv4 for each T1–T10.
   - Proposed location: **Nuremberg, Germany (NBG), same location for all ten**.
   - Proposed provider/class: **Hetzner Cloud CX23, x86_64, Primary IPv4**, same image/host class as far as the provider exposes.
   - Reason: keeps provider/ASN/region/VM-class constant so tunnel type is the principal changed variable.
2. **1 independent foreign health/controller VPS**.
   - Proposed provider/location: **OVHcloud VPS-1, Germany (Limburg)**.
   - Purpose: independent server-health checks, certificate observation and collection of redacted aggregates; it is not a tunnel-under-test node.
3. **Generation-2 replacements: up to 10 additional on-demand VPS/IP allocations**, created only after a tunnel reaches the frozen full-block criterion. They are not part of the baseline purchase and require the approved budget ceiling.

### Current public price basis (checked 2026-10-03)

Hetzner's 15 June 2026 list price for CX23 in Germany/Finland is **€5.49/month excluding IPv4 and VAT**. A Cloud Primary IPv4 is **€0.50/month excluding VAT**. Therefore:

- per T1–T10 node: **€5.99/month excl. VAT**;
- ten active tunnel nodes: **€59.90/month excl. VAT**;
- if 19% VAT applies: approximately **€71.28/month** for the ten-node matrix.

OVHcloud Germany currently lists VPS-1 from **€4.53/month including VAT**, with 2 vCores, 4 GB RAM and unlimited traffic.

Budgetary baseline: approximately **€75.81/month including VAT** if 19% VAT applies to the Hetzner portion, **before** any domain/CDN/ISP-access costs.

Worst-case budget ceiling if all ten generation-2 replacements overlap for a full month: approximately **€147.09/month including VAT**, before domain/CDN/ISP costs. Actual replacement cost can be lower with hourly billing and late allocation.

Hetzner documents **20 TB included monthly traffic for EU CX cloud servers**, which is comfortably above the planned per-node study traffic. Hetzner also documents a default limit of up to five cloud servers; the account may therefore need a quota increase before a ten-node same-provider matrix can be created.

Price/source references:
- https://docs.hetzner.com/general/infrastructure-and-availability/price-adjustment/
- https://docs.hetzner.com/cloud/servers/primary-ips/overview/
- https://docs.hetzner.com/robot/general/traffic/
- https://docs.hetzner.com/cloud/servers/overview/
- https://www.ovhcloud.com/de/vps/vps-deutschland/

### Iran-side ISP access method

Use **three physically independent retail access paths in the same city** where practical:

- ISP-A: fixed-line broadband;
- ISP-B: mobile network #1;
- ISP-C: mobile network #2.

Each path gets its own small probe host/device running the same frozen sampler and traffic schedule. Measurement traffic must leave directly through that ISP, with no management VPN/proxy on the measurement path. Management/collection should use a separate channel so loss of a tunnel under test does not hide the observation.

Prefer already-owned lines/SIMs/hardware to avoid new spending. If new SIMs, data packages, routers, modems or probe hardware are required, they need separate approval through HQ.

### ISP data-budget consequence

The current 7.5-vs-8 Mbit/s inconsistency must be resolved before purchasing mobile data:

- 7.5 Mbit/s × 30 min ≈ **1.6875 GB** per sustained block;
- 8 Mbit/s × 30 min ≈ **1.8 GB** per sustained block.

One sustained sweep across 10 tunnels × 3 ISPs is therefore about **50.625–54 GB**. If that sweep ran once per day for all 14 days, it would consume about **708.75–756 GB total**, or **236.25–252 GB per ISP**, before Profile-A traffic and protocol overhead. The actual cadence is not yet frozen, so this is a planning envelope, not a purchase recommendation.

### Approval gate

Before any paid infrastructure action, HQ/user approval is required for:

- the 10 + 1 baseline server layout and budget;
- permission to request/prepare capacity for up to 10 generation-2 replacements;
- the T5 CDN/domain choice and any associated cost;
- any new Iran-side SIM/data/hardware cost;
- the exact T4–T8 implementation/version pins;
- one final Profile-B bitrate and cadence.


## Evidence artifacts

- R-003 harness validation: run `37132577705` — PASS
- Browser fingerprint first attempt: run `37132696163` at `d07f173141dd92307449d1eb57d2b08e3757cef8` — workflow PASS, Firefox measurement INVALID (no ClientHello)
- Browser Firefox retest: run `37132902824` at `c7d321be47ce82911304c8bc050acb3aadbeacfe` — PASS
- Browser exact-head confirmation: run `37147117325` at `5a6b818cc1d589b8752b6de5b381402b262ad508` — PASS; job `111273253517`; artifact `11282486405`
- BAFT TLS fingerprint run: `37132795196` at `55c2bcc82f2597391967904425e0b3d1001375b3` — PASS
- BAFT fingerprint artifact ID: `11277442920`
- Original browser artifact ID: `11276639445`
- Firefox retest artifact ID: `11277717351`

Raw PCAPs contain endpoint data and remain private; they must not be copied into the shared report.
