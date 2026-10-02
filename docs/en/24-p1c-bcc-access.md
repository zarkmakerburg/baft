# 24 — P1-C: BCC web access and SQLite state

Launch-1 step P1-C from [22-launch-1-roadmap.md](22-launch-1-roadmap.md): how an operator reaches the BCC dashboard, and BCC state in a versioned SQLite database (last section).

## Model

Three independent credentials, created and rotated together:

| Credential | Form | Stored as |
|---|---|---|
| Secret path | 32 random base62 characters: `https://bcc.example.com/<secret>/` | plain, in the access file (it is a locator, not a password) |
| Username | `op-` + 10 random characters | plain, in the access file |
| Password | 24 random base62 characters (about 143 bits) | PBKDF2-SHA256 hash only (600 000 iterations, random salt) |

The access file (default `<state-file>.access.json`) must be owner-only (mode 0600); BCC refuses to start otherwise.

The secret path is an extra layer, not authentication. Every request under it still needs a session; the root path and any other path answer 404.

## Console only

```bash
sudo baft-bcc access init        # first time: prints path, username and password once
sudo baft-bcc access regenerate  # replaces all three; old ones stop working; every session ends
sudo baft-bcc access show        # path and username (the password is not stored)
```

There is no web password recovery and no web endpoint that changes access. Regeneration needs write access to the access file on the BCC host. A running BCC notices the new file within a second: the old path returns 404 and all sessions are dropped, without a restart.

## Sessions

- Login at `/<secret>/` (POST `/<secret>/login`). A wrong username costs the same as a wrong password; both are audited (`access.login`) and count toward the per-IP block (5 failures, then 5 minutes blocked).
- Session token: 32 random bytes in a cookie that is `HttpOnly`, `SameSite=Strict`, `Path=/<secret>/`, and `Secure` when BCC serves HTTPS. BCC keeps only its SHA-256, in memory (a restart logs everyone out).
- Idle timeout 30 minutes, absolute lifetime 12 hours, at most 256 sessions.
- State-changing requests must also send the session's CSRF token in `X-BAFT-CSRF`; the dashboard does this.
- The session counts only for requests routed through `/<secret>/api/...`. The bearer-token API at `/api/...` (automation) and the agent API are unchanged.
- Responses under the secret path carry `Cache-Control: no-store`, `X-Frame-Options: DENY` and `Referrer-Policy: no-referrer`. With secret-path access on, the per-IP rate limit applies to every path, so probing for the path is limited too.

## HTTPS

```bash
baft-bcc --listen 203.0.113.5:443 --tls-cert /etc/baft-bcc/fullchain.pem --tls-key /etc/baft-bcc/privkey.pem ...
```

- The certificate is re-read on the next handshake after the files change, so a renewal (for example by certbot) needs no restart; a broken renewal keeps the last good certificate.
- A non-loopback listen address without `--tls-cert/--tls-key` is refused unless `--allow-insecure-http` is given. On loopback (reached through an SSH tunnel or a local reverse proxy) plain HTTP is allowed.
- The non-loopback address must still be whitelisted in `BAFT_BCC_ALLOWED_LISTEN_IPS`, as before.

## Tests

`internal/bcc/access_test.go`: credentials and hash-only storage, file mode, the dashboard only under the secret path (root and near-miss paths 404), login, cookie flags, CSRF, the session not accepted outside the secret path, regeneration ending sessions and old credentials, logout, idle timeout, login blocking, audit entries. `cmd/baft-bcc/access_test.go`: the console commands, the loopback rule and certificate reload.

## State in SQLite

The BCC state file (`--state-file`, same path as before) is now a SQLite database (pure Go `modernc.org/sqlite`, so builds stay static and reproducible; only `baft-bcc` and `baft-bcc-audit-verify` link it).

- **Schema and migrations.** `schema_migrations` records the applied versions; migrations run in order inside transactions at open. A database from a newer build is refused, never downgraded. Version 1 has one table per collection (nodes, jobs, finance, rate history, ledger, telemetry, history, alerts, retired boot IDs, counters), each row holding the record's JSON, so later steps (server inventory, signed jobs) can move fields into columns with new migrations.
- **Writes.** Every save is one transaction (`journal_mode=DELETE`, `synchronous=FULL`), so a crash leaves the previous or the new state, never a mix. The append-only finance ledger writes only its new rows when the stored rows are a prefix (20 000-row ledger: 150 ms → 4.5 ms per save).
- **One file between transactions.** The connection is opened per operation and closed after it, so the encrypted backup restore keeps its staged, journaled whole-file swap: it builds the restored state as a new database beside the live one, verifies it, then swaps it in (or rolls it back after a crash).
- **Migration from JSON.** On the first start of this build an existing JSON state file is loaded, written to a new database, read back and compared, then renamed over the state path. The original JSON stays at `<state-file>.json.bak` (0600). Agent tokens, jobs, finance and telemetry cursors carry over unchanged.
- The file is created owner-only (0600).

Tests: `internal/bcc/statedb_test.go` (round trip of every collection, file mode, schema version, in-place JSON migration with `.json.bak`, legacy JSON without rate history, newer schema refused, corrupt database refused, ledger append vs rewrite exactness); the restore tests now also check that a failed restore leaves the database on disk unchanged and that a successful one survives a restart.
