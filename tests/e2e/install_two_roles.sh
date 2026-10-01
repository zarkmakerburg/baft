#!/usr/bin/env bash
# Runs install.sh twice on one Debian/Ubuntu host with systemd -- once as EX,
# once as IR, under separate service names and directories -- pairs them
# through the installer's own prompts, then pushes traffic through the
# installed services and compares SHA-256. It installs packages, creates the
# baft user and systemd units, so it is meant for a throwaway CI runner.
#
#   sudo BAFT_INSTALL_FROM=source BAFT_REPO_URL=$PWD BAFT_REF=$(git rev-parse HEAD) tests/e2e/install_two_roles.sh
#
# With BAFT_INSTALL_FROM=release (and BAFT_RELEASE_URL, BAFT_ROOT_PUB,
# BAFT_REVOCATIONS_URL, e.g. from scripts/release/local_release.sh) it
# installs the signed release instead and checks nothing was built.
set -Eeuo pipefail
cd "$(dirname "$0")/../.."
ROOT="$PWD"
[[ "$EUID" -eq 0 ]] || { echo "run as root"; exit 1; }
export BAFT_INSTALL_FROM="${BAFT_INSTALL_FROM:-source}"
if [[ "$BAFT_INSTALL_FROM" == "source" ]]; then
  export BAFT_REPO_URL="${BAFT_REPO_URL:-$ROOT}"
  export BAFT_REF="${BAFT_REF:-$(git rev-parse HEAD)}"
fi
WORK="$(mktemp -d)"
TARGET_PID=""; EX_PID=""
log() { printf '[install-e2e] %s\n' "$*"; }
cleanup() {
  local rc=$?
  [[ -n "$TARGET_PID" ]] && kill "$TARGET_PID" 2>/dev/null
  [[ -n "$EX_PID" ]] && kill "$EX_PID" 2>/dev/null
  if [[ $rc -ne 0 ]]; then
    diag="$WORK/diag"
    {
      for f in "$WORK"/*.out "$WORK"/*.err; do [[ -f "$f" ]] && { echo "== $f"; tail -n 40 "$f"; }; done
      for s in baft-ex baft-ir; do echo "== journal $s"; journalctl -u "$s" -n 40 --no-pager 2>/dev/null || true; done
    } >"$diag" 2>&1
    cat "$diag"
    # Job logs are not always retrievable; annotations are. One annotation
    # per section keeps the failure readable from the checks API.
    if [[ "${GITHUB_ACTIONS:-}" == "true" ]]; then
      awk '/^== /{if (msg != "") print msg; msg=$0; next} {msg = msg "\\n" $0} END{if (msg != "") print msg}' "$diag" |
        tail -n 8 | while IFS= read -r section; do
          section="${section//%/%25}"
          printf '::error title=install e2e diagnostics::%s\n' "${section//\\n/%0A}"
        done
    fi
  fi
  rm -rf "$WORK"
  exit $rc
}
trap cleanup EXIT

python3 tests/e2e/echo.py serve 2443 >"$WORK/target.out" 2>&1 & TARGET_PID=$!

log "EX install (waits for the IR reply on stdin)"
mkfifo "$WORK/reply"
exec 3<>"$WORK/reply"
env BAFT_SERVICE=baft-ex BAFT_CONFIG_DIR=/etc/baft-ex BAFT_STATE_DIR=/var/lib/baft-ex \
  BAFT_PREFIX=/opt/baft-ex BAFT_PORT=8443 BAFT_METRICS_LISTEN=127.0.0.1:9191 \
  BAFT_TARGET=127.0.0.1:2443 \
  bash install.sh --role ex --public-address 127.0.0.1 <"$WORK/reply" >"$WORK/ex.out" 2>"$WORK/ex.err" &
EX_PID=$!
for _ in $(seq 1 1800); do
  grep -q '^BAFTPAIR1:' "$WORK/ex.out" 2>/dev/null && break
  kill -0 "$EX_PID" 2>/dev/null || { echo "EX installer exited early"; exit 1; }
  sleep 1
done
CODE="$(grep -m1 '^BAFTPAIR1:' "$WORK/ex.out")" || { echo "no pairing code"; exit 1; }

log "IR install with the pairing code"
env BAFT_SERVICE=baft-ir BAFT_CONFIG_DIR=/etc/baft-ir BAFT_STATE_DIR=/var/lib/baft-ir \
  BAFT_PREFIX=/opt/baft-ir BAFT_METRICS_LISTEN=127.0.0.1:9192 BAFT_ROUTE_LISTEN=127.0.0.1:1443 \
  BAFT_NONINTERACTIVE=1 BAFT_PAIRING_CODE="$CODE" \
  bash install.sh --role ir >"$WORK/ir.out" 2>"$WORK/ir.err"
REPLY_CODE="$(grep -m1 '^BAFTREPLY1:' "$WORK/ir.out")" || { echo "no reply code"; exit 1; }

log "hand the reply to the EX installer"
printf '%s\n' "$REPLY_CODE" >&3
wait "$EX_PID"; EX_PID=""
exec 3>&-

[[ ! -e /var/lib/baft-ex/pairing.psk && ! -e /var/lib/baft-ex/pairing.ex.json ]] || { echo "one-time PSK left behind"; exit 1; }
for f in /etc/baft-ex/baft.yaml /etc/baft-ir/baft.yaml; do
  [[ "$(stat -c '%U:%G %a' "$f")" == "root:baft 640" ]] || { stat "$f"; exit 1; }
done
[[ "$(stat -c '%U %a' /etc/baft-ex/pki/ca.key)" == "root 600" ]] || { echo "CA key not root-only"; exit 1; }
if [[ "$BAFT_INSTALL_FROM" == "release" ]]; then
  for p in /opt/baft-ex /opt/baft-ir; do
    [[ ! -e "$p/src" ]] || { echo "release install built from source in $p"; exit 1; }
    [[ "$(stat -c '%U %a' "$p/release-state.json")" == "root 644" ]] || { echo "no root-owned release state in $p"; exit 1; }
    grep -q '"version": "v' "$p/release-state.json" || { cat "$p/release-state.json"; exit 1; }
  done
  log "installed signed release: $(/usr/local/bin/baft version)"
fi

log "wait for both services and the IR route"
for _ in $(seq 1 60); do
  if systemctl is-active --quiet baft-ex && systemctl is-active --quiet baft-ir &&
     python3 -c "import socket;socket.create_connection(('127.0.0.1',1443),0.5)" 2>/dev/null; then
    break
  fi
  sleep 1
done
systemctl is-active baft-ex baft-ir

log "traffic through the installed services"
python3 tests/e2e/echo.py send 1443 "${E2E_MIB:-8}" "${E2E_CONNS:-4}"
systemctl is-active baft-ex baft-ir
log "PASS"
