# R-003 — Offline TLS Fingerprint Evidence

Status: MEASURED FOR BAFT + BROWSER CONTROLS; T4/T5/T7 CLIENTS PENDING THEIR ACTUAL DEPLOYMENTS

## Provenance

BAFT:
- workflow run: `37132495478` — PASS
- artifact: `11277457433`
- Go: `go1.27.1 linux/amd64`
- SNI fixture: `ex.test`
- ALPN: `h2`
- official FoxIO JA4 source commit: `16b96d95c220762cf658f67d678cda2aac95c81e`

Browser controls:
- workflow run: `37132902824` — PASS
- artifact: `11277717351`
- Chrome: `Google Chrome 154.0.8037.57`
- Firefox: `Mozilla Firefox 156.0`
- local control endpoint: TLS server on loopback; browser certificate acceptance is irrelevant to ClientHello capture
- official FoxIO JA4 source commit: `16b96d95c220762cf658f67d678cda2aac95c81e`

## JA3 / JA4 observations

| Client | JA3 digest | JA4 | Observation |
|---|---|---|---|
| BAFT Go crypto/tls + h2 | `1bcbceb7d6c06f583eeb65246997739f` | `t13d0312h2_55b375c5d22e_a089bac06eae` | stable across 2 captured ClientHellos |
| Chrome 154 | multiple due extension ordering | modal: `t13d1517h2_8daaf6152771_cb7bf5808d99` | first 3 captures shared this JA4; one additional connection used an 18-extension JA4 |
| Firefox 156 | `a6d822a3f8c8542526830987918a61f9` | `t13i1516h2_8daaf6152771_3cbfd9057e0d` | stable across 3 captured ClientHellos |

Chrome JA3 digests observed in one headless navigation:

```
0deb44c3f4fb3b57e210c5a220b5c9d8
48b709b1f5e73b255460aa0a5a677cf7
cd65375d36dfd6b2b1f0f18b49c1c5b2
6c4b46562d3a31e98b8f9e9e4e7d133f
```

Chrome JA4 values observed:

```
t13d1517h2_8daaf6152771_cb7bf5808d99
t13d1518h2_8daaf6152771_e2d80978ab2e
```

The first value occurred on three captures; the second on one additional connection with a different extension count.

## BAFT raw JA3

```
771,4865-4866-4867,0-11-65281-23-18-5-10-13-50-16-43-51,4588-4587-4589-29-23-24-25,0
```

## Firefox raw JA3

```
771,4865-4867-4866-49195-49199-52393-52392-49196-49200-49171-49172-156-157-47-53,23-65281-10-11-35-16-5-34-18-51-43-13-45-28-27-65037,4588-29-23-24-25,0
```

## Interpretation

The measured BAFT outer TLS ClientHello is not fingerprint-equivalent to either measured browser control.

This is a detectability observation, not proof that any ISP or DPI system currently classifies BAFT from this fingerprint.

R-003 must keep the distinction:

- **fingerprint difference** = measured offline;
- **recognition/classification by an observer** = not established by the fingerprint alone;
- **blocking in Iran** = requires the longitudinal ISP experiment.

Record shaping does not alter the already-sent TLS ClientHello. Therefore T9 and T10 are expected to share the same outer ClientHello when all other TLS/client configuration is identical; the actual R-003 captures must still verify this rather than assume it.

## Certificate observations

Current offline browser-control certificate:
- local control fixture: self-signed, intentionally test-only.

R-003 external certificate cells remain to be measured from the dedicated servers:

| Tunnel | Certificate observation |
|---|---|
| T4 WSS direct IP | setup requires self-signed; actual leaf hash/subject/issuer must be captured |
| T5 WSS + CDN | pending actual CDN edge deployment; capture leaf/issuer/SAN |
| T7 Reality | pending actual reference deployment |
| T9 BAFT | pending dedicated R-003 server; capture ordinary-browser certificate |
| T10 BAFT shaped | pending dedicated R-003 server; must be same certificate policy as T9 for fair comparison |

Use `bench/r003/cert_observe.py` at day 0 and after any certificate rotation.

## Pending offline client fingerprints

T4/T5 WSS cannot be assigned one generic JA3/JA4 because the fingerprint belongs to the actual client TLS implementation, not to the label “WSS”.

T7 Reality must be measured from the pinned Xray-core version selected for the experiment.

Those cells stay **NOT MEASURED** until the exact implementations/versions in `FROZEN_MATRIX.md` are deployed and captured.
