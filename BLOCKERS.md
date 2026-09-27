# BLOCKERS

## Resolved — GitHub repository creation
`zarkmakerburg/baft` exists and the connected GitHub app has admin/push access. Stage A/B source is on `main`.

## Resolved — pinned Go 1.27.1 verification
GitHub Actions run 36312312847 completed successfully on 2026-09-27. `actions/setup-go@v7` selected Go 1.27.1 and the logs show:

```text
go version go1.27.1 linux/amd64
```

The run passed unit/integration tests, the race detector, `go vet` and the protocol fuzz smoke. The local sandbox still has Go 1.23.2, but it is no longer a blocker for the pinned-toolchain CI gate.

## Resolved — strict YAML loader dependency
The maintained `go.yaml.in/yaml/v3` module is pinned at v3.0.5. GitHub Actions run 36312919871 generated the module checksums and passed unit/integration tests, race detector, vet and fuzz smoke. The committed `go.sum` is now the reproducible dependency lock.
