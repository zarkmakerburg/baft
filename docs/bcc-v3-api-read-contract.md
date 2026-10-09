# BCC API contract for Command Center V3 — read-only slice

Source: `main@5f1502d4f228584611db3af31d2a7725025b9ebc`, inspected 2026-10-08. This document describes existing Go handlers, not live staging acceptance. UI Work owns `ui/bcc-command-center-v3`; backend changes stay on the Technical HQ branch.

## Authentication and response conventions

Every endpoint below calls `s.admin`: `Authorization: Bearer <admin token>` or a valid BCC admin web session through the configured secret-path access handler. There is **one admin role**, not an implemented RBAC hierarchy. The standalone UI should keep its token in memory and never log it. BCC access may be configured behind a secret-path wrapper; use the same origin and access context as the authenticated BCC session. JSON success responses use `Content-Type: application/json`. Errors currently use `http.Error` plain text; do not assume a JSON error schema or display raw backend error text. Common responses are `401` unauthorized, `405` wrong method, `429` when the security guard limits requests, and `500` for backend failures. No route-specific rate quota or ETag is defined; treat polling conservatively. GET calls are read-only and do not require an idempotency key. None of these GET handlers writes an audit entry for a successful read; authentication failures are counted by the guard.

| Method and path | Query / request | 200 response | Other handler responses | Evidence |
|---|---|---|---|---|
| GET `/api/nodes` | none | array of `Node` | 401, 405 | `internal/bcc/server.go:244-282` |
| GET `/api/tunnels` | optional `id` string | array of `Tunnel` without id; single `Tunnel` with id | 401, 404 for unknown id, 405 | `internal/bcc/tunnel.go:858-910` |
| GET `/api/jobs` | none | array of sanitized `Job` via `publicJob` | 401, 405 | `internal/bcc/server.go:285-291` |
| GET `/api/monitoring` | none | array of `MonitoringNode` | 401, 405 | `internal/bcc/server.go:436-440` |
| GET `/api/health` | optional `node` string; `all=1` with node removes history limit | `{policy, nodes}` | 401, 405 | `internal/bcc/health_engine.go:324-345` |
| GET `/api/history` | required `node_id` string | array of node history points | 400 missing node_id, 401, 405 | `internal/bcc/server.go:442-448` |
| GET `/api/finance` | none | array of `NodeFinance` | 401, 405 | `internal/bcc/server.go:384-411` |
| GET `/api/finance/report` | optional `period` (default daily), `from`, `to`, `tz` (default Asia/Tehran), `format=csv` | JSON `{period,timezone,rows}` or CSV attachment | 400 invalid range/period, 401, 405, 500 CSV failure | `internal/bcc/server.go:414-433` |
| GET `/api/audit` | none | array of at most 200 `AuditEntry` | 401, 405, 500 read failure | `internal/bcc/server.go:488-494` |
| GET `/api/backups` | none | `{backups:[{name,size,modified_at}]}` | 401, 405, 503 backup admin disabled; 400 filesystem failure | `internal/bcc/backup_admin.go:118-132` |

Minimal response fields for UI binding (additional fields may exist):
- `Node`: `id,alias,address,role,health,latency_ms,updated_at`; optional `path_ipv4,path_ipv6,agent_seen,revoked`. Existing `Node` also serializes token **hash** metadata; UI should avoid persisting or rendering it.
- `Tunnel`: `id,ex_node,ir_node,public_address,port,target,route_id,route_listen` plus lifecycle/evidence fields. A tunnel row is desired/control state, not proof of live user traffic.
- `MonitoringNode`: `node_id,alias,address,role,status,health_check_status,latency_ms,last_seen,active_sessions,handshake_errors,noise_latency_ms,handshake_error_rate_milli_per_min,routes`. Status can be `unknown`, `up`, or `down`; stale telemetry changes status after the configured threshold.
- `NodeFinance`: `node_id,ingress_bytes,egress_bytes,cost_micros,revenue_micros,profit_micros,updated_at`. Monetary values are integer micros, not floating currency.
- `BackupAdminEntry`: `name,size,modified_at`; this is file inventory, not backup validity.
- `Job` has `id,type,node_id,status,message,created_at,updated_at`; `publicJob` sanitizes its output.

## Write boundary and missing product contracts

`POST /api/backups/restore-preview` exists with request `{"filename":"<name>.baftbak"}`, admin authentication, audited outcome and a `RestorePreview` JSON result. It validates the selected regular file and returns 400/404/503/500 as applicable. It **does not restore**. The actual restore is an offline CLI operation; there is no BCC HTTP restore endpoint. Do not wire an irreversible restore button as if it already exists.

`POST /api/tunnels` and other mutation routes exist, but need separate reviewed schemas, safety preconditions, audit and idempotency tests before UI write integration. There is no verified login/logout API for the standalone V3 frontend, no generic `/api/dashboard` aggregate, no server-side RBAC, and no implemented `/api/settings` or `/api/updates` route in the inspected handler. Do not present synthetic KPIs as live telemetry. Authenticated staging integration, error paths and browser tests remain OPEN.

Coverage for this document is source inspection only. Existing Go tests cover individual handlers, but this exact frontend contract has not passed an authenticated UI staging test.
