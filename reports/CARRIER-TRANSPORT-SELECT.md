# Config-selectable carrier transport (WebSocket wiring)

This wires the WebSocket carrier (`internal/carrier/ws`, added in #93) into the
node so it can actually be used, selected by `transport.primary` in the config.
It is the keystone of the Cloudflare orange-cloud path from
`reports/CARRIER-CAMOUFLAGE.md`.

## What changed

- `config`: `transport.primary` now accepts `"h2"` (default) or `"ws"`. `ws`
  requires Noise — behind Cloudflare the outer TLS is terminated at the edge, so
  authentication rests entirely on the inner Noise IK handshake.
- `node`: the three carrier-open sites (initial dial, recovery replacement,
  listener) are unified behind one transport-agnostic seam:
  - `dialCarrier` opens one physical carrier, branching h2/ws, and returns the
    session `Carrier` plus a `closeFn`. `dialerShard` and `openedRuntimeCarrier`
    now hold only that `closeFn`, so recovery/ECRL is transport-agnostic.
  - the listener builds the h2 or ws Noise handler and sets ALPN accordingly
    (`h2,http/1.1` vs `http/1.1` only). For ws it also disables the automatic
    HTTP/2 (`TLSNextProto` emptied) so the WebSocket upgrade can hijack the
    HTTP/1.1 connection.
  - the ws dial offers `http/1.1` ALPN and sends a browser-like `User-Agent`
    and `Origin` on the upgrade.
- `baft-pair`: a `--transport {h2|ws}` flag so configs generate with the chosen
  transport through the normal pairing flow.

The session, recovery (ECRL), flow-control and Noise layers are unchanged; only
the physical byte transport is swapped.

## Verified

- `tests/e2e/ws_carrier.sh` (wired into the `e2e-binaries` CI job): pairs a real
  EX/IR with `--transport ws`, runs both nodes, pushes 1 KiB / 64 KiB / 512 KiB
  byte-exact through the WebSocket carrier (512 KiB exercises many 32 KiB WS
  frames and receive-window growth), and verifies a non-upgrade probe to the
  carrier path still gets the ordinary cover page.
- `internal/config` tests: `ws` requires Noise; unknown transports rejected.
- `go test -race ./internal/node ./internal/config ./internal/carrier/...` green.
- h2 behavior byte-for-byte unchanged (default path untouched).

## Risk assessment

| Risk | Severity | Mitigation |
|---|---|---|
| Refactor breaks the h2 recovery/ECRL path | High | `closeFn` only replaces lifecycle fields that were used solely for close; `o.carrier` (the functional part) is unchanged; full step57/stagec/ecrl/COR-01 gates before merge |
| ws listener accidentally serves h2 (upgrade fails) | Medium | ALPN set to `http/1.1` and `TLSNextProto` emptied; e2e asserts real WS traffic flows |
| ws without Noise (no auth behind CF) | High | config rejects `ws` without Noise |
| Probe distinguishes the ws endpoint | Low–Medium | non-upgrade requests get the cover page (asserted in e2e); Reality fall-through is a later mission |

## Scope / deferred

- uTLS (`internal/carrier/utlsdial`, #94) is not yet applied to this dial; the ws
  path still uses Go's `crypto/tls` ClientHello. Wiring uTLS under the ws (and
  h2) dialers is the next step.
- Multi-carrier **failover** between h2/ws over ECRL, and the Reality-style
  listener fall-through, remain their own missions.

## Deploying behind Cloudflare

See `docs/en/37-cloudflare-websocket.md`.
