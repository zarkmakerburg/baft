# dependency lock

## Go
- target: Go 1.27.1
- official release date: 2026-09-01
- official Linux amd64 archive: `go1.27.1.linux-amd64.tar.gz`
- official SHA-256: `63d339f0da5ab53635a56f2490a7984dfe12dfcff22ad749f63edaf590168445`
- verification source: `go.dev/doc/devel/release` and `go.dev/dl/`, checked 2026-09-27
- local sandbox installed toolchain: Go 1.23.2
- target toolchain download: blocked in this environment; direct shell network is unavailable and external archive retrieval failed.

## External dependencies
None in the current core tree. Baseline TLS and HTTP/2 spike use the Go standard library.

Planned YAML decoder dependency is intentionally not added until it can be fetched, pinned, license-checked and tested with the target toolchain. The current typed model, strict JSON decoder and JSON Schema are sufficient for the Stage-A contract but are not claimed as the final YAML loader.

## GitHub Actions
- `actions/checkout@v7`
- `actions/setup-go@v7`
- versions checked against the upstream action repositories on 2026-09-27.


## YAML
- module: `go.yaml.in/yaml/v3`
- version: `v3.0.5`
- source/repository: YAML organization maintained `yaml/go-yaml`
- release/package metadata checked 2026-09-27 via pkg.go.dev.
- license reported by pkg.go.dev: Apache-2.0 and MIT.
- module checksum: `h1:N6y/pJk8buWs9NY5ERU2HSMfm+IuD/OtfdAnq6kESPw=`
- go.mod checksum: `h1:HVTZu1O7/Vkt2N+BFy8Zza+lnLsABggaTM2ZpNIGuKg=`
- checksums were generated and verified by Go 1.27.1 in GitHub Actions run 36312919871.
