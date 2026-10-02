# 25 — P1-D: secure agent jobs

Launch-1 step P1-D from [22-launch-1-roadmap.md](22-launch-1-roadmap.md). Part 1 (this document): BCC signs every job and the rules an agent applies before running one (`internal/agentjob`). The agent binary, server inventory and SSH bootstrap follow.

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
