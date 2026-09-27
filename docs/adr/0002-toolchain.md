# ADR-0002: Go toolchain

Status: accepted
Date: 2026-09-27

Pin the source tree to Go 1.27.1. Official Go release history lists Go 1.27.1 as released on 2026-09-01; the official Linux amd64 archive SHA-256 is recorded in `dependency-lock.md`.

The current sandbox has Go 1.23.2 and cannot retrieve the official 1.27.1 archive. Compatibility smoke, race and vet checks are therefore executed from a temporary copy with only the `go` directive lowered to 1.23.2. Those results are useful engineering evidence but do not replace the final pinned-toolchain gate.
