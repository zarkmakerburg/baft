# 30 — BCC layered health with hysteresis

HQ P1-1. Every node has a health state per **layer**, and a state changes only after several consistent samples over a minimum time, never on one reading. Each transition carries a reason and the evidence, is written to the audit log and is kept in a visible history that survives a restart.

## Layers

| Layer | What BCC observes | Source |
|---|---|---|
| L0 | BAFT process reporting | the node's telemetry: stale telemetry from a node that used to report is BAD |
| L1 | node reachable | BCC's own TCP probe of the node address (not the carrier between IR and EX) |
| L2 | peer sessions authenticated | active sessions in telemetry = OK; handshake error rate above the alert threshold = BAD; no sessions = no evidence |
| L3 | session correctness | **NOT_ASSESSED** (no recovery or epoch signal reaches BCC yet) |
| L4 | route probes | the route probes the node runs: any `down` = BAD, at least one `up` and none down = OK |
| L5 | target reachable | **NOT_ASSESSED** (telemetry does not say which probe is a target) |
| L6 | application traffic | **NOT_ASSESSED** (needs an end-to-end probe) |
| A | agent polling | the agent's last poll of BCC (management plane) |

Configuration conformity (the drift state of the node's active tunnel, doc 27) is shown next to the layers but is not part of these state machines: it is an observation of files, not of liveness.

## State machine (per layer)

```
UNKNOWN --(5 OK)--> UP --(2 consecutive BAD)--> DEGRADED --(5 BAD and 30 s)--> DOWN
DEGRADED --(3 consecutive OK)--> UP
DOWN --(first OK)--> RECOVERING --(5 OK and 30 s)--> UP
RECOVERING --(2 consecutive BAD)--> DOWN
```

Every sample is **OK**, **BAD** or **NONE** (no evidence). One BAD between OKs, or strict alternation, never leaves UP. A layer is never UP before it has proven itself with consecutive OK samples, so there is no false UP at startup either.

- **Silence is not failure and not health.** NONE counts as neither OK nor BAD: it resets the runs, it can never complete a recovery or a failure, and after 3 minutes of continuous NONE the layer becomes **UNKNOWN**, not DOWN.
- **NOT_ASSESSED is not DOWN and not UNKNOWN.** It marks a layer BCC has no signal for; it is a fixed label, not a state a layer can enter or leave.
- **BCC outages do not count.** If more than 2 minutes pass between two samples of a layer (BCC was stopped or stalled), the runs restart from zero; the state itself is kept. One sample after an outage is one observation, not a verdict.
- The defaults (sample every `--health-interval`, 10 s) are fixed in code for now; they are returned by `GET /api/health` (`policy`).

## The node's overall state

Derived from the layers, never stored separately and never taken from one layer alone:

- **DOWN** only when L0 or L1 is DOWN. **RECOVERING** when L0 or L1 is RECOVERING and none is DOWN.
- **DEGRADED** when any other layer (L2, L4, A) is DEGRADED, DOWN or RECOVERING, or any layer is DEGRADED.
- **UP** only when L0 and L1 are both UP and nothing is degraded, down or recovering. Layers with no evidence do not block UP and are not counted as healthy; a node whose L0 or L1 has no proven state is **UNKNOWN**, not UP.
- **UNKNOWN** when no layer has evidence.

So healthy sessions (L2) cannot make a silent process (L0) look healthy, and a good probe (L1) cannot hide a failing route (L4).

## Where it is visible

- `GET /api/health` (admin, read-only): per node the overall state with the reasons, every layer with its state, since when and the last evidence, the configuration conformity, and the last 10 transitions; `?node=ID&all=1` returns the full history (the last 200 transitions are kept).
- The dashboard card **Layered health**.
- Audit log: every transition is an entry `health.transition` with target `<node>/<layer>`, outcome `failure` for a move to DEGRADED or DOWN, and details `layer`, `from`, `to`, `node_from`, `node_to`, `reason`, `evidence`.
- State: the records are part of the BCC state database (schema migration 3, table `node_health`) and of its encrypted backups.

## Alerts follow confirmed health

```
RAW OBSERVATIONS -> LAYER SAMPLING -> HYSTERESIS -> EFFECTIVE HEALTH -> alerts, webhook, API, dashboard
```

The alert engine and the webhook no longer read an instantaneous signal. They follow the effective state of a layer:

| Layer state | Alert |
|---|---|
| DEGRADED (confirmed) | opened as `warning` |
| DOWN (confirmed) | opened as `critical`, or escalated from `warning` with one more event |
| RECOVERING, UNKNOWN | held as it is: nothing new, nothing resolved |
| UP | resolved |

Mapping: L0 -> `telemetry_stale`, L2 -> `handshake_error_rate`, L4 -> `route_down` (one alert per route whose probe is down while L4 is confirmed bad; an open route alert stays until L4 is UP). NOT_ASSESSED layers have no alert, and NONE (no evidence) can neither open nor close one. With the default policy and a 10 s sample interval, a `warning` follows at the second consecutive bad sample (about 20 s) and `critical` at the fifth over at least 30 s; a recovery closes after 3 OK samples from DEGRADED, or after 5 OK over at least 30 s from DOWN.

- A single failed probe, a lost packet or a flapping signal opens nothing and cannot cause an alert storm.
- The webhook payload gains `severity`, `health` (the layer state behind it) and `evidence` (the raw observation, kept only as evidence). `status` stays `firing` or `resolved`; an escalation is another `firing` event with `severity: critical`. A severity is never lowered while an alert is open.
- Every alert transition is also an audit entry (`alert.firing`, `alert.escalated`, `alert.resolved`) with the type, route, severity, health state and evidence.
- Alerts are persisted with the state, so a restart neither repeats an open alert nor forgets it. Delivery is at-least-once: if a webhook call fails, nothing is recorded for that alert and it is retried on the next evaluation. An alert opened by an older build (no severity) is adopted without a new notification.
- The health sampler (`ProbeOnce`) is the only writer of layer state; the alert engine only reads it, so evaluating alerts more often cannot count a sample twice. The instantaneous dashboard status (`/api/monitoring`) is unchanged.

## What is not changed

BCC has no alert for a node that is merely unreachable (L1 DOWN) yet; that is a new alert type and not part of this alignment. L3, L5 and L6 stay NOT_ASSESSED until BCC receives a signal for them.
