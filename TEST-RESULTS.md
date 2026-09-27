# TEST RESULTS

## Environment
- Date: 2026-09-27
- Host: Linux amd64 sandbox
- Installed Go: `go1.23.2 linux/amd64`
- Source-pinned target Go: `go1.27.1`

## Pinned toolchain check
The source `go.mod` remains pinned to Go 1.27.1. The sandbox cannot fetch that toolchain because outbound network/DNS for the Go toolchain download is unavailable. Therefore no test below is claimed as the final Go-1.27.1 gate.

## Compatibility smoke procedure
A temporary copy of the repository was created and only its `go` directive was changed from `1.27.1` to `1.23.2`. Source files were otherwise identical.

Commands executed:

```text
GOTOOLCHAIN=local go test ./...
GOTOOLCHAIN=local go test -race ./...
GOTOOLCHAIN=local go vet ./...
```

Result: PASS after fixing one real race found by the first race-detector run.

### Race found and fixed
The first `go test -race ./...` detected a write to the HTTP/2 `ResponseWriter` from a Flow pump after the handler could return. The session now tracks Flow pump goroutines with a WaitGroup, closes flow sockets on session exit, and joins all pumps before returning from the carrier handler. The race test then passed.

## End-to-end vertical test
Topology:

```text
local TCP client
  → IR local TCP socket
  → BAFT frames
  → HTTP/2 over TLS 1.3 mTLS
  → EX route table
  → fixed TCP target
  → delayed echo after half-close
  → reverse BAFT direction
  → local TCP client
```

Payload: deterministic 262267 bytes.
SHA-256 expected and received:

```text
152ceb6d48a8f6028589ed554219e49a6ce29330bf8eb2c2c1adffa2c48523b3
```

Result: PASS in compatibility smoke environment.

## Security/correctness smoke coverage
- Invalid peer identity outside TLS allowlist: PASS (rejected).
- Expired client certificate: PASS (rejected).
- Server SAN / configured server-name mismatch: PASS (rejected).
- Untrusted server CA: PASS (rejected).
- H2 full-duplex response header flush before request-body completion: PASS.
- Cancellation reaches server handler: PASS.
- Four separately-owned shard transports produce four independent TCP connections: PASS.
- Unknown config field: PASS (rejected in strict JSON decoder).
- Unsafe baseline public service listener: PASS (rejected).
- `recovery.enabled=true` before 0.3: PASS (rejected).
- OPEN target-injection field: PASS (rejected).
- Duplicate OPEN JSON key: PASS (rejected).
- Duplicate DATA: PASS (no second payload returned for target write).
- ACK greater than `tx_next`: PASS (rejected).
- Old ACK: PASS (no backward state movement).
- WINDOW regression: PASS (rejected).
- Repeated OPEN with same stream/route/nonce: PASS (no second target dial).

## Not yet run / not yet implemented
- Pinned Go 1.27.1 gate.
- Full 1GiB COR-01 run.
- Active-peer revocation test SEC-03.
- Full RESET behavior and all standardized error mappings.
- Full fuzz campaign/soak test and dependency vulnerability scan.
- Resume/epoch race/replay tests.
- Benchmark 60s × 5 repeats.
- Iran↔EX real-path pilot.

## Parser fuzz smoke
Command executed in the Go 1.23.2 compatibility copy:

```text
GOTOOLCHAIN=local go test ./internal/protocol -run '^$' -fuzz '^FuzzDecode$' -fuzztime 2s
```

Result: PASS. The run executed 131603 fuzz cases in about 2 seconds and added 4 interesting inputs to the in-memory corpus. This is a smoke fuzz run, not the final fuzz campaign.
