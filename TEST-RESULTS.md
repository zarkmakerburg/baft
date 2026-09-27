# TEST RESULTS

## Pinned CI environment
- Date: 2026-09-27
- GitHub Actions run: 36312312847
- Commit: `fc24c53a3ec0552793a60bd5ab2b7a30c71c0d86`
- Runner OS/arch: Linux amd64
- Go: **go1.27.1 linux/amd64**

The job log records `actions/setup-go@v7` selecting and installing 1.27.1, followed by:

```text
go version go1.27.1 linux/amd64
```

## Commands passed on Go 1.27.1

```text
go test ./...
go test -race ./...
go vet ./...
go test ./internal/protocol -run '^$' -fuzz '^FuzzDecode$' -fuzztime 10s
```

Result: **PASS**.

The normal test phase included:
- `cmd/baft`
- `internal/config`
- `internal/protocol`
- `internal/routes`
- `internal/session`
- `tests/integration`

The 10-second protocol fuzz smoke completed with PASS.

## Earlier compatibility smoke
Before GitHub CI was available, the same project was smoke-tested locally using Go 1.23.2 in a temporary compatibility copy. A real race was found in Flow-pump shutdown, fixed, and re-tested. Those results remain useful history but are superseded for the pinned-toolchain gate by the successful Go 1.27.1 GitHub run.

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

Expected/received SHA-256 in the compatibility run:

```text
152ceb6d48a8f6028589ed554219e49a6ce29330bf8eb2c2c1adffa2c48523b3
```

The same integration package also passed in the Go 1.27.1 CI run.

## Not yet run / not yet implemented
- Full 1 GiB correctness run.
- Active-peer revocation test.
- Full standardized RESET/error mapping tests.
- Long fuzz campaign and soak tests.
- Resume/epoch/replay tests.
- Benchmark 60 s × 5 repeats.
- Iran↔EX real-path pilot.
