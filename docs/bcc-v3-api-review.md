# BCC V3 — Verified API mapping and review gates

Reviewed from current `main`:
- `internal/bcc/server.go` registers `GET /api/tunnels`, `GET /api/monitoring`, `GET /api/nodes`, `GET /api/history`.
- The `monitoring` handler requires admin authentication and returns `MonitoringSnapshot(now, TelemetryStaleAfter)`. Default stale threshold is 3 minutes.
- The existing dashboard sends `Authorization: Bearer <token>` and stores its token in localStorage. The V3 preview uses an ephemeral in-memory token instead and sends only GET requests.
- The existing dashboard renders monitoring entries with `node_id`, `status`, `noise_latency_ms`, `latency_ms`, `last_seen`, `routes` and error-rate fields. Those are node/route observations, not guaranteed to be specific to a BCC tunnel ID.
- Existing tunnel entries include `id`, `ir_node`, `ex_node`, `phase`, `error`, and other lifecycle fields.

## Mandatory data integrity rules

1. Do not equate a tunnel's `phase=active` with healthy traffic flow.
2. Do not use aggregate node RTT, node throughput or node status as per-tunnel measurements.
3. Do not mark a tunnel DOWN from a lifecycle phase alone: use tunnel-scoped live failure evidence; otherwise UNKNOWN.
4. Never use an IP geolocation guess as a server's exact physical coordinate. Require verified coordinates; label approximate values.
5. If telemetry is absent, stale, inconsistent, or missing its identity mapping, show UNKNOWN with timestamp, never a fabricated number.
6. Never expose BCC admin tokens in URL query strings, files, console logs, screenshot fixtures or PR comments.

## Next implementation acceptance checks

- Confirm exact JSON field casing and optional/null semantics from source types and representative sanitized fixtures.
- Add authenticated same-origin read-only endpoint or verified adapter mapping with stable tunnel identifiers and a per-tunnel telemetry contract.
- Add explicit loading/error/stale states; avoid retry storms and ensure expired sessions are handled.
- Automated unit checks for schema, coordinates, dateline, state transitions, stale timestamps and authorization errors.
- Browser validation for English/Persian, RTL/LTR, light/dark, reduced motion, keyboard and touch.
- Owner's canonical BAFT logo must be imported from approved original asset before release.
- No production deployment or merge while the draft PR is under review.

This document is an evidence-based design audit, not evidence of an operational integration test.
