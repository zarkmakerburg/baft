# ADR-0002 — Go toolchain

Status: accepted. Date: 2026-09-27.

The repository targets Go 1.27.1 through `go.mod`. GitHub Actions installs and tests with that pinned toolchain. Module metadata is kept reproducible by running `go mod tidy` and failing CI when `go.mod` or `go.sum` changes unexpectedly. See [dependency-lock.en.md](../../../dependency-lock.en.md).
