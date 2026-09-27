# STATUS

Date: 2026-09-27

## Repository
- GitHub: `zarkmakerburg/baft`
- Branch: `main`
- Current Stage-B commit before this status update: `fc24c53a3ec0552793a60bd5ab2b7a30c71c0d86`
- CI run 36312312847: **PASS**

## Verified on pinned toolchain
GitHub Actions selected **Go 1.27.1 linux/amd64** from `go.mod` and passed:
- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- protocol fuzz smoke for `FuzzDecode`

## Implemented
- BAFT/1 bounded frame codec and golden vectors.
- Strict HELLO / HELLO_ACK / READY validation for new sessions.
- TLS 1.3 mTLS with certificate-chain, hostname and URI-SAN identity checks.
- Explicit peer allowlist independent from CA trust.
- Full-duplex HTTP/2 carrier with flush/cancellation behavior.
- One independently owned H2 transport per Shard.
- Fixed allowlisted route table; OPEN carries no arbitrary destination.
- OPEN/OPEN_OK/OPEN_ERR, DATA, ACK, WINDOW, FIN/FIN_ACK and baseline RESET handling.
- Duplicate DATA suppression and idempotent OPEN behavior.
- Real TCP → BAFT/H2+mTLS → fixed TCP target integration path with half-close and deterministic hash verification.
- H2 negative security tests for unauthorized peer, expired certificate, server-name mismatch and wrong server CA.

## Current stage
Stage A gate is complete for the implemented scope. Stage B has a working secure vertical slice, but is not complete.

Remaining Stage-B work includes:
- active certificate revocation for established carriers;
- complete RESET/error-code semantics;
- production IR/EX CLI path outside the integration harness;
- larger correctness run (including the planned 1 GiB test).

Stage C resource allocator / DRR / multi-flow resource policy has not started yet.


## YAML configuration gate
Strict YAML loading is implemented with `go.yaml.in/yaml/v3 v3.0.5`. Anchors/aliases, merge keys, duplicate mapping keys, custom tags, multiple documents, oversized input and unknown configuration fields are rejected. GitHub Actions run 36312919871 passed test/race/vet/fuzz with this dependency.
