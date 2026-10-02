<p align="center"><img src="docs/assets/baft-logo.png" alt="BAFT" width="220"></p>

> © 2026 BAFT Project. All rights reserved. This repository is public for review only; no license is granted. See [COPYRIGHT](COPYRIGHT).

# BAFT

> **Language:** [فارسی](README.md) | English

[![ci](https://github.com/zarkmakerburg/baft/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/zarkmakerburg/baft/actions/workflows/ci.yml)
[![stagec-soak](https://github.com/zarkmakerburg/baft/actions/workflows/stagec-soak.yml/badge.svg?branch=main)](https://github.com/zarkmakerburg/baft/actions/workflows/stagec-soak.yml)
[![vulncheck](https://github.com/zarkmakerburg/baft/actions/workflows/vulncheck.yml/badge.svg)](https://github.com/zarkmakerburg/baft/actions/workflows/vulncheck.yml)

BAFT relays authenticated TCP byte streams between two operator-controlled nodes over a multiplexed, flow-controlled Carrier (HTTP/2 over TLS 1.3). A local service on the **IR** node maps to one fixed, pre-authorized target behind the **EX** node; the remote peer never supplies a destination. The **BCC** control plane enrolls servers, builds tunnels on both ends, health-checks them, and rolls both back on failure. Releases are signed and verified before anything is installed.

**Research software; not declared production-ready.**

## Status

| Area | State |
|---|---|
| Data plane A–B | Complete for the defined scope: protocol, strict configuration, Carrier, authenticated peers, fixed Routes, Flows, active revocation, 1 GiB bidirectional correctness gate (COR-01). |
| Data plane C | Allocator, backpressure, byte-based scheduling (PADL) and receive ledger (TWRL) in the data path; repeated multi-Flow / slow-receiver soak and race sample pass on CI/loopback networking. Not a public-network benchmark. |
| Data plane D | Same-process carrier replacement with epoch fencing and bounded replay. Restart and reboot resume are **not** implemented. |
| Launch-1 control plane | On `main`: signed releases, verifying installer, BCC access and SQLite state, signed agent jobs, SSH bootstrap, tunnel builder with rollback, end-to-end test on systemd ([22](docs/en/22-launch-1-roadmap.md)). |
| Release | [`v0.1.0`](https://github.com/zarkmakerburg/baft/releases/tag/v0.1.0), signed; root key id `98741d81da746d2e435302f90569911d`. |

Evidence: [STATUS](STATUS.en.md) · [TEST-RESULTS](TEST-RESULTS.en.md) · [KNOWN-LIMITATIONS](KNOWN-LIMITATIONS.en.md).

## Architecture

```text
application ─► 127.0.0.1:1443 (IR) ─► BAFT IR ══ Shard 0..N: TCP + TLS 1.3 + HTTP/2 ══► BAFT EX ─► fixed target (EX)
                                      OPEN carries a route_id, never a host

operator ─► BCC (web UI + API, SQLite, audit) ── signed jobs, pulled over HTTPS ──► baft-agent ─► baft · baft-pair · systemd
```

Per Carrier: `HELLO → HELLO_ACK → READY`; per Flow: `OPEN/OPEN_OK`, `DATA`, `ACK`, `WINDOW`, `FIN/FIN_ACK`, `RESET`. Each Shard has its own transport. Memory is bounded by a global allocator with backpressure.

| Carrier authentication | When | Mechanism |
|---|---|---|
| mTLS | default (no `noise` section) | Certificate chain, hostname, validity, EKU; identity from a verified URI SAN. |
| Pinned Noise IK | provisioned by `install.sh` pairing | Outer TLS validated against a local CA; peers authenticate with pinned Noise static keys, each mapped to one identity ([ADR-0007](docs/adr/0007-pinned-noise-morphing.md)). |

## Components

| Binary | Role |
|---|---|
| `baft` | Data-plane node (`run`), `config validate`, `status`, `doctor`, `logs`. |
| `baft-pair` | Keys, outer-TLS PKI, one-time pairing that writes both configs. |
| `baft-bcc` | Control plane: monitoring, finance, audit, backup, signed jobs, SSH bootstrap, tunnel builder, web access. |
| `baft-agent` | Per-server agent: signed, allowlisted jobs; updates only to release-signed binaries. |
| `baft-release` | `keygen`, `certify`, `revoke`, `sign`, `verify`. |
| `baft-bcc-audit-verify` | Offline verification of the audit chain and anchors. |
| `baft-master`, `baft-worker`, `baft-cluster-keygen` | Earlier cluster/mesh runtime and key tooling. |

## Security

| Property | Mechanism | Reference |
|---|---|---|
| No plaintext, no unverified peer | TLS 1.3 minimum; chain, hostname, validity, EKU checks cannot be disabled; HELLO `node_id` alone is never trusted. | [03](docs/en/03-security-model.md) |
| No peer-chosen destination | Routes map a `route_id` to a fixed target; frames are validated before allocation. | [04](docs/en/04-protocol-baft1.md) |
| Authentic releases | Offline Ed25519 root certifies the CI release key; root public key pinned in `install.sh` and the agent; mandatory expiring revocation list; manifest, `SHA256SUMS` and per-binary hashes verified before use; no downgrade or re-tag; reproducible builds. | [23](docs/en/23-p1a-signed-releases.md) |
| Protected console | Random secret path, username and password (PBKDF2 hash only), CSRF-protected sessions, rate limiting, console-only regeneration that revokes all sessions. | [24](docs/en/24-p1c-bcc-access.md) |
| Constrained agent | Jobs signed by BCC, allowlisted actions with strict per-parameter patterns, 1-hour expiry, never run twice; no shell or free-form command. | [25](docs/en/25-p1d-agent.md) |
| Safe bootstrap | SSH host key pinned (no trust on first use); credentials kept in memory only, never stored, logged or audited. | [26](docs/en/26-p1d-ssh-bootstrap.md) |
| Reversible tunnels | Prepare stages only, commit keeps a backup, any failure rolls every touched node back; pairing secrets wiped after use. | [27](docs/en/27-p1e-tunnel-builder.md) |
| Auditability | Hash-chained admin audit log; versioned SQLite state; payload never logged. | [24](docs/en/24-p1c-bcc-access.md) |

## Install

Server requirements: Linux with systemd, `curl`, `openssl`, `python3`, a `sudo` user. The installer downloads the signed release for the architecture (`amd64`, `arm64`), verifies it against the pinned root key and revocation list, and installs nothing if verification fails.

```bash
# EX: prints a one-time pairing code
sudo bash -c "$(curl -fsSL https://raw.githubusercontent.com/zarkmakerburg/baft/main/install.sh)" baft --role ex --public-address EX_IP_OR_HOST

# IR: paste the pairing code, then give the printed reply code back to the EX
sudo bash -c "$(curl -fsSL https://raw.githubusercontent.com/zarkmakerburg/baft/main/install.sh)" baft --role ir

# Server managed by BCC (agent only)
sudo env BAFT_BCC_JOB_KEY="$(baft-bcc jobkey show)" BAFT_AGENT_TOKEN=NODE_TOKEN \
  bash -c "$(curl -fsSL https://raw.githubusercontent.com/zarkmakerburg/baft/main/install.sh)" baft \
  --agent-only --bcc-url https://bcc.example.com --node-id ex-1
```

`BAFT_BCC_JOB_KEY` comes from `baft-bcc jobkey show` on the BCC host. Add `--version vX.Y.Z` to pin a release. `bash -c`, not `| bash`, keeps the terminal available for the codes. Verify a release by hand:

```bash
baft-release verify -dir <download dir> -root-pub release/keys/root.pub -revocations release/keys/revocations.json
```

## Operate with BCC

| Step | Action |
|---|---|
| 1 | Start `baft-bcc` (HTTPS or loopback); create web access with `baft-bcc access init`. |
| 2 | Add servers: dashboard **Add a server over SSH** (read host key, confirm fingerprint, install agent), or the agent-only command above. |
| 3 | Build a tunnel: dashboard **Tunnels**, or `POST /api/tunnels {"ex_node":"…","ir_node":"…"}`. |
| 4 | BCC prepares, commits, health-checks and finalizes both ends; any failure or `POST /api/tunnels/cancel` restores both. |

Local inspection: `baft status`, `baft doctor`, `baft logs`.

## Build and test

```bash
go build ./... && go vet ./... && go test ./... && go test -race ./...
```

Toolchain: Go 1.27.1 (`go.mod`).

## What CI proves

| Job | Evidence |
|---|---|
| `test` | Unit, integration, race detector, staged evidence gates. |
| `e2e-binaries`, `e2e-install` | Real IR→EX traffic, built from source and installed on systemd. |
| `e2e-install-release`, `release-dry-run` | Install from a signed release; reproducible builds; tamper, revocation, replay, downgrade, re-tag rejected. |
| `e2e-agent-enroll`, `e2e-ssh-bootstrap` | Agent enrollment and signed job; SSH bootstrap against a real `sshd`, wrong host key refused. |
| `e2e-launch1` | Two servers enrolled, tunnel built by BCC with real traffic, failing change rolled back on both sides, no secret left. |
| `stagec-soak`, `step57-recovery-soak` | Repeated multi-Flow, slow-receiver and recovery soaks. |
| `vulncheck` | `govulncheck`, weekly and on dependency changes. |

## Documentation

| Topic | Documents |
|---|---|
| Design | [01 Overview](docs/en/01-overview.md) · [02 Architecture](docs/en/02-architecture.md) · [03 Security model](docs/en/03-security-model.md) · [04 Protocol](docs/en/04-protocol-baft1.md) · [05 Configuration](docs/en/05-configuration.md) · [11 Glossary](docs/en/11-glossary.md) · [ADRs](docs/adr/) |
| Operation | [06 Running IR/EX](docs/en/06-running-ir-ex.md) · [07 Resource control](docs/en/07-resource-control.md) · [08 Testing and CI](docs/en/08-testing-and-ci.md) · [10 Repository layout](docs/en/10-repository-layout.md) |
| Launch-1 | [22 Roadmap](docs/en/22-launch-1-roadmap.md) · [23 Releases](docs/en/23-p1a-signed-releases.md) · [24 BCC access](docs/en/24-p1c-bcc-access.md) · [25 Agent](docs/en/25-p1d-agent.md) · [26 SSH bootstrap](docs/en/26-p1d-ssh-bootstrap.md) · [27 Tunnel builder](docs/en/27-p1e-tunnel-builder.md) |
| Research | [09 Roadmap A–H](docs/en/09-roadmap.md) · [12 Method](docs/en/12-innovation-method.md) · [13 TWRL](docs/en/13-stage-c-twrl.md) · [14 PADL](docs/en/14-stage-c-padl.md) · [15 Telemetry](docs/en/15-conservation-telemetry.md) · [15D ECRL](docs/en/15-stage-d-ecrl.md) · [17 Gates](docs/en/17-correctness-gates.md) · [18 Soak](docs/en/18-stage-c-soak.md) · [21 Morphing](docs/en/21-stealth-pro.md) |
| Records | [STATUS](STATUS.en.md) · [PLAN](PLAN.en.md) · [BLOCKERS](BLOCKERS.en.md) · [dependency lock](dependency-lock.en.md) · [novelty matrix](novelty-matrix.en.md) |

## Limitations

- Not production-ready; no real IR↔EX pilot; no throughput, undetectability or connectivity guarantee.
- No resume after process restart or reboot; recovery is same-process only.
- The IR listener does not authenticate end users; beyond loopback it needs service-layer security.
- Record shaping / morphing is experimental; similarity to HTTPS is not established.
- H3, relay and Worker paths are outside the default core; BCC checks service liveness, not traffic quality.
- Not a general-purpose VPN or arbitrary-destination proxy.

## License

All rights reserved by BAFT Project; visible for review only, no license to use, copy, modify, distribute or run. See [COPYRIGHT](COPYRIGHT).
