# STATUS

Date: 2026-09-27

## Implemented
- Repository scaffold per BAFT architecture and reference documents.
- Minimal `cmd/baft` binary with `version` command.
- BAFT/1 frame encoder/decoder with bounded length, known-type validation, zero flags/reserved enforcement, stream-class validation, overflow checks and short-write-safe encoding.
- Reproducible protocol golden vectors.
- Typed configuration model plus strict JSON validator and JSON Schema; YAML examples are present, but the YAML loader dependency is not yet available in this sandbox.
- TLS 1.3 mTLS helpers with certificate-chain verification, hostname verification, a single URI SAN peer identity and an independent peer allowlist.
- HTTP/2 carrier spike with full-duplex streaming, header flush, cancellation, redirect prohibition and one independent transport per Shard.
- BAFT/1 new-session handshake: HELLO → HELLO_ACK → two-sided READY. Application frames before READY are rejected.
- Fixed allowlisted route table; OPEN never carries an arbitrary target address.
- Stage-B flow path: OPEN/OPEN_OK/OPEN_ERR, DATA, ACK, WINDOW, directional FIN/FIN_ACK and duplicate DATA suppression.
- One-route end-to-end integration path over real TCP on both sides through H2+mTLS.

## Tested in compatibility smoke environment
The source remains pinned to Go 1.27.1. Because the sandbox only has Go 1.23.2 and cannot download a new toolchain, tests were executed from a temporary copy whose `go` directive was changed only for the smoke run.

Executed successfully on Go 1.23.2 linux/amd64:
- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- 2-second parser fuzz smoke (`FuzzDecode`) with 131603 executions in this run.
- H2 full-duplex with mTLS.
- Four independent TCP/TLS connections for four separately-owned H2 transports.
- Peer outside allowlist rejected.
- Expired client certificate rejected.
- Server-name/SAN mismatch rejected.
- Untrusted server CA rejected.
- H2 cancellation observed by server.
- Config unknown-field and unsafe-baseline checks.
- OPEN target-injection and duplicate JSON key rejection.
- ACK beyond `tx_next` rejected; old ACK does not move state backwards.
- Duplicate DATA is not returned for a second socket write.
- WINDOW regression rejected.
- Duplicate OPEN with identical nonce does not redial target.
- End-to-end deterministic 262267-byte transfer with half-close and exact SHA-256 echo match.

## Current stage
- Stage A implementation: feature-complete for the intended spike, but final gate on pinned Go 1.27.1 is blocked by sandbox toolchain/network availability.
- Stage B implementation: substantial vertical slice is working; security revocation-on-active-carrier, 1GiB correctness run, complete error/reset behavior and production node CLI are not complete yet.
- Stage C+ remain incomplete.
