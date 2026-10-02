<p align="center"><img src="docs/assets/baft-logo.png" alt="BAFT" width="220"></p>

> © 2026 BAFT Project. All rights reserved. This repository is public for review only; no license is granted. See [COPYRIGHT](COPYRIGHT).

# BAFT — Authenticated TCP Relay and Tunnel Control Plane

> **Language:** [فارسی](README.md) | English

[![ci](https://github.com/zarkmakerburg/baft/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/zarkmakerburg/baft/actions/workflows/ci.yml)
[![stagec-soak](https://github.com/zarkmakerburg/baft/actions/workflows/stagec-soak.yml/badge.svg?branch=main)](https://github.com/zarkmakerburg/baft/actions/workflows/stagec-soak.yml)
[![vulncheck](https://github.com/zarkmakerburg/baft/actions/workflows/vulncheck.yml/badge.svg)](https://github.com/zarkmakerburg/baft/actions/workflows/vulncheck.yml)

## Abstract

BAFT is a relay for authenticated TCP byte streams between two operator-controlled nodes. A multiplexed, credit-based protocol (BAFT/1) runs inside a TLS 1.3 Carrier built on HTTP/2. A local listener on the **IR** node is bound to exactly one pre-authorized target behind the **EX** node; the remote peer can name a route but never a destination. A control plane, **BCC**, enrolls servers, builds tunnels on both ends as a transaction, verifies them, and reverts both ends on failure. Every artifact installed on a server is signed and verified against an offline root of trust before it runs.

**Maturity: research software. Not declared production-ready.** Section 9 states what is and is not established.

## 1. Scope

| In scope | Out of scope |
|---|---|
| Bounded-memory relay of TCP streams between two nodes with fixed Routes | General-purpose VPN, SOCKS or arbitrary-destination proxy |
| Peer authentication, Route authorization, revocation | End-user authentication on the IR listener |
| Signed releases, verified installation, anti-rollback | Third-party package distribution |
| Server enrollment, tunnel build/verify/rollback from BCC | Guarantees of throughput, undetectability or reachability |
| Same-process carrier recovery with epoch fencing | Resume across process restart or machine reboot |

## 2. System model

```text
 application ─► 127.0.0.1:1443 ─► BAFT IR ═══ Shard 0..N ═══► BAFT EX ─► fixed target
                  (IR host)       dialer     TCP+TLS1.3+H2    listener    (EX host)

 operator ─► BCC ── signed jobs (Ed25519), pulled over HTTPS ──► baft-agent ─► baft · baft-pair · systemd
```

| Term | Definition |
|---|---|
| Node | A BAFT process in the `dialer` (IR) or `listener` (EX) role. |
| Carrier | Authenticated bidirectional byte stream transporting BAFT/1 frames. |
| Shard | Independent Carrier with its own transport and scheduling; Shards are never pooled onto one TCP connection. |
| Flow | One bidirectional application TCP connection, with an independent byte-offset space per direction. |
| Route | Pre-authorized binding of a `route_id` to a local listener (IR) and a fixed target (EX). |
| ACK / WINDOW | ACK is the next contiguous byte accepted by BAFT, not proof of application processing. WINDOW is an absolute maximum offset (credit), not a delta. |
| BCC / agent | Control-plane server and the per-server executor of signed, allowlisted jobs. |

## 3. Data-plane design

### 3.1 BAFT/1 framing

All integers are big-endian. The header is 24 bytes; payload is at most 65 536 bytes (total frame 24–65 560). Type, flags and reserved bits are validated and bounds are checked **before** payload allocation.

| Offset | Size | Field | Rule |
|---:|---:|---|---|
| 0 | 4 | `frame_len` | Total length including header. |
| 4 | 1 | `type` | `HELLO`, `HELLO_ACK`, `READY`, `OPEN`, `OPEN_OK`, `OPEN_ERR`, `DATA`, `ACK`, `WINDOW`, `FIN`, `FIN_ACK`, `RESET`, `PING`, `PONG`, `GOAWAY`, plus reserved resume/profile types. |
| 5 | 1 | `flags` | Zero in v1. |
| 6 | 2 | `reserved` | Zero in v1. |
| 8 | 8 | `stream_id` | Zero for Session control, non-zero for a Flow. |
| 16 | 8 | `offset` | Type-specific byte or state offset. |

Session start is `HELLO → HELLO_ACK → two-sided READY`. `OPEN` carries a `route_id` and a random nonce, never a host; a duplicate `OPEN` is idempotent and does not redial the target. Duplicate or overlapping `DATA` is never written twice; gaps and credit violations are rejected. Wire errors use a fixed vocabulary (`AUTH_FAILED`, `ROUTE_DENIED`, `FLOW_CONTROL_ERROR`, `RESOURCE_EXHAUSTED`, `STALE_EPOCH`, …); no OS error text crosses the wire. Full state machine: [04](docs/en/04-protocol-baft1.md).

### 3.2 Resource control

| Mechanism | Behaviour |
|---|---|
| Global allocator | Separate receive and replay pools that do not borrow from each other (example configuration: 256 MiB total, 128/128 MiB), plus per-Flow caps bounded by their pool. |
| Credit reservation | A receiver reserves real capacity before advertising a larger `WINDOW`; a sender reserves replay capacity before reading application bytes. |
| Backpressure | When credit or budget is exhausted BAFT stops reading from the source; TCP backpressure propagates. No unbounded queue exists. |
| Scheduling | Byte-based deficit round robin with pressure aging (PADL); a bounded control queue (256 messages / 1 MiB) with a finite burst cap keeps ACK/WINDOW/FIN from queueing behind bulk DATA without starving it. |

Details: [07](docs/en/07-resource-control.md), [13](docs/en/13-stage-c-twrl.md), [14](docs/en/14-stage-c-padl.md).

### 3.3 Carrier authentication

| Mode | Selected when | Mechanism |
|---|---|---|
| mTLS | Configuration has no `noise` section | Certificate chain, hostname, validity and EKU verified; node identity derived from a verified URI SAN. |
| Pinned Noise IK | Provisioned by `install.sh` pairing | Outer TLS validated against a local CA; peers authenticate with pinned Noise static keys, each mapped to one allowed identity ([ADR-0007](docs/adr/0007-pinned-noise-morphing.md)). |

### 3.4 Recovery

A live Session can replace its Carrier within the same process: epoch fencing, bounded replay from validated plans, a two-sided prepare/commit barrier with idempotent commit identity, and fail-closed handling of a peer boot-ID change. Persistent snapshots and restart/reboot resume are not implemented.

## 4. Control plane and supply chain

### 4.1 Components

| Binary | Responsibility |
|---|---|
| `baft` | Node runtime (`run`), `config validate`, `status`, `doctor`, `logs`. |
| `baft-pair` | Key generation, outer-TLS PKI, one-time pairing that writes both configurations. |
| `baft-bcc` | Monitoring, finance, hash-chained audit, encrypted backup, token rotation/revocation, signed jobs, SSH bootstrap, tunnel builder, web access. |
| `baft-agent` | Executes signed, allowlisted jobs; updates only to release-signed binaries. |
| `baft-release` | `keygen`, `certify`, `revoke`, `sign`, `verify`. |
| `baft-bcc-audit-verify` | Offline verification of the audit chain and anchors. |
| `baft-master`, `baft-worker`, `baft-cluster-keygen` | Earlier cluster/mesh runtime and key tooling. |

### 4.2 Release trust chain

```text
 offline Ed25519 root key ──certifies──► CI release key ──signs──► manifest + SHA256SUMS + binaries
        │                                                                   ▲
        └── signs ──► revocation list (sequence, expiry ≤ 400 days)         │
 root public key pinned in install.sh and baft-agent ── verification before any artifact runs ──┘
```

Verification covers certificate, revocation list, manifest signature, `SHA256SUMS` and each binary hash. Installation refuses downgrades, re-tagged versions and replayed revocation lists, and records state in a root-owned file. Builds are reproducible and compared in CI. See [23](docs/en/23-p1a-signed-releases.md).

### 4.3 Job model

BCC signs each job (Ed25519, DSSE-style envelope). The agent runs a job only if the signature verifies against the pinned BCC key, the job names this node, the action is on the allowlist with exactly its parameters (each matching a strict pattern), it is within its validity window (1 hour as issued) and its ID has not been seen. There is no shell or free-form command action. Allowlist: `health`, `restart`, `reload`, `update_baft`, and the `tunnel_*` actions ([25](docs/en/25-p1d-agent.md)).

### 4.4 Tunnel transaction

| Phase | Effect |
|---|---|
| prepare EX / IR | Keys and PKI ensured; pairing code and reply produced; new configuration staged. Nothing live changes. |
| commit EX / IR | Previous configuration and unit backed up; staged configuration installed; service must stay active for a settle window or the node restores itself. |
| health IR / EX | Service stable without restarts; listener and local route accept connections; retried. |
| finalize | Backups and one-time secrets deleted. |
| rollback | From any phase, every node that reached a step restores its previous configuration, unit and service state. A step timeout, failure or operator cancel triggers it. |

Pairing secrets exist in jobs only while a step needs them and are removed afterwards. See [27](docs/en/27-p1e-tunnel-builder.md).

## 5. Security requirements

Normative keywords follow RFC 2119.

| ID | Requirement | Specification · evidence |
|---|---|---|
| SR-1 | The Carrier MUST use TLS 1.3 or later with no plaintext fallback; certificate chain, hostname, validity and EKU checks MUST NOT be disableable. | [03](docs/en/03-security-model.md) · `ci/test` |
| SR-2 | A node's identity MUST come from authentication, not from HELLO metadata; CA trust and Route authorization MUST be separate. | [03](docs/en/03-security-model.md) · `ci/test` |
| SR-3 | A peer MUST NOT be able to select a destination; frames MUST be validated before allocation. | [04](docs/en/04-protocol-baft1.md) · `ci/test`, COR-01 |
| SR-4 | Receive and replay memory MUST be bounded; exhaustion MUST cause backpressure, not growth. | [07](docs/en/07-resource-control.md) · `stagec-soak` |
| SR-5 | User payload MUST NOT appear in logs or support bundles. | [03](docs/en/03-security-model.md) · `ci/test` |
| SR-6 | Installed artifacts MUST verify against the pinned root, an unexpired revocation list and recorded hashes; downgrade and re-tag MUST be refused. | [23](docs/en/23-p1a-signed-releases.md) · `e2e-install-release`, `release-dry-run` |
| SR-7 | BCC web access MUST use a random secret path and credentials, store only a password hash, protect sessions against CSRF and rate-limit sign-in. | [24](docs/en/24-p1c-bcc-access.md) · `ci/test` |
| SR-8 | An agent MUST run only BCC-signed, allowlisted, unexpired, previously unseen jobs with strictly validated parameters. | [25](docs/en/25-p1d-agent.md) · `ci/test`, `e2e-agent-enroll` |
| SR-9 | SSH bootstrap MUST pin the host key and MUST NOT persist, log or audit credentials. | [26](docs/en/26-p1d-ssh-bootstrap.md) · `e2e-ssh-bootstrap` |
| SR-10 | A tunnel change MUST be reversible on every touched node until finalized, and pairing secrets MUST be removed after use. | [27](docs/en/27-p1e-tunnel-builder.md) · `e2e-launch1` |
| SR-11 | Administrative actions MUST be recorded in a tamper-evident audit log. | [24](docs/en/24-p1c-bcc-access.md) · `ci/test` |

## 6. Verification

| CI job | Evidence produced |
|---|---|
| `test` | Unit and integration tests, race detector, staged evidence gates (stability, sync integrity, mesh, telemetry, BCC hardening, backup/restore, ECRL runtime). |
| `e2e-binaries`, `e2e-install` | Real IR→EX traffic through binaries built from source and installed on systemd. |
| `e2e-install-release`, `release-dry-run` | Install from a signed release; reproducible builds; tamper, revocation, replay, downgrade and re-tag rejected. |
| `e2e-agent-enroll`, `e2e-ssh-bootstrap` | Agent enrollment and a signed job on systemd; SSH bootstrap against a real `sshd`, wrong host key refused. |
| `e2e-launch1` | Two servers enrolled; tunnel built by BCC with real traffic; a failing change rolled back on both sides; no secret left on BCC or the servers. |
| `stagec-soak`, `step57-recovery-soak` | Repeated multi-Flow, slow-receiver and recovery soaks. |
| `vulncheck` | `govulncheck`, weekly and on dependency changes. |

Results are CI/loopback correctness evidence, not public-network benchmarks. Records: [STATUS](STATUS.en.md), [TEST-RESULTS](TEST-RESULTS.en.md).

## 7. Installation and operation

Requirements: Linux with systemd, `curl`, `openssl`, `python3`, a `sudo` user. The installer fetches the signed release for the architecture (`amd64`, `arm64`), verifies it (SR-6) and installs nothing on failure.

```bash
# EX: prints a one-time pairing code
sudo bash -c "$(curl -fsSL https://raw.githubusercontent.com/zarkmakerburg/baft/main/install.sh)" baft --role ex --public-address EX_IP_OR_HOST

# IR: paste the pairing code, then return the printed reply code to the EX
sudo bash -c "$(curl -fsSL https://raw.githubusercontent.com/zarkmakerburg/baft/main/install.sh)" baft --role ir

# Server managed by BCC (agent only)
sudo env BAFT_BCC_JOB_KEY="$(baft-bcc jobkey show)" BAFT_AGENT_TOKEN=NODE_TOKEN \
  bash -c "$(curl -fsSL https://raw.githubusercontent.com/zarkmakerburg/baft/main/install.sh)" baft \
  --agent-only --bcc-url https://bcc.example.com --node-id ex-1
```

`BAFT_BCC_JOB_KEY` is printed by `baft-bcc jobkey show` on the BCC host; `--version vX.Y.Z` pins a release. `bash -c` is used instead of `| bash` so the terminal remains available for the codes. Independent verification of a release:

```bash
baft-release verify -dir <download dir> -root-pub release/keys/root.pub -revocations release/keys/revocations.json
```

Operating with BCC: start `baft-bcc` (HTTPS or loopback) and run `baft-bcc access init`; add servers from the dashboard (**Add a server over SSH**) or with the agent-only command; build a tunnel from **Tunnels** or `POST /api/tunnels {"ex_node":"…","ir_node":"…"}`; cancel with `POST /api/tunnels/cancel`. Local inspection: `baft status`, `baft doctor`, `baft logs`.

Build: `go build ./... && go vet ./... && go test ./... && go test -race ./...` (Go 1.27.1, see `go.mod`).

## 8. Releases and versioning

Releases are tagged `vMAJOR.MINOR.PATCH` and built by the `release` workflow from a commit on `main`, signed in a protected environment, verified against the pinned root, and published as a draft for the owner to review. Release notes live in `release/notes/<tag>.md`. Current: [`v0.1.0`](https://github.com/zarkmakerburg/baft/releases/tag/v0.1.0), root key id `98741d81da746d2e435302f90569911d`. The revocation list is root-signed with a sequence number and an expiry of at most 400 days.

## 9. Conformance status and known limitations

| Area | Established | Not established |
|---|---|---|
| Data plane A–B | Secure vertical slice; 1 GiB bidirectional gate (COR-01) | — |
| Data plane C | Allocator, backpressure, PADL, TWRL; repeated soak and race sample | Public-network benchmark; formal 60 s × 5 profiling |
| Data plane D | Same-process recovery with epoch fencing | Restart/reboot resume; durable snapshots |
| Control plane | SR-6 to SR-11 as verified in section 6 | Traffic-quality measurement beyond service liveness |
| Deployment | Systemd installation on CI hosts | Real IR↔EX pilot; certificate rotation and support-bundle operations |

Further limitations: the IR listener does not authenticate end users and requires service-layer security if exposed beyond loopback; record shaping / morphing is experimental and similarity to HTTPS is not established; H3, relay and Worker paths are outside the default core. See [KNOWN-LIMITATIONS](KNOWN-LIMITATIONS.en.md).

## 10. Documentation

| Topic | Documents |
|---|---|
| Design | [01 Overview](docs/en/01-overview.md) · [02 Architecture](docs/en/02-architecture.md) · [03 Security model](docs/en/03-security-model.md) · [04 Protocol](docs/en/04-protocol-baft1.md) · [05 Configuration](docs/en/05-configuration.md) · [11 Glossary](docs/en/11-glossary.md) · [ADRs](docs/adr/) |
| Operation | [06 Running IR/EX](docs/en/06-running-ir-ex.md) · [07 Resource control](docs/en/07-resource-control.md) · [08 Testing and CI](docs/en/08-testing-and-ci.md) · [10 Repository layout](docs/en/10-repository-layout.md) |
| Launch-1 | [22 Roadmap](docs/en/22-launch-1-roadmap.md) · [23 Releases](docs/en/23-p1a-signed-releases.md) · [24 BCC access](docs/en/24-p1c-bcc-access.md) · [25 Agent](docs/en/25-p1d-agent.md) · [26 SSH bootstrap](docs/en/26-p1d-ssh-bootstrap.md) · [27 Tunnel builder](docs/en/27-p1e-tunnel-builder.md) |
| Research | [09 Roadmap A–H](docs/en/09-roadmap.md) · [12 Method](docs/en/12-innovation-method.md) · [15 Telemetry](docs/en/15-conservation-telemetry.md) · [15D ECRL](docs/en/15-stage-d-ecrl.md) · [17 Gates](docs/en/17-correctness-gates.md) · [18 Soak](docs/en/18-stage-c-soak.md) · [21 Morphing](docs/en/21-stealth-pro.md) |
| Records | [STATUS](STATUS.en.md) · [PLAN](PLAN.en.md) · [BLOCKERS](BLOCKERS.en.md) · [dependency lock](dependency-lock.en.md) · [novelty matrix](novelty-matrix.en.md) |

## License

All rights reserved by BAFT Project. The repository is visible for review only; no license to use, copy, modify, distribute or run the software is granted. See [COPYRIGHT](COPYRIGHT).
