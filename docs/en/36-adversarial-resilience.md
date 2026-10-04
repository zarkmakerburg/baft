# 36 — Adversarial resilience soak

A periodic red-team soak that attacks a real BAFT EX/IR pair and asserts the
nodes survive, stay bounded, and recover. It models two adversary roles — a
censor / DPI box doing active probes, and an attacker flooding or exhausting the
node, including DDoS-style load — to surface security and resilience gaps before
they reach production.

**Scope.** Everything runs against BAFT nodes started on loopback by the
harness. Nothing targets any third-party host, and no real external addresses or
packet captures enter the repository. This is authorized testing of the
project's own software.

## Run it

```
tests/adversary/run.sh OUT_DIR
```

- `ADV_PROFILE=bounded` (default) is sized for a 2-CPU GitHub runner and runs
  daily in `adversary-soak.yml`.
- `ADV_PROFILE=heavy` raises volume for a dedicated self-hosted box (real
  NIC-level load). Individual knobs override either profile: `SECONDS_PER`,
  `WORKERS`, `CONNS`, `SLOWLORIS_CONNS`, `RSS_LIMIT_MB`, `FD_LIMIT`, `ADV_NOISE`.

The harness builds `baft` and `baft-pair`, pairs an EX and IR exactly as
`install.sh` does (Noise + H2/mTLS by default), runs each scenario from
`tests/adversary/attack.py` against the node, and after each one asserts:

1. both nodes are still running;
2. resident memory and open fds are within their limits (no leak, no fd
   exhaustion);
3. a real transfer through IR → EX → target still succeeds (recovering within a
   few seconds is allowed and recorded).

A non-zero exit means at least one scenario left the node crashed, leaking, or
unable to carry traffic.

## Scenarios

| Scenario | Adversary role | What it does | What it proves |
|---|---|---|---|
| `connflood` | DDoS | open/close carrier connections as fast as possible | accept churn does not crash or leak |
| `garbage` | DDoS / attacker | flood the carrier with random bytes that never form a valid handshake | junk is rejected cheaply, memory bounded |
| `slowloris` | attacker | hold many connections open, trickling one byte at long intervals | slow clients cannot exhaust handshake slots (bounded pending handshakes, read timeout) |
| `probe` | censor / DPI | active probes to the carrier: HTTP GET, random bytes, a BAFT-header-shaped prefix, and a replayed identical probe | the carrier exposes no stateful distinguisher (identical input → identical response); all fingerprints are recorded as evidence |
| `manyflows` | attacker | open many application connections through IR at once | per-flow slot and memory bounds hold; fds return after close |

## Intrusion scenarios (`tests/adversary/intrude.sh`)

These attack BAFT's security boundaries directly. Each passes when the boundary
**holds** — the intrusion is rejected, the node stays up, and legitimate traffic
is unaffected.

| Intrusion | Boundary | Proves |
|---|---|---|
| `pairing_replay` | authentication | a one-time pairing reply, replayed after it was consumed, is rejected (no second session from a captured code) |
| `rogue_key` | authentication | a rogue IR whose Noise static key is not the one EX pinned at pairing cannot establish a carrier; EX stays up and the legitimate IR keeps carrying traffic |
| `onpath_tamper` | integrity | an on-path attacker that flips one ciphertext byte is caught by the AEAD (`bad record MAC`); the carrier fails closed and **no corrupted byte reaches the target** (verified by SHA-256); EX does not crash |

The IR dialer exiting on a tampered carrier is the designed fail-closed
behaviour (systemd restarts it in production), not a bypass; the test treats a
node *crash* or any corrupted delivery as the only failures.

Deep active-probe indistinguishability (an unauthenticated authentication
attempt is answered like an ordinary HTTP request, success withheld until
message one authenticates) is asserted by the Go unit tests in
`internal/carrier/h2` (`TestNoiseHTMLFallbackAndAuthenticatedHTTP2`,
`TestNoiseSlowProbeBounded`). The soak complements them with whole-node
behaviour under sustained load.

## When a gap is found

A failing scenario is a real finding. The run's artifact (`summary.md`,
`results.jsonl`, `provenance.txt`) has the repro and the measured numbers. A
clearly-scoped defensive fix (for example a missing bound) is made on a branch
with the full gates; anything larger is reported first.

## Not covered yet

Real NIC-level DDoS, netem storms, and amplification live in the `heavy`
profile for a self-hosted runner. Fuzzing of the wire decoder is a separate,
always-on test (`FuzzDecode` in `internal/protocol`).
