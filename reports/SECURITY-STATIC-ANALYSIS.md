# Security static-analysis triage

Zero-day hunting pass with `gosec` over the whole tree (go vet and govulncheck
already run in CI). `staticcheck` could not parse the go1.27 standard library at
the time of writing; it returns to the workflow once it supports go1.27.

All 14 high-severity gosec findings were reviewed. **None is exploitable.** Each
is a bounded conversion, a guarded path, or a literal that is not a secret. The
`static-analysis` workflow excludes these four rule classes (with this file as
the record) and keeps every other rule blocking, so a *new* high-severity
finding fails CI.

## G115 — integer overflow conversion (bounded before conversion)

| Site | Why safe |
|---|---|
| `protocol/frame.go:87` | `total = HeaderSize + len(payload)`; `validateFrame` rejects `len(payload) > MaxPayloadSize` (65536) first, so `total` fits uint32 |
| `securityinternal/security.go:352` | `writeHandshakeFrame` rejects `len(msg) > maxHandshakeFrameSize` (< 65536) before the uint16 write; the read side bounds `n` the same way before allocating |
| `securityinternal/pairing.go:116` | MAC domain-separation length of our own short identity/key strings |
| `recordshape/morphing.go:194,202,203,231,246` | lengths bounded by the record/frame caps checked upstream |
| `session/session.go` (×9), `session/conservation.go:61,62`, `session/recovery_runtime.go:1285` | offsets/reservations bounded by the per-flow window and allocator caps; the Flow receive-path fuzzer (`FuzzFlowReceivePath`) exercises these with hostile sequences and asserts no overflow |
| `bcc/store.go:473,474,476,604`, `clustersync/worker_config.go:155`, `node/node.go:1047` | counters/sizes bounded by prior validation |

G115 is excluded as a class because these bounded conversions are pervasive and
low-signal; they are tracked for incremental `#nosec` annotation, not treated as
vulnerabilities.

## G703 — path traversal (guarded or operator-local)

- `bcc/emergency_verify.go` — the backup member name is rejected if it contains
  `/`, `\`, `.` or `..` *before* `filepath.Join` (see the check above the join);
  no attacker-controlled component reaches the path.
- `bcc/restore_preview.go` — reads the operator's own current state file path,
  not attacker input.

## G101 — hardcoded credentials (false positive)

- `node/node.go:559` — `"BAFT_AGENT_TOKEN"` is the *name* of an environment
  variable, not a secret; the token is read from the environment.

## G122 — filesystem TOCTOU (accepted)

- `tunnelnode/tunnelnode.go:853` — `Lchown` inside the installer's own
  `filepath.Walk` over directories it just created while running as root.

## What is actively hunting for the unknown

- Coverage-guided fuzzing (`fuzz.yml`, daily) of every attacker-reachable
  parser — wire frames, control payloads, **pre-auth** pairing codes and
  replies, config — and the Flow receive-path state machine, all asserting no
  panic / out-of-bounds / broken invariant.
- `govulncheck` (weekly) for known advisories in linked code.
- The adversary + intrusion soak (`adversary-soak.yml`, daily) for whole-node
  behaviour under attack.
