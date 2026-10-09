# BCC V3 topology snapshot contract (preview only)

The standalone preview accepts a **locally selected** JSON file. It never sends the file to a server. This is an interim interface for validating UI rendering, **not** an authenticated BCC telemetry endpoint.

```json
{
  "schemaVersion": 1,
  "nodes": [
    {"id": "ir-demo", "name": "Tehran demo", "lat": 35.6892, "lon": 51.389},
    {"id": "de-demo", "name": "Frankfurt demo", "lat": 50.1109, "lon": 8.6821}
  ],
  "links": [
    {"id": "demo-link", "source": "ir-demo", "destination": "de-demo",
     "status": "unknown", "rttMs": null, "upBps": null, "downBps": null,
     "updatedAt": null}
  ]
}
```

Use coordinates from verified node inventory (not country centroid guesses), in WGS84 decimal degrees. Never include SSH credentials, agent tokens, private keys, host secrets, or other confidential inventory fields in browser-side snapshots. Node and link IDs must be unique.

Status is one of `healthy`, `degraded`, `down`, `unknown`. Only fresh telemetry (last 180 seconds, timestamp not more than 30 seconds in the future) can retain a non-unknown status. RTT is milliseconds; up/down are **bits per second**. Null means unavailable. Import enforces at most 100 nodes, 300 links and 512 KiB per file. For actual deployment, data must be authorized, redacted, validated on the server and delivered over an authenticated same-origin endpoint.

## Remaining acceptance gates
- Connect to verified BCC data structures and authenticated endpoints; do not guess the backend schema.
- Replace demo city coordinates with approved real node coordinates; ensure links track actual endpoints.
- Correct camera focus for antimeridian and multiple-node layouts; test browser interaction and accessibility.
- Implement per-link error, jitter, packet loss, last handshake and staleness explanations when backed by real signals.
- Import the owner-supplied original BAFT logo as an approved asset.
- No live production deployment or PR merge until review and tests.
