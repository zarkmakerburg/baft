# Dependency lock

> Persian: [dependency-lock.md](dependency-lock.md)

## Go
- target: Go 1.27.1
- official Linux amd64 archive: `go1.27.1.linux-amd64.tar.gz`
- recorded SHA-256: `63d339f0da5ab53635a56f2490a7984dfe12dfcff22ad749f63edaf590168445`
- CI reads the version from `go.mod` using `actions/setup-go@v7`.

## YAML
- module: `go.yaml.in/yaml/v3`
- version: `v3.0.5`
- upstream: `yaml/go-yaml`
- reported licenses: Apache-2.0 and MIT
- module checksum: `h1:N6y/pJk8buWs9NY5ERU2HSMfm+IuD/OtfdAnq6kESPw=`
- go.mod checksum: `h1:HVTZu1O7/Vkt2N+BFy8Zza+lnLsABggaTM2ZpNIGuKg=`

Checksums were generated with Go 1.27.1 in GitHub Actions and committed in `go.sum`.

New dependencies require a pinned version, license review, checksum/lock, and tests on the target toolchain.
