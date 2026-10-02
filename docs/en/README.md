# BAFT Documentation

[فارسی](../fa/README.md) · [Project README](../../README.en.md)

This is the index of the BAFT technical documentation. It states, for each document, what kind of document it is and who needs it, so a reader can find the authoritative source without reading everything.

## 1. Reading paths

| If you are… | Read, in order |
|---|---|
| Evaluating the project | [Project README](../../README.en.md) → [01 Overview](01-overview.md) → [03 Security model](03-security-model.md) → [STATUS](../../STATUS.en.md) → [KNOWN-LIMITATIONS](../../KNOWN-LIMITATIONS.en.md) |
| Operating servers | [06 Running IR/EX](06-running-ir-ex.md) → [23 Signed releases](23-p1a-signed-releases.md) → [24 BCC access](24-p1c-bcc-access.md) → [26 SSH bootstrap](26-p1d-ssh-bootstrap.md) → [27 Tunnel builder](27-p1e-tunnel-builder.md) |
| Developing the data plane | [02 Architecture](02-architecture.md) → [04 Protocol](04-protocol-baft1.md) → [05 Configuration](05-configuration.md) → [07 Resource control](07-resource-control.md) → [10 Repository layout](10-repository-layout.md) → [08 Testing and CI](08-testing-and-ci.md) |
| Developing the control plane | [22 Launch-1 roadmap](22-launch-1-roadmap.md) → [23](23-p1a-signed-releases.md) → [24](24-p1c-bcc-access.md) → [25 Agent jobs](25-p1d-agent.md) → [26](26-p1d-ssh-bootstrap.md) → [27](27-p1e-tunnel-builder.md) |
| Reviewing research claims | [12 Method](12-innovation-method.md) → [13 TWRL](13-stage-c-twrl.md) → [14 PADL](14-stage-c-padl.md) → [15D ECRL gate](15-stage-d-ecrl.md) → [17 Gates](17-correctness-gates.md) → [18 Soak](18-stage-c-soak.md) |

## 2. Document catalog

Type: **Spec** defines behaviour that code and tests must match · **Guide** explains and instructs · **Record** captures a decision, plan or result · **Research** states a hypothesis with falsification criteria · **Superseded** kept for history · **Experimental** not part of the supported core.

| ID | Document | Type | Audience |
|---|---|---|---|
| 01 | [Overview, goals and scope](01-overview.md) | Guide | All |
| 02 | [Architecture, roles and data flow](02-architecture.md) | Spec | Developers |
| 03 | [Security, identity and trust boundaries](03-security-model.md) | Spec | All |
| 04 | [BAFT/1 protocol and state machine](04-protocol-baft1.md) | Spec | Developers |
| 05 | [Configuration and Routes](05-configuration.md) | Spec | Operators, developers |
| 06 | [Building and running IR/EX](06-running-ir-ex.md) | Guide | Operators |
| 07 | [Memory control, backpressure and scheduling](07-resource-control.md) | Spec | Developers |
| 08 | [Testing, CI and acceptance gates](08-testing-and-ci.md) | Guide | Developers |
| 09 | [Roadmap, Stages A–H](09-roadmap.md) | Record | All |
| 10 | [Repository layout and module responsibilities](10-repository-layout.md) | Guide | Developers |
| 11 | [Glossary](11-glossary.md) | Guide | All |
| 12 | [Innovation methodology](12-innovation-method.md) | Record | Reviewers |
| 13 | [TWRL: tri-watermark receive ledger](13-stage-c-twrl.md) | Research | Reviewers |
| 14 | [PADL: pressure-aged deficit leasing](14-stage-c-padl.md) | Research | Reviewers |
| 15 | [Conservation telemetry and multi-Shard budget](15-conservation-telemetry.md) | Spec | Developers |
| 15D | [ECRL: prior art, threat model, invariants](15-stage-d-ecrl.md) | Research | Reviewers |
| 16 | [Privacy-bounded conservation metrics](16-conservation-metrics.md) | Spec | Developers |
| 17 | [Single-flight correctness gates](17-correctness-gates.md) | Record | Developers |
| 18 | [Stage-C soak gate](18-stage-c-soak.md) | Record | Developers |
| 19 | [Bounded terminal quarantine](19-terminal-quarantine.md) | Spec | Developers |
| 20 | [ECRL note](20-stage-d-ecrl.md) | Superseded | — |
| 21 | [Record shaping / morphing (R3.1)](21-stealth-pro.md) | Experimental | Reviewers |
| 22 | [Launch-1 roadmap](22-launch-1-roadmap.md) | Record | All |
| 23 | [P1-A signed release artifacts](23-p1a-signed-releases.md) | Spec | Operators, developers |
| 24 | [P1-C BCC web access and SQLite state](24-p1c-bcc-access.md) | Spec | Operators, developers |
| 25 | [P1-D secure agent jobs](25-p1d-agent.md) | Spec | Operators, developers |
| 26 | [P1-D server inventory and SSH bootstrap](26-p1d-ssh-bootstrap.md) | Spec | Operators, developers |
| 27 | [P1-E native tunnel builder](27-p1e-tunnel-builder.md) | Spec | Operators, developers |

Decision records: [ADR-0001 project contract](../adr/en/0001-project-contract.md) · [0002 toolchain](../adr/en/0002-toolchain.md) · [0003 HTTP/2 + mTLS Carrier](../adr/en/0003-h2-mtls-carrier.md) · [0004 Flow semantics](../adr/en/0004-stage-b-flow-semantics.md) · [0005 Stage-C resources](../adr/en/0005-stage-c-resources.md) · [0006 control scheduling](../adr/en/0006-control-scheduling.md) · [0007 pinned Noise](../adr/en/0007-pinned-noise-morphing.md). Wire contract: [BAFT/1 notes](../protocol/baft1.en.md) and [golden vectors](../protocol/golden-vectors.json).

## 3. Source hierarchy

When documents disagree, the higher source wins.

| Rank | Source | Governs |
|---:|---|---|
| 1 | Content Blueprint 1.4 and Implementation Master Prompt 1.2 | Intent and scope |
| 2 | Accepted ADRs | Implementation decisions |
| 3 | Schema, wire tests, golden vectors | Executable contracts |
| 4 | [STATUS](../../STATUS.en.md) and [TEST-RESULTS](../../TEST-RESULTS.en.md) | What is built and measured |
| 5 | These guides | Explanation |

A capability described in the Blueprint but not recorded as implemented in STATUS is a **planned contract**, not a current capability.

## 4. Conventions

| Topic | Rule |
|---|---|
| Normative language | MUST, MUST NOT, SHOULD and MAY follow RFC 2119. |
| Evidence | Measured claims cite a CI run, test or recorded result; none are estimated. Results on CI or loopback networking are correctness evidence, not public-network benchmarks. |
| Claim states | Engineering result, research hypothesis and legal novelty are separate states ([12](12-innovation-method.md)). |
| Numbering | Documents are numbered by creation order. `15` (conservation telemetry) and `15D` (ECRL design gate) share a number for historical reasons; `20` is superseded by `15D`. |
| Languages | Persian and English trees are maintained in parallel. Exceptions: `21` exists only in English, and `16-reachability-innovation.md` only in Persian. |
