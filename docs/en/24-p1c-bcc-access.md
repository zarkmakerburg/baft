# 24 — P1-C (part 1): BCC web access

Launch-1 step P1-C from [22-launch-1-roadmap.md](22-launch-1-roadmap.md). This part covers how an operator reaches the BCC dashboard. Moving BCC state from a JSON file to versioned SQLite is the second part.

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
