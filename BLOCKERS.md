# BLOCKERS

## B-001 — pinned Go 1.27.1 toolchain cannot be executed in this sandbox
Impact: the final build/test gate with the source-pinned toolchain cannot yet be claimed.
Evidence: installed toolchain is Go 1.23.2; automatic Go toolchain download cannot reach the network. The official Go 1.27.1 Linux amd64 archive and checksum were verified from go.dev, but external archive retrieval into this sandbox also failed.
Independent work continued: all current packages were smoke-tested from a temporary compatibility copy on Go 1.23.2, including `go test`, race detector and `go vet`.

## B-002 — GitHub connector cannot create repositories
Impact: this chat can write files to an existing connected repository, but the available GitHub actions do not expose creation of a new repository. A second check on 2026-09-27 still shows no `zarkmakerburg/baft` repository.
Independent work continued: the complete local Git repository is maintained with commits and can be populated to GitHub immediately once an empty repository exists.

## B-003 — final YAML loader dependency unavailable
Impact: YAML examples and JSON Schema exist, but the production YAML decoder is not wired yet.
Reason: the project will not vendor an ad-hoc parser or silently add an unverified dependency. The chosen YAML library must be fetched, pinned, license-checked and tested with the target toolchain.
Independent work continued: the typed config model, strict JSON decoder, semantic validation and JSON Schema are implemented and tested.
