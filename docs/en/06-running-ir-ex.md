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
