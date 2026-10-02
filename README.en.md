<p align="center"><img src="docs/assets/baft-logo.png" alt="BAFT" width="220"></p>

> © 2026 BAFT Project. All rights reserved. This repository is public for review only; no license is granted. See [COPYRIGHT](COPYRIGHT).

# BAFT

> **Language:** [فارسی](README.md) | English

[![ci](https://github.com/zarkmakerburg/baft/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/zarkmakerburg/baft/actions/workflows/ci.yml)
[![stagec-soak](https://github.com/zarkmakerburg/baft/actions/workflows/stagec-soak.yml/badge.svg?branch=main)](https://github.com/zarkmakerburg/baft/actions/workflows/stagec-soak.yml)
[![vulncheck](https://github.com/zarkmakerburg/baft/actions/workflows/vulncheck.yml/badge.svg)](https://github.com/zarkmakerburg/baft/actions/workflows/vulncheck.yml)

BAFT relays authenticated TCP byte streams between two operator-controlled nodes over a multiplexed, flow-controlled Carrier (HTTP/2 over TLS 1.3). A local service on the **IR** node is mapped to one fixed, pre-authorized service behind the **EX** node; the remote peer never supplies a destination. A control plane (**BCC**) enrolls servers, builds tunnels on both ends, checks them and rolls both back on failure. Releases are signed and verified before anything is installed.

**Status: research software, not declared production-ready.** See [Status](#status) and [Limitations](#limitations).

## Contents

[Status](#status) · [Architecture](#architecture) · [Components](#components) · [Security properties](#security-properties) · [Install](#install) · [Operating with BCC](#operating-with-bcc) · [Build and test](#build-and-test) · [Verification in CI](#verification-in-ci) · [Repository layout](#repository-layout) · [Documentation](#documentation) · [Limitations](#limitations) · [License](#license)

## Status

| Area | State |
|---|---|
| Data plane, Stage A–B | Complete for the defined scope: protocol contracts, parser, strict configuration, HTTP/2 + TLS 1.3 Carrier, authenticated peers, fixed Routes, Flow semantics, active revocation, 1 GiB bidirectional correctness gate (COR-01). |
| Data plane, Stage C | Allocator, backpressure, byte-based scheduling (PADL) and the TWRL receive ledger are in the data path; the repeated multi-Flow / slow-receiver soak and its race-detector sample pass. This is a correctness result on CI/loopback networking, not a public-network benchmark. |
| Data plane, Stage D | Same-process carrier replacement with epoch fencing and bounded replay is implemented and tested. Process-restart and machine-reboot resume are **not** implemented. |
| Launch-1 control plane | Delivered on `main`: signed releases, release-verifying installer and `baft status/doctor/logs`, BCC access and SQLite state, signed agent jobs, SSH bootstrap, native tunnel builder with rollback, end-to-end test on systemd. See [22](docs/en/22-launch-1-roadmap.md). |
| Latest release | [`v0.1.0`](https://github.com/zarkmakerburg/baft/releases/tag/v0.1.0): signed; root key id `98741d81da746d2e435302f90569911d`. |

Evidence is tracked in [STATUS.en.md](STATUS.en.md), [TEST-RESULTS.en.md](TEST-RESULTS.en.md), [PLAN.en.md](PLAN.en.md) and [KNOWN-LIMITATIONS.en.md](KNOWN-LIMITATIONS.en.md). Claims in this repository must come from real executions.

## Architecture

```text
local application
      │
      ▼
127.0.0.1:1443 on IR                    one Route = one fixed target
      │                                 OPEN carries a route_id, never a host
      ▼
BAFT IR (dialer)
      │   Shard 0..N: independent TCP + TLS 1.3 + HTTP/2 Carriers
      ▼
BAFT EX (listener)
      │
      ▼
fixed authorized target, e.g. 127.0.0.1:2443
```

Inside a Carrier: `HELLO → HELLO_ACK → READY`, then per Flow `OPEN / OPEN_OK`, `DATA`, `ACK`, `WINDOW` (credit), `FIN / FIN_ACK`, `RESET`. Each Shard owns its own transport; Shards are never silently pooled onto one TCP connection. Memory is bounded by a global allocator with backpressure, and DATA is scheduled by byte-based deficit round robin with pressure aging.

Two Carrier authentication modes exist, both over TLS 1.3:

- **mTLS** (default when the configuration has no `noise` section): certificate chain, hostname, validity and EKU are verified; node identity comes from a verified URI SAN.
- **Pinned Noise IK** (what `install.sh` pairing provisions): the outer TLS server is validated against a local CA, and both peers authenticate with pinned Noise static keys, each mapped to one allowed identity ([ADR-0007](docs/adr/0007-pinned-noise-morphing.md)).

Control plane:

```text
 operator ──► BCC (secret-path web UI + API, SQLite state, audit log)
                │  signed jobs (Ed25519), pulled over HTTPS
                ▼
         baft-agent on each server ──► baft, baft-pair, systemd
```

## Components

| Binary | Role |
|---|---|
| `baft` | Data-plane node (`run`), `config validate`, and operations commands `status`, `doctor`, `logs`. |
| `baft-pair` | Key generation, outer-TLS PKI, and the one-time pairing that writes both configs (`ex-code`, `ir-apply`, `ex-accept`). |
| `baft-bcc` | Control plane: monitoring, finance, hash-chained audit, encrypted backup, token rotation and revocation, signed jobs, SSH bootstrap, tunnel builder, web access (`access` and `jobkey` subcommands). |
| `baft-agent` | Runs on each server; pulls signed jobs, executes only allowlisted actions, updates only to release-signed binaries. |
| `baft-release` | Release signing and verification: `keygen`, `certify`, `revoke`, `sign`, `verify`. |
| `baft-bcc-audit-verify` | Offline verification of the BCC audit chain and anchors. |
| `baft-master`, `baft-worker`, `baft-cluster-keygen` | Earlier cluster/mesh runtime and its key tooling. |

## Security properties

Data plane

- TLS 1.3 minimum, no plaintext fallback; certificate chain, hostname, validity and EKU checks cannot be disabled.
- CA trust and Route authorization are separate; HELLO `node_id` alone never establishes trust.
- The peer cannot supply a target; frame sizes and types are validated before allocation.
- User payload is never written to logs or support bundles.

Release trust ([23](docs/en/23-p1a-signed-releases.md))

- DSSE-style envelopes. An **offline Ed25519 root key** certifies the CI **release key**; the root public key is pinned inside `install.sh` and the agent. A mandatory, expiring, root-signed **revocation list** is checked on every install.
- The installer verifies certificate, revocation list, manifest signature, `SHA256SUMS` and each binary hash before running anything downloaded, refuses downgrades and re-tagged versions, and records state in a root-owned file.
- Release builds are reproducible (checked in CI).

Control plane ([24](docs/en/24-p1c-bcc-access.md), [25](docs/en/25-p1d-agent.md), [26](docs/en/26-p1d-ssh-bootstrap.md), [27](docs/en/27-p1e-tunnel-builder.md))

- BCC web access uses a random secret path, random username and password (stored as a PBKDF2 hash), sessions with CSRF protection, rate limiting and console-only regeneration that revokes every session.
- Every job is signed by BCC and limited to an allowlist with strict per-parameter patterns, checked again by the agent; there is no shell or free-form command action. Jobs expire (1 hour) and are never run twice.
- SSH bootstrap pins the server's host key (no trust on first use); SSH credentials exist only in memory for the request and are never stored, logged or audited.
- Tunnel changes are transactional: prepare stages, commit keeps a backup, any failure rolls every touched node back. Pairing secrets pass through jobs only while a step needs them and are wiped afterwards.
- BCC state is a versioned SQLite database; admin actions go to a tamper-evident audit log.

Read the [security model](docs/en/03-security-model.md).

## Install

Requirements on a server: Linux with systemd, `curl`, `openssl`, `python3`, and a user with `sudo`. No Go, git or compiler is needed: the installer downloads the signed release for the machine's architecture (`amd64`, `arm64`), verifies it against the pinned root key and the current revocation list, and installs nothing if verification fails.

```bash
# EX (outside server): prints a one-time pairing code
sudo bash -c "$(curl -fsSL https://raw.githubusercontent.com/zarkmakerburg/baft/main/install.sh)" baft --role ex --public-address EX_IP_OR_HOST

# IR (inside server): paste the pairing code, then give the printed reply code back to the EX
sudo bash -c "$(curl -fsSL https://raw.githubusercontent.com/zarkmakerburg/baft/main/install.sh)" baft --role ir
```

To enroll a server for BCC instead, install the agent only (BCC then builds tunnels itself):

```bash
sudo env BAFT_BCC_JOB_KEY="$(baft-bcc jobkey show)" BAFT_AGENT_TOKEN=NODE_TOKEN \
  bash -c "$(curl -fsSL https://raw.githubusercontent.com/zarkmakerburg/baft/main/install.sh)" baft \
  --agent-only --bcc-url https://bcc.example.com --node-id ex-1
```

`BAFT_BCC_JOB_KEY` is the value printed by `baft-bcc jobkey show` on the BCC host. Add `--version vX.Y.Z` to pin a release. The script is run through `bash -c`, not `| bash`, so the installer can still read the codes from the terminal.

To check a release yourself, download its assets and run:

```bash
baft-release verify -dir <download dir> -root-pub release/keys/root.pub -revocations release/keys/revocations.json
```

Details and environment variables: [06](docs/en/06-running-ir-ex.md) and `bash install.sh --help`.

## Operating with BCC

1. Run `baft-bcc` (HTTPS, or loopback) and create web access with `baft-bcc access init`.
2. Add servers in the dashboard (**Add a server over SSH**: read host key, confirm the fingerprint, install the agent) or enroll them with the agent-only command above.
3. Build a tunnel in the dashboard (**Tunnels**) or with `POST /api/tunnels {"ex_node":"…","ir_node":"…"}`. BCC prepares both ends, commits, health-checks and finalizes; on any failure, or on `POST /api/tunnels/cancel`, both ends return to their previous state.
4. Follow phases in the dashboard and outcomes in the audit log.

Inspect a node locally with `baft status`, `baft doctor` and `baft logs`.

## Build and test

```bash
git clone https://github.com/zarkmakerburg/baft.git
cd baft
go build ./...
go vet ./...
go test ./...
go test -race ./...
```

The toolchain is the Go version in `go.mod` (1.27.1). Validate and run a node by hand:

```bash
./baft config validate --file configs/example-ir.yaml
./baft run --file /etc/baft/ir.yaml     # or ex.yaml on the EX
```

## Verification in CI

| Workflow / job | What it proves |
|---|---|
| `ci` · `test` | Unit and integration tests, race detector, and the staged evidence gates (stability, sync integrity, mesh, telemetry, BCC hardening, backup/restore, ECRL runtime). |
| `ci` · `e2e-binaries`, `e2e-install` | Real traffic through IR→EX built from source and installed with `install.sh` on systemd. |
| `ci` · `e2e-install-release`, `release-dry-run` | Install from a signed release built with throwaway keys; reproducible builds; tamper, revocation, replay, downgrade and re-tag are rejected. |
| `ci` · `e2e-agent-enroll`, `e2e-ssh-bootstrap` | Agent enrollment and a signed job on systemd; SSH bootstrap against a real `sshd`, including a wrong host key. |
| `ci` · `e2e-launch1` | Launch-1 on one commit: two servers enrolled, tunnel built by BCC with real traffic, a failing change rolled back on both sides, no secret left behind. |
| `stagec-soak`, `step57-recovery-soak` | Repeated multi-Flow, slow-receiver and recovery soaks. |
| `vulncheck` | `govulncheck` weekly and on dependency changes. |

## Repository layout

| Path | Contents |
|---|---|
| `cmd/` | Binaries listed under [Components](#components). |
| `internal/protocol`, `session`, `scheduler`, `resources`, `routes`, `identity`, `config`, `carrier/h2`, `recovery`, `recordshape`, `securityinternal` | Data plane: BAFT/1 codec, Sessions and Flows, scheduling, allocator, Routes, identities, configuration, Carrier, recovery, record shaping, pairing and Noise. |
| `internal/bcc`, `agent`, `agentjob`, `sshboot`, `tunnelnode`, `release`, `telemetry` | Control plane: BCC, agent, job format, SSH bootstrap, node-side tunnel transactions, release signing, telemetry. |
| `internal/cluster`, `clustersync`, `mesh`, `failover`, `node`, `metrics` | Cluster/mesh runtime, failover and metrics. |
| `install.sh`, `scripts/release/` | Installer; reproducible release build and rehearsals. |
| `release/keys/` | Pinned root public key and the current signed revocation list. |
| `tests/` | `integration`, `correctness` (large gates), `installer`, `e2e`, `docs`. |
| `docs/en`, `docs/fa`, `docs/adr` | Mirrored guides and architecture decision records. |

Dependency boundaries are enforced by design: `protocol` does not dial targets, `routes` does not parse wire payloads, `scheduler` does not interpret payloads, `carrier` does not decide Route authorization. See [10](docs/en/10-repository-layout.md).

## Documentation

Architecture and protocol: [01 Overview](docs/en/01-overview.md) · [02 Architecture](docs/en/02-architecture.md) · [03 Security model](docs/en/03-security-model.md) · [04 BAFT/1 protocol](docs/en/04-protocol-baft1.md) · [05 Configuration](docs/en/05-configuration.md) · [11 Glossary](docs/en/11-glossary.md)

Operation and quality: [06 Running IR/EX](docs/en/06-running-ir-ex.md) · [07 Resource control](docs/en/07-resource-control.md) · [08 Testing and CI](docs/en/08-testing-and-ci.md) · [17 Correctness gates](docs/en/17-correctness-gates.md) · [18 Stage-C soak](docs/en/18-stage-c-soak.md) · [10 Repository layout](docs/en/10-repository-layout.md)

Launch-1 control plane: [22 Roadmap](docs/en/22-launch-1-roadmap.md) · [23 Signed releases](docs/en/23-p1a-signed-releases.md) · [24 BCC access and state](docs/en/24-p1c-bcc-access.md) · [25 Agent jobs](docs/en/25-p1d-agent.md) · [26 SSH bootstrap](docs/en/26-p1d-ssh-bootstrap.md) · [27 Tunnel builder](docs/en/27-p1e-tunnel-builder.md)

Research and design records: [09 Roadmap, Stages A–H](docs/en/09-roadmap.md) · [12 Innovation methodology](docs/en/12-innovation-method.md) · [13 TWRL](docs/en/13-stage-c-twrl.md) · [14 PADL](docs/en/14-stage-c-padl.md) · [15 Conservation telemetry](docs/en/15-conservation-telemetry.md) · [15D ECRL design gate](docs/en/15-stage-d-ecrl.md) · [16 Conservation metrics](docs/en/16-conservation-metrics.md) · [19 Terminal quarantine](docs/en/19-terminal-quarantine.md) · [21 Record shaping / morphing (experimental)](docs/en/21-stealth-pro.md) · [ADRs](docs/adr/)

Status files: [STATUS](STATUS.en.md) · [PLAN](PLAN.en.md) · [BLOCKERS](BLOCKERS.en.md) · [TEST-RESULTS](TEST-RESULTS.en.md) · [KNOWN-LIMITATIONS](KNOWN-LIMITATIONS.en.md) · [dependency lock](dependency-lock.en.md) · [novelty matrix](novelty-matrix.en.md)

## Limitations

- Not production-ready. No real IR↔EX pilot has been run; no universal throughput, undetectability or guaranteed-connectivity claim is made.
- Resume after process restart or machine reboot is not implemented; recovery is same-process only.
- The IR public listener does not authenticate end users; if it is exposed beyond loopback, add service-layer security.
- Record shaping / morphing is experimental; statistical similarity to HTTPS is not established.
- H3, relay and Worker paths are outside the default core path; BCC does not measure traffic quality beyond service liveness.
- BAFT is not a general-purpose VPN or an arbitrary-destination proxy.

Full list: [KNOWN-LIMITATIONS.en.md](KNOWN-LIMITATIONS.en.md).

## License

All rights reserved by BAFT Project. The repository is publicly visible for review only; no license to use, copy, modify, distribute or run the software is granted. See [COPYRIGHT](COPYRIGHT).
