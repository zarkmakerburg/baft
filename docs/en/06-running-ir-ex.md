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

1. EX: `sudo bash install.sh --role ex --public-address HOST` builds the binaries, creates the Noise key and the outer TLS PKI (`baft-pair pki`, no OpenSSL needed), and prints a one-time `BAFTPAIR1:` code. It then waits for the IR's reply (or prints the `baft-pair ex-accept` command to run later when `BAFT_NONINTERACTIVE=1`).
2. IR: `sudo bash install.sh --role ir --pairing-code BAFTPAIR1:...` runs `baft-pair ir-apply --config-out`, which writes `/etc/baft/baft.yaml` (a Noise dialer pinned to the EX key, no client certificate), starts the service, and prints a `BAFTREPLY1:` code.
3. EX: paste the reply. `baft-pair ex-accept` checks it with an HMAC keyed by the pairing code's one-time PSK (a reply from anyone without the code is rejected), writes the listener config pinned to the IR key, deletes the PSK, and the service starts.

Local clients then connect to `BAFT_ROUTE_LISTEN` on the IR (default `127.0.0.1:1443`); the EX forwards to `BAFT_TARGET` (default `127.0.0.1:2443`, must be a fixed IP). Until the EX accepts the reply the IR dialer exits and systemd restarts it every 2 s.

`tests/e2e/pair_and_run.sh` runs this pairing with the real binaries and pushes data through; `tests/e2e/install_two_roles.sh` runs `install.sh` itself for both roles on one host. CI runs both.

## Operating an installed node

Three read-only commands; none of them changes the host.

```bash
sudo baft status            # version, installed release, role and peer, routes, service state, flows, recovery counters
sudo baft doctor            # checks with OK / INFO / WARN / FAIL and a hint for each problem; exit 1 on any FAIL
sudo baft logs -n 200 -f    # journalctl for the service
```

All three take `--service` (default `baft`); `status` and `doctor` also take `--file` (default `/etc/baft/baft.yaml`), `--release-state` (default `/opt/baft/release-state.json`) and `--json`.

`doctor` checks: the config and revocation file load; private keys are owner-only; the service is active (WARN if it has restarted); the binary matches the installed signed release (WARN for a source install); the metrics endpoint answers and reports no conservation invariant violation; the IR reaches its EX and its local route listens, or the EX listener accepts and its route targets answer. It also reads (never writes) a few network settings — congestion control, default qdisc, socket buffer limits — and prints the `sysctl` it would recommend as INFO. Automatic tuning is P2.
