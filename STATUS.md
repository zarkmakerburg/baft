# STATUS

Date: 2026-09-27

## Repository
- GitHub: `zarkmakerburg/baft`
- Branch: `main`
- Stage B evidence commit: `54f539cebfd919e6bac28560bbe8b0b0cc97c94e`
- COR-01 workflow run: `36316645627` — **PASS**
- Production CLI CI run: `36316481799` — **PASS**
- Active revocation CI run: `36316283267` — **PASS**

## Verified toolchain and gates
GitHub Actions uses **Go 1.27.1 linux/amd64** from `go.mod`.

The standard CI gate passes:
- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- module lock/tidy verification
- protocol fuzz smoke

## Stage A
Complete for the implemented contract/spike scope: protocol codec/golden vectors, strict configuration contract, test PKI, H2+mTLS full-duplex, cancellation and four independently-owned Shard transports are implemented and tested.

## Stage B
**Complete for the secure vertical-slice scope defined by the implementation plan.**

Implemented and tested:
- HELLO / HELLO_ACK / two-sided READY for new sessions.
- fixed allowlisted routes; OPEN cannot inject arbitrary destinations.
- OPEN/OPEN_OK/OPEN_ERR, DATA, ACK, WINDOW, FIN/FIN_ACK and fixed-code RESET.
- duplicate DATA suppression, stale/invalid ACK/WINDOW rejection and idempotent OPEN.
- TLS 1.3 mTLS with chain, hostname, EKU and URI-SAN peer identity checks.
- independent peer/route allowlists.
- runtime active revocation by peer identity, certificate serial or SHA-256 fingerprint; existing carrier is terminated.
- strict YAML loader with pinned `go.yaml.in/yaml/v3 v3.0.5` checksums.
- production `baft config validate --file ...` and `baft run --file ...` path for listener/dialer roles.
- real TCP → BAFT/H2+mTLS → fixed TCP target vertical transfer.
- COR-01 streaming 1 GiB each direction with exact SHA-256 equality.

## COR-01
GitHub Actions run `36316645627` transferred **1,073,741,824 bytes in each direction**.

SHA-256:
`1efd9d3aab21f9e312a2a0b5a6886b2a640c810ecb1fbe33f64614b26cfb27e3`

The test completed in **10.72 s** on the GitHub runner. This is a correctness measurement on loopback CI infrastructure, not an Internet throughput claim.

## Stage C
Started. Standalone resource primitives now exist:
- 256 MiB total data allocator;
- independent 128 MiB receive and replay pools with no borrowing;
- 16 MiB default per-flow caps;
- bounded control queue: 256 messages or 1 MiB;
- byte-based DRR primitive.

These primitives are not yet fully connected to the Session send/receive path, so Stage C remains incomplete.
