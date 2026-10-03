# R-003 — Detectability Study Report

Status: NOT YET POPULATED WITH IRAN-SIDE LONGITUDINAL DATA

## Study metadata

- Study ID:
- Start UTC:
- End UTC:
- Foreign provider / ASN class:
- Foreign region:
- Iran city alias:
- ISP aliases:
- NTP/clock-sync evidence:
- Raw-data integrity hash bundle:

## Tunnel × ISP outcomes

| Tunnel | ISP | First disruption (h) | Full block (h) | Block type | Profile A bytes/day | Profile B delivered/attempted | IP-change follow-up |
|---|---|---:|---:|---|---:|---:|---|
| T1 | ISP-A | — | — | — | — | — | — |
| T1 | ISP-B | — | — | — | — | — | — |
| T1 | ISP-C | — | — | — | — | — | — |

Repeat rows for T2..T10.

## Active-probe summary

| Tunnel | Unique unsolicited sources (redacted) | ASN count | First probe after start (h) | HTTP/TLS fetch observed | BAFT cover fetched | Block followed probe |
|---|---:|---:|---:|---|---|---|
| T1 | — | — | — | — | n/a | — |
| T2 | — | — | — | — | n/a | — |
| T9 | — | — | — | — | — | — |
| T10 | — | — | — | — | — | — |

Shared report uses source counts/ASN aggregates only; raw source IPs remain private.

## Offline TLS fingerprint table

| Client / tunnel | Implementation + version | JA3 | JA4 | SNI | ALPN | Certificate shown to ordinary browser |
|---|---|---|---|---|---|---|
| Chrome control | — | — | — | — | — | test control |
| Firefox control | — | — | — | — | — | test control |
| T4 | — | — | — | — | — | expected self-signed; record observed |
| T5 | — | — | — | — | — | record CDN edge cert observed |
| T7 | — | — | — | — | — | record observed, do not infer |
| T9 BAFT | Go crypto/tls + H2 | — | — | — | h2 | record observed |
| T10 BAFT | Go crypto/tls + H2 | — | — | — | h2 | record observed |

## BAFT cover-page check

- Ordinary HTTPS GET `/` status:
- Body hash:
- Invalid method/path/content-type response:
- Valid outer TLS but invalid Noise handshake response:
- Unsolicited fetch count:
- First unsolicited fetch time:
- Any blocking transition after fetch:

## Notes on causality

Temporal sequence alone is not proof that a probe caused a later block. Report “probe preceded block by X hours” unless repeated controlled evidence supports a stronger statement.

## Redaction

No foreign test IP, Iran-side IP, credential, private key, auth token, secret domain/path, or raw unsolicited source IP is included here.
