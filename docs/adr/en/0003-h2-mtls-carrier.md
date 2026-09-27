# ADR-0003 — Baseline HTTP/2 + mTLS Carrier

Status: accepted. Date: 2026-09-27.

The baseline uses Go standard TLS and `net/http` HTTP/2. TLS 1.3 and mTLS are mandatory; hostname verification remains active; client identity comes from a verified URI SAN and is separately allowlisted. Redirects, environment proxies, plaintext, and HTTP/1 fallback are disabled. `/baft/v1/carrier` is fixed but not secret. Each Shard owns an independent transport. Full duplex, flushing, and cancellation are part of the Carrier contract.
