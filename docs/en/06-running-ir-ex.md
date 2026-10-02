# 06 — Building and running IR/EX

This page describes the current executable path, not a finished production deployment system.

Build:

```bash
git clone https://github.com/zarkmakerburg/baft.git
cd baft
go build -o baft ./cmd/baft
./baft version
```

Prepare validated IR/EX configuration plus CA/certificate/private-key files. Private-key permissions must not allow group/other access.

Validate:

```bash
./baft config validate --file /etc/baft/ex.yaml
./baft config validate --file /etc/baft/ir.yaml
```

Start EX first, then IR:

```bash
./baft run --file /etc/baft/ex.yaml
./baft run --file /etc/baft/ir.yaml
```

The connection sequence is: TCP dial → TLS 1.3 mTLS → H2 Carrier → HELLO/HELLO_ACK → two-sided READY → OPEN → fixed Route target dial → OPEN_OK/WINDOW → DATA/ACK/WINDOW → FIN/FIN_ACK.

SIGINT/SIGTERM are converted to context cancellation so listeners, carriers, and flows can shut down.

Common failures include strict-config rejection, hostname mismatch, peer/Route authorization failure, missing Route, unreachable fixed target, and insecure private-key permissions.

Final systemd hardening, installer/package flow, admin transactions, certificate-rotation operations, support bundles, and real-path pilot procedures are later-stage work.

## Installed path: pairing writes both configs

`install.sh` now provisions a runnable pair without hand-written YAML:

1. EX: `sudo bash install.sh --role ex --public-address HOST` installs the binaries (see "Where the binaries come from" below), creates the Noise key and the outer TLS PKI (`baft-pair pki`, no OpenSSL needed), and prints a one-time `BAFTPAIR1:` code. It then waits for the IR's reply (or prints the `baft-pair ex-accept` command to run later when `BAFT_NONINTERACTIVE=1`).
2. IR: `sudo bash install.sh --role ir --pairing-code BAFTPAIR1:...` runs `baft-pair ir-apply --config-out`, which writes `/etc/baft/baft.yaml` (a Noise dialer pinned to the EX key, no client certificate), starts the service, and prints a `BAFTREPLY1:` code.
3. EX: paste the reply. `baft-pair ex-accept` checks it with an HMAC keyed by the pairing code's one-time PSK (a reply from anyone without the code is rejected), writes the listener config pinned to the IR key, deletes the PSK, and the service starts.

Local clients then connect to `BAFT_ROUTE_LISTEN` on the IR (default `127.0.0.1:1443`); the EX forwards to `BAFT_TARGET` (default `127.0.0.1:2443`, must be a fixed IP). Until the EX accepts the reply the IR dialer exits and systemd restarts it every 2 s.

`tests/e2e/pair_and_run.sh` runs this pairing with the real binaries and pushes data through; `tests/e2e/install_two_roles.sh` runs `install.sh` itself for both roles on one host. CI runs it twice: from source (`e2e-install`) and from a signed release built with throwaway keys (`e2e-install-release`).

### Where the binaries come from

By default (`BAFT_INSTALL_FROM=release`) the installer needs only `curl`, `openssl` and `python3`; there is no Go, git or compiler on the server. It:

1. downloads `manifest.json`, `release-key.cert.json`, `SHA256SUMS` and this architecture's `baft` and `baft-pair` from the latest GitHub release (`--version vX.Y.Z` pins one; `BAFT_RELEASE_URL` overrides the location), and the current revocation list (`BAFT_REVOCATIONS_URL`, default `release/keys/revocations.json` on `main`);
2. verifies them before running anything downloaded, with the rules of [23-p1a-signed-releases.md](23-p1a-signed-releases.md): root key pinned in `install.sh` (`BAFT_PINNED_ROOT_PUB`), certificate, mandatory unexpired revocation list, manifest signature, `SHA256SUMS` and each binary's hash. Only the downloaded artifacts are required; any other file is rejected;
3. refuses a release older than the installed one (unless `--allow-downgrade`) and a version already installed from a different commit, using the root-owned `$BAFT_PREFIX/release-state.json` (default `/opt/baft/release-state.json`);
4. installs the binaries and only then records the release in that state file.

If verification fails nothing is installed. Until the owner's key ceremony pins the root key, release installs stop with a clear error; `--from-source` (`BAFT_INSTALL_FROM=source`) keeps the old clone-and-build path for development. `tests/installer` checks the installer's verifier against releases signed by `internal/release`, including tampering, revocation, replayed lists, downgrade and re-tag.

## Operating an installed node

Three read-only commands; none of them changes the host.

```bash
sudo baft status            # version, installed release, role and peer, routes, service state, flows, recovery counters
sudo baft doctor            # verdict, layers, and Problem / Evidence / Impact / Fix for each finding; exit 1 on FAIL
sudo baft logs -n 200 -f    # journalctl for the service
```

All three take `--service` (default `baft`); `status` and `doctor` also take `--file` (default `/etc/baft/baft.yaml`), `--release-state` (default `/opt/baft/release-state.json`) and `--json`.

`doctor` checks: the config and revocation file load; private keys are owner-only; the service is active (WARN if it has restarted); the binary matches the installed signed release (WARN for a source install); the metrics endpoint answers and reports no conservation invariant violation; the IR reaches its EX and its local route listens, or the EX listener accepts and its route targets answer. It also reads (never writes) a few network settings — congestion control, default qdisc, socket buffer limits — and prints the `sysctl` it would recommend as INFO. Automatic tuning is P2.

### Doctor report

`doctor` opens with a verdict (`HEALTHY`, `DEGRADED` when only warnings exist, `FAILING` on any FAIL), the most likely failure domain with a confidence, and one next action. One failing domain is reported with confidence `high`; several failing domains report the lowest one (configuration/host first, then L0 upwards) as the best guess for the root cause with confidence `medium`. Then the layers:

| Layer | Meaning | How doctor knows |
|---|---|---|
| L0 | process alive | service state, metrics endpoint |
| L1 | carrier path | TCP reachability of the peer / the local listener (not authentication) |
| L2 | peer authenticated | **not assessed** (doctor opens no authenticated session; use BCC health and telemetry) |
| L3 | session correctness | conservation invariant counter from the metrics |
| L4 | route available | the route's local listener |
| L5 | target reachable | the fixed target of an inbound route |
| L6 | application traffic | **not assessed** (needs an end-to-end probe) |

A layer nothing observed reads `NOT_ASSESSED`, never `PASS`. Every non-OK finding then prints, in this order, **problem**, **evidence**, **impact** and **fix**. `--json` carries the same (`summary`, and per check `layer`, `domain`, `problem`, `impact`, `fix_command`, `fix_safety`; `detail` is the evidence and `hint` the fix).

`baft doctor --preview-fixes` lists the commands that would fix findings, each marked `SAFE` (does not interrupt traffic or access and is easily undone; `chmod 0600` on a key counts only when the key is already owned by the user the service runs as, otherwise it is `REVIEW` and the finding says to fix the owner first, because a differently owned key would lock the service out on restart) or `REVIEW` (can change behaviour for traffic or other software, e.g. `sysctl -w`). Every config-derived path in a suggested command is shell-quoted. **Doctor never runs a fix**; the preview exists so an operator can review the exact command first.

## Support bundle

```bash
sudo baft support-bundle [--file /etc/baft/baft.yaml] [--service baft] [--out report.tar.gz] [-n 500]
```

Writes one owner-only (`0600`) archive for a bug report; it refuses to overwrite an existing file and only reads the host. Contents: `status.json`, `doctor.json`, the configuration, the installer's release state, the service unit and `systemctl show` summary, the last `-n` journal lines, `system.txt` (`uname`, `/etc/os-release`) and a `manifest.json` listing each file's size and SHA-256, the redaction counts and anything that could not be collected.

Never collected: Noise or TLS private-key files, pairing and PSK files, agent or admin tokens, user payload. Every text file is redacted before it is stored: PEM private keys, `BAFTPAIR1:` / `BAFTREPLY1:` codes, `Bearer` tokens and `token` / `password` / `passphrase` / `secret` / `private_key` assignments. Addresses, hostnames and node identities are **not** redacted, so review the bundle before sharing it. CI checks on a running node that no byte of its key files appears in the bundle (`tests/e2e/launch1.sh`).
