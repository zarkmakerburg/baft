# 27 — P1-E: native tunnel builder

BCC builds an IR/EX tunnel between two enrolled servers and rolls both back if anything goes wrong. It drives the same `baft-pair` pairing as the installer ([06-running-ir-ex.md](06-running-ir-ex.md)), but through signed agent jobs ([25-p1d-agent.md](25-p1d-agent.md)), so the operator never opens a shell. Both servers need `baft-agent` first (install it by hand or with [26-p1d-ssh-bootstrap.md](26-p1d-ssh-bootstrap.md)).

## Use it

```
POST /api/tunnels/plan    {"ex_node":"ex-1","ir_node":"ir-1"}      → plan with a hash
POST /api/tunnels         {"ex_node":"ex-1","ir_node":"ir-1","plan_hash":"…"}
GET  /api/tunnels[?id=…]
POST /api/tunnels/cancel  {"id":"tun-…","reason":"…"}
```

Optional fields and their defaults: `public_address` (the EX node's address), `port` 8443, `target` `127.0.0.1:2443` (a fixed IP:port the EX exits to), `route_listen` `127.0.0.1:1443` (loopback address clients use on the IR), `route_id` `service-main`, `record_shaping` false.

The EX must be a `foreign` node and the IR a `worker` or `master`. A plan is refused, before any job exists, if it breaks the rules agents apply (for example a hostname target or a non-loopback `route_listen`), or if either node is part of a change that is still running.

## Plan, then deploy by hash

`POST /api/tunnels/plan` changes nothing. It returns a deterministic **plan** from the same validation creation uses: per node the changes and the configuration generation (`from → to`), the readiness gates, the steps, what is verified and how a failure rolls back. Gates: agent contact within 5 minutes for both nodes (`FAIL` otherwise), roles and revocation, no unfinished change or failed rollback on either node, parameters valid under the agents' own rules, and a `WARN` when the change will supersede an active tunnel.

The plan carries a `hash` over everything it depends on (not over times). Sending `plan_hash` to `POST /api/tunnels` deploys exactly that plan: BCC recomputes the plan and creates the tunnel in **one store operation** (one lock), so the state that was reviewed is the state it is created against; if the plan no longer passes its gates or its hash differs (a node's generation moved, a tunnel appeared, an agent went quiet, a different request) it answers `409` with the new plan and queues nothing. Without `plan_hash` the API behaves as before, for automation; the dashboard always uses the plan. Both `tunnel.plan` and the deployment record the hash in the audit log, and the tunnel keeps it as `plan_hash`.

## Steps

| Phase | Job | What happens |
|---|---|---|
| `preparing_ex` | `tunnel_prepare_ex` on EX | keys and outer-TLS PKI are created if missing; a one-time pairing code is produced. Nothing live changes. |
| `preparing_ir` | `tunnel_prepare_ir` on IR | the code is consumed, the dialer config is staged and validated, a reply code is produced. Nothing live changes. |
| `committing_ex` | `tunnel_commit_ex` | the reply is verified, the listener config is installed (previous one backed up), the service restarts and must stay active. |
| `committing_ir` | `tunnel_commit_ir` | same for the dialer. |
| `health_ir`, `health_ex` | `tunnel_health` | service active and not restarting across a settle window, and the listener (EX) or local route (IR) accepts connections. Retried up to 5 times. |
| `observing_ir`, `observing_ex` | `tunnel_observe` | the node reports its own state (read-only) and BCC compares it with the plan: change id and phase, role, service active, unit equal to the expected one, route id, listener / peer / target, and generation: previous and current must equal the **reviewed** `from` and `to`, and the current one must equal the node's own counter. Any difference is listed and rolls the change back. |
| `finalizing_ir`, `finalizing_ex` | `tunnel_finalize` | backups and one-time secrets are deleted. |
| `active` | | done; an earlier `active` tunnel on the same nodes becomes `superseded`. |

Any failure, step timeout (20 minutes) or operator cancel moves to `rolling_back`: jobs nobody picked up are cancelled, and every node that reached a step gets `tunnel_rollback`, IR first. The node restores its previous config and unit (or removes them and stops the service when there was none) and restarts the old service. Outcomes: `rolled_back`, or `rollback_failed` if a node did not restore or did not answer within 2 hours. A `rollback_failed` tunnel blocks new changes on its nodes; cancel it again once the node is back to retry.

An `active` tunnel is final. To change it, build a new one: it replaces the old one and the old one is restored if the new one fails.

Agents poll every 30 s by default, so a build takes a few minutes. The pairing code lives 15 minutes; a build that stalls longer than that fails and rolls back.

## Observed state, generation and evidence

A command exiting successfully is never taken as proof. After health, each node is asked what it actually runs (`tunnel_observe`: the live configuration is loaded and digested, the service state and unit are read) and the answer must equal the plan. Each node keeps a **generation** counter, +1 per committed change and restored by rollback. The tunnel created from a plan stores the generation change that plan promised (`expected_generation`); a node that advanced by one step from some other generation (say 99 → 100 against a reviewed 4 → 5) is rejected and the change rolled back. **Bootstrap exception:** while BCC has no verified generation for a node (`applied_generation` is 0, e.g. a node never built through this version), only the node's own consistency is checked (current = previous + 1 = its own counter), the plan says "bootstrap", and the reported value is what BCC records; from then on the next change must start exactly from it. BCC records the generation it verified as the node's `applied_generation`, and the next plan starts from it.

Every health result and observation is stored with the tunnel as **evidence** (`GET /api/tunnels?id=…`): step, node, time, OK, the raw report and any problems. The dashboard shows it under each tunnel.

## Ownership markers

Everything a change installs is labelled as BAFT's: `baft.service` starts with `# baft-managed: true`, `# baft-tunnel: <id>`, `# baft-generation: <n>`, and `<config-dir>/baft.managed.json` records `managed_by`, `tunnel_id`, `generation`, `role` and the SHA-256 of the config and the unit BAFT wrote. (The marker is a separate file because the config is strictly decoded and cannot carry comments.) Consequences:

- `tunnel_observe` reports whether the node is managed, whose marker it is, and whether the live config and unit still match the marker. BCC requires the marker to name this tunnel and the verified generation, and both hashes to match; otherwise the change is rolled back.
- **Rollback never overwrites what BAFT did not write.** If the live config, unit or marker was edited by hand after the commit, rollback is refused with "nothing was changed", the transaction stays committed, and the operator decides. Restoring the BAFT-written content (or reverting to the backup) lets the rollback proceed.
- Rollback restores the previous marker, or removes it when there was none.

## Security

- Jobs are signed by BCC and limited to the actions above; every parameter has a strict pattern (fixed-IP target, loopback route listen, no free text), checked again by the agent.
- The pairing code and the reply are secrets. They travel only in signed jobs and in the ack of the step that produced them, are hidden from `/api/jobs`, are deleted from BCC's records as soon as the next step has them (and on rollback), are never logged by the agent, and the state database overwrites deleted rows (`secure_delete`).
- A node holds its pairing PSK and staged files only inside `<state-dir>/tunnels/<id>` and deletes them on finalize or rollback. The reply is verified with the pairing PSK exactly as in the manual flow, so a reply from anyone without the code is rejected.
- Only one change runs on a node at a time; a second `prepare` while one is in progress is refused.
- Create, cancel and the final outcome of every tunnel are in the audit log.

## Node side

`internal/tunnelnode` does the work on a server: it runs `baft-pair` and `baft config validate` (the installer's own tools), writes `baft.yaml` root-owned and group-readable by the service user, renders `baft.service` identically to `install.sh` (a test compares them), and keeps all transaction state in `<state-dir>/tunnels/<id>/txn.json`. The agent unit now has write access to the BAFT config and state directories and `/etc/systemd/system`, and `baft-agent` takes `--config-dir`, `--baft-state-dir`, `--unit-dir` and `--service-user`.

## Tests

`internal/tunnelnode` runs the real `baft` and `baft-pair` with a pretend systemd: full build, prepare changes nothing live, rollback restores the previous tunnel, a failed commit leaves nothing behind, a forged reply is rejected, one change at a time, parameter validation, unit parity with `install.sh`. `internal/agent/tunnel_flow_test.go` runs BCC and two real agents against each other: a successful build, failure on one side rolling both back, a failed second change restoring the working tunnel, supersede, cancel midway, bad plans, no secret left in listings, state or audit log. `internal/bcc/tunnel_test.go` covers step timeouts, rollback timeouts and retry, health retries and secret wiping.

## Not yet

BCC itself does not check traffic beyond "service up and listening"; `tests/e2e/launch1.sh` (CI job `e2e-launch1`) pushes real traffic through a built tunnel. The dashboard has the controls (Tunnels card).
