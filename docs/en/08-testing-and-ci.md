# 08 — Testing, CI, and acceptance gates

BAFT treats implementation and evidence as separate things. A capability is not recorded as verified until its actual command/run evidence exists.

The standard CI gate runs module-lock verification, `go test ./...`, the race detector, `go vet ./...`, and a protocol fuzz smoke on the pinned Go toolchain.

## Test categories

- unit: parser, config, identity, routes, allocator, scheduler, Flow state;
- integration: real TCP + BAFT Session + H2/mTLS + target TCP;
- fuzz: parser/state boundaries;
- correctness: deterministic byte count and end-to-end hashes;
- benchmark: formal repeated performance work, not yet complete.

## COR-01

COR-01 streams 1 GiB in each direction and compares SHA-256. It is a correctness gate on local/runner networking, not a public-network throughput claim. The gate has also passed after allocator/DRR integration.

## Security tests

Coverage includes unauthorized peers, expired certificates, hostname mismatch, wrong CA, active revocation, Route denial, duplicate keys, invalid error codes, and malformed frame behavior.

## Current Stage-C gate

The slow-receiver integration test checks bounded memory, source backpressure, resumed progress after drain, and clean cancellation. At the time of this documentation revision that gate is still being stabilized, so Stage C remains incomplete.
