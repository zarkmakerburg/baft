# 25 — P1-D: secure agent jobs

Launch-1 step P1-D from [22-launch-1-roadmap.md](22-launch-1-roadmap.md): BCC signs every job, the rules an agent applies before running one (`internal/agentjob`), and the agent itself (`baft-agent`). Server inventory and SSH bootstrap follow.

## Trust

- BCC holds a job-signing Ed25519 key, `<state-file>.job-key` (owner-only, created on first start; `baft-bcc jobkey show` prints the public key).
- An agent pins that public key at enrollment. It is a different key from the release root: a compromised BCC cannot sign releases, and an agent installs only releases that verify against the release root (P1-A/P1-B).
- Jobs are DSSE envelopes with payload type `application/vnd.baft.agent-job+json`, the same envelope format as releases.

## What an agent runs

An agent runs a job only if all of these hold:

1. The signature verifies against the pinned BCC key and the payload type is the job type.
2. The payload parses strictly (no unknown fields) and has schema 1.
3. `node_id` is this node.
4. The action is on the allowlist and has exactly its parameters, each matching its pattern:

   | Action | Parameters |
   |---|---|
   | `health` | — |
   | `restart` | — |
   | `reload` | — |
   | `update_baft` | `version`: a release tag `vX.Y.Z[-pre]` |
   | `enroll_peer` | `worker_id`, `public_key` |

   There is no shell or free-form command action.
5. `issued_at` is not more than 5 minutes in the future, `expires_at` has not passed, and the window is at most 24 hours (BCC issues 1 hour at dispatch).
6. The job ID has not been run before (the agent records IDs it runs).

BCC applies the same shape checks before signing, so it never hands out a job an agent would refuse. Deploy jobs now require a release tag (`vX.Y.Z[-pre]`).

## API

`GET /api/agent/jobs` (agent token, unchanged) now returns each job with a `signed` envelope. The plain fields stay for display; agents act only on `signed`.

## Tests

`internal/agentjob`: valid jobs accepted; shell actions, missing, extra and malformed parameters, long or inverted windows refused at signing; wrong key, wrong node, expired, future-dated, replayed, tampered and type-confused envelopes refused. `internal/bcc/jobsign_test.go`: jobs pulled over the API carry envelopes the agent verifier accepts (deploy and enrollment mapping), expire after the dispatch window, are refused by another node; deploys need a release tag; the job key is owner-only and stable.

## The agent (`baft-agent`)

```bash
baft-agent --bcc-url https://bcc.example.com --node-id ex-1 \
  --token-file /etc/baft-agent/token \
  --bcc-job-key /etc/baft-agent/bcc-job.pub \
  --release-root /etc/baft-agent/release-root.pub
```

- Polls `GET /api/agent/jobs` every 30 s (`--interval`; `--once` handles pending jobs and exits). The BCC URL must be `https://` (the agent token travels on it) unless `--allow-insecure-http`. The token file must be owner-only.
- Runs a job only after `agentjob.Verifier` accepts it, records the job ID in `--state-dir` **before** running it (a crash cannot replay it), and acknowledges `succeeded` or `failed` with a short message. Refused jobs are acknowledged as failed when the signed payload names the same job and node.
- `health` runs `baft doctor`; `restart` and `reload` call `systemctl` and then require the service to be active after a settle time.
- `update_baft vX.Y.Z`:
  1. downloads the manifest, certificate, `SHA256SUMS` and this architecture's `baft` and `baft-pair` from `<release-base-url>/<version>/`, and the current revocation list;
  2. verifies them with `release.VerifyDir` against the pinned **release root** (not the BCC key), the revocation list and the installer trust state (`/opt/baft/release-state.json`: no downgrade, no re-tag), requiring exactly the downloaded artifacts;
  3. refuses if the signed version is not the one asked for;
  4. keeps the current binaries as `.prev`, swaps in the new ones, restarts the service;
  5. if the service is not active after the settle time, restores `.prev`, restarts again and reports the failure; otherwise records the release in the trust state.
- `enroll_peer` is acknowledged as not implemented yet.
- `baft-agent` is now one of the signed release artifacts (`scripts/release/build.sh`).

Tests (`internal/agent`, against a real `bcc.Server` and a locally signed release): a deploy job installs the verified release, keeps `.prev`, restarts the service, records the trust state, marks the BCC job succeeded and survives an agent restart without replay; a service that does not come up is rolled back and nothing is recorded; a tampered or older release changes nothing; jobs signed by an unpinned key are refused without touching the host; restart, reload and health call the right commands and a down service fails the job.
