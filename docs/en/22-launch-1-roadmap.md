# 22 — Launch-1 roadmap (approved scope)

Status: scope approved by the project owner on 2026-10-01, from the HQ review of the advisory master roadmap. Each step below becomes its own mission (scope, non-scope, invariants, tests, exit criteria, rollback) and is approved separately before work starts.

## Baseline

- Freeze candidate: `e38dd6a` on `release-v1-goldapp`. Push CI, PR CI, Stage-C soak and the recovery soak (including the Flow-churn gate) all passed on this exact SHA. Declaring the Core Freeze is the owner's decision.
- What exists today: BAFT binaries, `install.sh` (builds from source), pairing that writes `baft.yaml` for both roles, systemd units, real-traffic E2E in CI, and a BCC with monitoring, finance, audit, backup, revocation and token rotation.
- What does not exist yet:
  - BCC sign-in is a single static admin bearer token; there are no users, sessions or secret path.
  - BCC creates `deploy_baft` jobs and serves `/api/agent/jobs`; the agent (P1-D) executes them.
  - There are no signed release artifacts.

## Launch-1 steps

| Step | Content | Depends on |
|---|---|---|
| P1-A | Signed release artifacts (baft, baft-pair, baft-bcc; amd64/arm64; SHA256SUMS, signature, manifest, provenance) and branch protection on `main` / `release-v1-goldapp` | Core Freeze; signing-key decision |
| P1-B | Installer that installs verified release artifacts (no Go, git or build tools on production servers) and a minimal `baft` CLI: `status`, `doctor`, `logs` | P1-A |
| P1-C | BCC access: random secret path, random unique username, strong password (hash only); console-only regeneration that rotates all three and revokes every session; rate limiting; HTTPS lifecycle. HTTP only on localhost or with explicit confirmation. BCC state moves from a JSON file to a versioned SQLite schema with migrations | P1-B |
| P1-D | Secure agent: pull-based, allowlisted actions only, jobs signed by BCC and verified against a pinned key, updates accepted only for release-signed binaries. Server inventory and SSH bootstrap with host-key pinning; bootstrap credentials are destroyed after enrollment | P1-C |
| P1-E | Native tunnel builder: plan, validate, prepare both sides, commit, health check, roll back both sides on failure. Basic health, backup and rollback-safe update | P1-D |
| P1-F | Full Launch-1 E2E on one exact RC SHA | all |

## Deferred to P2

- Driver contract for external engines, then the first adapter. Each third-party engine needs its own security, licence and maintenance review.
- Network autotune. Only the read-only diagnosis in `baft doctor` is in Launch-1.
- Canary rollout, RBAC, 2FA/passkeys, multi-region control plane, durable process-restart recovery.

## Open owner decisions

- Declaring the Core Freeze on the candidate SHA.
- Where the release/job signing root key lives and how it is rotated.
- Branch protection settings.
- `ca.key` currently stays on the EX host.

## Principles kept from the advisory

- Servers first, tunnels second.
- BAFT Native is the primary engine.
- Native recovery and cross-engine failover are distinct and must be shown as distinct.
- No transport is advertised as working everywhere: measure, score, then select.
