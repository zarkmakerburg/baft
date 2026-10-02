# 27 — P1-E: native tunnel builder

BCC builds an IR/EX tunnel between two enrolled servers and rolls both back if anything goes wrong. It drives the same `baft-pair` pairing as the installer ([06-running-ir-ex.md](06-running-ir-ex.md)), but through signed agent jobs ([25-p1d-agent.md](25-p1d-agent.md)), so the operator never opens a shell. Both servers need `baft-agent` first (install it by hand or with [26-p1d-ssh-bootstrap.md](26-p1d-ssh-bootstrap.md)).

## Use it

```
POST /api/tunnels         {"ex_node":"ex-1","ir_node":"ir-1"}
GET  /api/tunnels[?id=…]
POST /api/tunnels/cancel  {"id":"tun-…","reason":"…"}
```

Optional fields and their defaults: `public_address` (the EX node's address), `port` 8443, `target` `127.0.0.1:2443` (a fixed IP:port the EX exits to), `route_listen` `127.0.0.1:1443` (loopback address clients use on the IR), `route_id` `service-main`, `record_shaping` false.

The EX must be a `foreign` node and the IR a `worker` or `master`. A plan is refused, before any job exists, if it breaks the rules agents apply (for example a hostname target or a non-loopback `route_listen`), or if either node is part of a change that is still running.

## Steps

| Phase | Job | What happens |
|---|---|---|
| `preparing_ex` | `tunnel_prepare_ex` on EX | keys and outer-TLS PKI are created if missing; a one-time pairing code is produced. Nothing live changes. |
| `preparing_ir` | `tunnel_prepare_ir` on IR | the code is consumed, the dialer config is staged and validated, a reply code is produced. Nothing live changes. |
| `committing_ex` | `tunnel_commit_ex` | the reply is verified, the listener config is installed (previous one backed up), the service restarts and must stay active. |
| `committing_ir` | `tunnel_commit_ir` | same for the dialer. |
| `health_ir`, `health_ex` | `tunnel_health` | service active and not restarting across a settle window, and the listener (EX) or local route (IR) accepts connections. Retried up to 5 times. |
| `finalizing_ir`, `finalizing_ex` | `tunnel_finalize` | backups and one-time secrets are deleted. |
| `active` | | done; an earlier `active` tunnel on the same nodes becomes `superseded`. |

Any failure, step timeout (20 minutes) or operator cancel moves to `rolling_back`: jobs nobody picked up are cancelled, and every node that reached a step gets `tunnel_rollback`, IR first. The node restores its previous config and unit (or removes them and stops the service when there was none) and restarts the old service. Outcomes: `rolled_back`, or `rollback_failed` if a node did not restore or did not answer within 2 hours. A `rollback_failed` tunnel blocks new changes on its nodes; cancel it again once the node is back to retry.

An `active` tunnel is final. To change it, build a new one: it replaces the old one and the old one is restored if the new one fails.

Agents poll every 30 s by default, so a build takes a few minutes. The pairing code lives 15 minutes; a build that stalls longer than that fails and rolls back.

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

A dashboard form (use the API), real-systemd runs of a two-server build in CI and the full end-to-end on one release candidate (P1-F), and traffic-level checks beyond "service up and listening".
