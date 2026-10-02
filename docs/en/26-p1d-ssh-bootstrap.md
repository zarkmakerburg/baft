# 26 — P1-D: server inventory and SSH bootstrap

Adds a server to BCC and installs `baft-agent` on it over SSH, so an operator never logs in to a new server by hand. Builds on [25-p1d-agent.md](25-p1d-agent.md) (signed jobs, `baft-agent`) and [23-p1a-signed-releases.md](23-p1a-signed-releases.md) (release verification).

## Enable it

```
baft-bcc ... --install-script /opt/baft/install.sh --public-url https://bcc.example.com
```

`--public-url` is how agents reach BCC (https; `http://` only with `--allow-insecure-http`). Without both flags the endpoints answer `501`.

## Flow

1. **Read the host key** (nothing secret is sent):

   `POST /api/bootstrap/hostkey` `{"host":"203.0.113.7","port":22}` → `{"fingerprint":"SHA256:…","key_type":"ssh-ed25519"}`

   The operator compares the fingerprint with the one the hosting provider shows (console / `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`).

2. **Bootstrap** with the confirmed fingerprint:

   `POST /api/bootstrap`
   ```json
   {"node_id":"ex-1","alias":"ex","host":"203.0.113.7","port":22,"user":"root",
    "host_key_fingerprint":"SHA256:…","password":"…"}
   ```
   Use `private_key` (PEM, optional `passphrase`) instead of `password` when you prefer keys. `user` defaults to `root`; a non-root user needs passwordless `sudo`.

BCC then creates the node, generates a fresh agent token, connects with the pinned host key, and runs `install.sh --agent-only` on the server. The agent downloads the signed release, verifies it against the pinned root key (the same verifier as everywhere else) and starts `baft-agent.service`. Tunnels are added afterwards from BCC; this step installs no tunnel.

## Security rules

| Rule | How |
|---|---|
| No trust on first use | Bootstrap refuses without a `SHA256:` fingerprint, and aborts before authentication if the server presents another key. |
| Credentials only in memory | Password / key / passphrase exist only for the request. They are not written to state, audit log, backups or logs, and they are redacted from any error or output. |
| Nothing secret on the command line | The remote command is `bash -s`; the agent token and BCC job key travel on the script's stdin as exported variables. |
| No clear-text credentials | The request is refused unless it arrives over TLS or from a loopback peer (reverse proxy / tunnel). |
| Audited | `bootstrap.hostkey` and `node.bootstrap` entries record host, port, user, auth method and fingerprint — never the credential. |
| One at a time | A second bootstrap while one runs gets `409`. |
| Retry-safe | A retry issues a new agent token and invalidates the previous one. |

The install script that runs on the server is the file given by `--install-script`, i.e. the one on the BCC host; its release verification is pinned to the release root key.

## Tests

`internal/sshboot` runs against an in-process SSH server: fingerprint scan; wrong host key refused before any command; secrets only on stdin, never in the command or output; sudo for non-root; failed install and wrong password leak nothing; key auth; request validation. `internal/bcc/bootstrap_test.go`: the API installs and registers the node with a token BCC accepts; credentials never reach the audit log or state file; clear-text, unauthenticated, unconfigured and no-job-key requests are refused; retries rotate the token.

## Not yet

Traffic-level checks beyond "the agent runs a signed job". The dashboard has the form (Add a server over SSH); the real-sshd run is `tests/e2e/ssh_bootstrap.sh`.
