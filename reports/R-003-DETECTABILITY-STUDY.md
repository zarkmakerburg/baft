# R-003 — Detectability Study Status Report

Status: **HARNESS READY / OFFLINE FINGERPRINT PARTIAL / IRAN FIELD DATA PENDING**

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

- Version observed: Mozilla Firefox 156.0
- First capture produced no ClientHello packets and therefore no valid JA3/JA4.
- Status: **RETEST REQUIRED**. Do not treat the empty output as a Firefox fingerprint.

## Immediate offline finding

In the measured fixtures, BAFT's Go `crypto/tls` ClientHello is **not fingerprint-equivalent to Chrome**:

- BAFT JA4: `t13d0312h2_55b375c5d22e_b36ed9cfacdc`
- Chrome primary JA4: `t13d1517h2_8daaf6152771_cb7bf5808d99`

This is a fingerprint difference, not proof that an ISP detects or blocks BAFT because of it. The 14-day field study is required for that causal question.

## Evidence artifacts

- R-003 harness validation: run `37132577705` — PASS
- Browser fingerprint run: `37132696163` — PASS
- BAFT TLS fingerprint run: `37132795196` — PASS
- BAFT fingerprint artifact ID: `11277442920`
- Browser fingerprint artifact ID: `11276639445`

Raw PCAPs contain endpoint data and remain private; they must not be copied into the shared report.
