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

IR_ENV=(BAFT_SERVICE=baft-ir BAFT_CONFIG_DIR=/etc/baft-ir BAFT_STATE_DIR=/var/lib/baft-ir
  BAFT_PREFIX=/opt/baft-ir BAFT_METRICS_LISTEN=127.0.0.1:9192 BAFT_ROUTE_LISTEN=127.0.0.1:1443)
if [[ "${BAFT_E2E_IR_VIA_SSH:-0}" == 1 ]]; then
  # One command on the EX installs and pairs the IR over SSH (a real sshd on
  # this host stands in for the IR server); nobody runs anything on the IR.
  command -v sshd >/dev/null || { apt-get update -qq && apt-get install -y -qq openssh-server >/dev/null; }
  mkdir -p /run/sshd
  ssh-keygen -q -t ed25519 -N '' -f "$WORK/hostkey"
  ssh-keygen -q -t ed25519 -N '' -f "$WORK/clientkey"
  cp "$WORK/clientkey.pub" "$WORK/authorized_keys"
  cat >"$WORK/sshd_config" <<CFG
Port 22022
ListenAddress 127.0.0.1
HostKey $WORK/hostkey
PidFile $WORK/sshd.pid
PermitRootLogin yes
PubkeyAuthentication yes
PasswordAuthentication no
KbdInteractiveAuthentication no
AuthorizedKeysFile $WORK/authorized_keys
StrictModes no
UsePAM no
CFG
  "$(command -v sshd || echo /usr/sbin/sshd)" -f "$WORK/sshd_config" -E "$WORK/sshd.err"
  for _ in $(seq 1 20); do (exec 3<>/dev/tcp/127.0.0.1/22022) 2>/dev/null && break; sleep 0.5; done
  FP="$(ssh-keygen -lf "$WORK/hostkey.pub" -E sha256 | awk '{print $2}')"

  log "EX install that sets up the IR over SSH"
  env BAFT_SERVICE=baft-ex BAFT_CONFIG_DIR=/etc/baft-ex BAFT_STATE_DIR=/var/lib/baft-ex \
    BAFT_PREFIX=/opt/baft-ex BAFT_PORT=8443 BAFT_METRICS_LISTEN=127.0.0.1:9191 \
    BAFT_TARGET=127.0.0.1:2443 BAFT_NONINTERACTIVE=1 BAFT_IR_ENV="${IR_ENV[*]}" \
    bash install.sh --role ex --public-address 127.0.0.1 \
      --ir-ssh root@127.0.0.1:22022 --ir-ssh-key "$WORK/clientkey" --ir-ssh-fingerprint "$FP" \
      </dev/null >"$WORK/ex.out" 2>"$WORK/ex.err"
  kill "$(cat "$WORK/sshd.pid")" 2>/dev/null || true
  grep -q 'BAFTPAIR1:\|BAFTREPLY1:' "$WORK/ex.out" "$WORK/ex.err" && { echo "a pairing secret was printed"; exit 1; }
else
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
fi

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

log "operator commands against the installed services"
for r in ex ir; do
  /usr/local/bin/baft status --file "/etc/baft-$r/baft.yaml" --service "baft-$r" --release-state "/opt/baft-$r/release-state.json"
  # doctor exits 1 on any FAIL; INFO/WARN (network tuning, source install) are fine here.
  /usr/local/bin/baft doctor --file "/etc/baft-$r/baft.yaml" --service "baft-$r" --release-state "/opt/baft-$r/release-state.json"
done

# Idempotency gate: RERUN != REINSTALL. Five reruns of each installer on the
# healthy install must change nothing: no config drift, no secret rotation, no
# service damage, no tunnel interruption, no ownership change. Release
# installs only (a source install would rebuild the binaries each time).
if [[ "$BAFT_INSTALL_FROM" == "release" ]]; then
  fingerprint() { # prefix-dir config-dir state-dir unit
    { sha256sum "$2"/baft.yaml "$2"/noise-key.json "$2"/pki/* "/etc/systemd/system/$4.service" "$1/release-state.json" /usr/local/bin/baft /usr/local/bin/baft-pair 2>/dev/null
      stat -c '%n %U:%G %a' "$2" "$2"/baft.yaml "$2"/noise-key.json "$2"/pki/* "$3" "/etc/systemd/system/$4.service" "$1/release-state.json" 2>/dev/null
      systemctl show -p MainPID -p NRestarts -p ActiveEnterTimestampMonotonic "$4"; } | sha256sum
  }
  fp_ex() { fingerprint /opt/baft-ex /etc/baft-ex /var/lib/baft-ex baft-ex; }
  fp_ir() { fingerprint /opt/baft-ir /etc/baft-ir /var/lib/baft-ir baft-ir; }
  rerun_ex() { env BAFT_SERVICE=baft-ex BAFT_CONFIG_DIR=/etc/baft-ex BAFT_STATE_DIR=/var/lib/baft-ex \
    BAFT_PREFIX=/opt/baft-ex BAFT_PORT=8443 BAFT_METRICS_LISTEN=127.0.0.1:9191 BAFT_TARGET=127.0.0.1:2443 \
    BAFT_NONINTERACTIVE=1 bash install.sh --role ex --public-address 127.0.0.1 "$@" </dev/null; }
  rerun_ir() { env BAFT_SERVICE=baft-ir BAFT_CONFIG_DIR=/etc/baft-ir BAFT_STATE_DIR=/var/lib/baft-ir \
    BAFT_PREFIX=/opt/baft-ir BAFT_METRICS_LISTEN=127.0.0.1:9192 BAFT_ROUTE_LISTEN=127.0.0.1:1443 \
    BAFT_NONINTERACTIVE=1 bash install.sh --role ir "$@" </dev/null; }
  A_EX="$(fp_ex)"; A_IR="$(fp_ir)"
  for role in ex ir; do
    plan="$(rerun_$role --plan --json 2>/dev/null)"
    python3 - "$plan" <<'PY' || { echo "healthy install plans changes: $plan"; exit 1; }
import json, sys
p = json.loads(sys.argv[1])
assert p["state"] in ("ALREADY_INSTALLED", "CURRENT", "UPGRADE_AVAILABLE"), p
assert p["changes"] == 0 or p["state"] == "UPGRADE_AVAILABLE", p
PY
  done
  for i in 1 2 3 4 5; do
    log "rerun $i/5 on the healthy install"
    rerun_ex >"$WORK/rerun-ex.out" 2>"$WORK/rerun-ex.err" || { cat "$WORK/rerun-ex.err"; exit 1; }
    rerun_ir >"$WORK/rerun-ir.out" 2>"$WORK/rerun-ir.err" || { cat "$WORK/rerun-ir.err"; exit 1; }
    grep -q 'CURRENT\|ALREADY_INSTALLED' "$WORK/rerun-ex.err" || { cat "$WORK/rerun-ex.err"; exit 1; }
    grep -q 'nothing was changed' "$WORK/rerun-ex.err" "$WORK/rerun-ir.err" || { cat "$WORK/rerun-ex.err"; exit 1; }
    if grep -q 'BAFTPAIR1:\|BAFTREPLY1:' "$WORK/rerun-ex.out" "$WORK/rerun-ir.out"; then echo "a rerun printed a pairing or reply code"; exit 1; fi
    [[ "$(fp_ex)" == "$A_EX" ]] || { echo "EX install changed on rerun $i"; exit 1; }
    [[ "$(fp_ir)" == "$A_IR" ]] || { echo "IR install changed on rerun $i"; exit 1; }
  done
  [[ ! -d /opt/baft-ex/backups && ! -d /opt/baft-ir/backups ]] || { echo "a no-op rerun made backups"; exit 1; }
  systemctl is-active baft-ex baft-ir
  python3 tests/e2e/echo.py send 1443 2 2
  [[ "$(fp_ex)" == "$A_EX" && "$(fp_ir)" == "$A_IR" ]] || { echo "traffic changed the install"; exit 1; }

  log "a service stopped by hand is repaired by a rerun, and nothing else changes"
  systemctl stop baft-ir
  rerun_ir --yes >"$WORK/repair.out" 2>"$WORK/repair.err" || { cat "$WORK/repair.err"; exit 1; }
  grep -q 'REPAIR_REQUIRED' "$WORK/repair.err" || { cat "$WORK/repair.err"; exit 1; }
  systemctl is-active baft-ir
  python3 tests/e2e/echo.py send 1443 2 2

  log "a plan that changes things is refused without --yes"
  systemctl stop baft-ir
  if rerun_ir >"$WORK/noyes.out" 2>"$WORK/noyes.err"; then echo "changing plan applied without --yes"; exit 1; fi
  systemctl is-active baft-ir && { echo "service started without --yes"; exit 1; } || true
  rerun_ir --yes >/dev/null 2>&1
  systemctl is-active baft-ir
  python3 tests/e2e/echo.py send 1443 2 2
  if [[ -n "${BAFT_E2E_UPGRADE_DIR:-}" ]]; then
    # BAFT_E2E_UPGRADE_DIR holds rel2 (newer), brokenv (version fails) and
    # brokenr (version works, the service dies), all signed by the same root.
    U="$BAFT_E2E_UPGRADE_DIR"
    run_up() { local d="$1"; shift; BAFT_RELEASE_URL="file://$d/dist" BAFT_REVOCATIONS_URL="file://$d/revocations.json" rerun_ex "$@"; }
    secrets() { sha256sum /etc/baft-ex/baft.yaml /etc/baft-ex/noise-key.json /etc/baft-ex/pki/* "/etc/systemd/system/baft-ex.service"; stat -c '%n %U:%G %a' /etc/baft-ex/baft.yaml /etc/baft-ex/noise-key.json /etc/baft-ex/pki/*; }
    S0="$(secrets)"
    OLDVER="$(/usr/local/bin/baft version)"
    state_version() { sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' /opt/baft-ex/release-state.json; }
    V1="$(state_version)"

    log "upgrade: a plan that changes the install is refused without --yes"
    if run_up "$U/rel2" >"$WORK/up.out" 2>"$WORK/up.err"; then echo "upgrade applied without --yes"; exit 1; fi
    [[ "$(/usr/local/bin/baft version)" == "$OLDVER" ]] || { echo "binary changed without --yes"; exit 1; }

    log "upgrade: --yes upgrades the binary, keeps every secret and config"
    PID0="$(systemctl show -p MainPID --value baft-ex)"
    run_up "$U/rel2" --yes >"$WORK/up.out" 2>"$WORK/up.err" || { cat "$WORK/up.err"; exit 1; }
    grep -q 'UPGRADE_AVAILABLE' "$WORK/up.err" || { cat "$WORK/up.err"; exit 1; }
    [[ "$(state_version)" != "$V1" ]] || { echo "release state did not move forward"; exit 1; }
    [[ "$(secrets)" == "$S0" ]] || { echo "secrets or config changed by the upgrade"; exit 1; }
    [[ "$(systemctl show -p MainPID --value baft-ex)" != "$PID0" ]] || { echo "service was not restarted onto the new binary"; exit 1; }
    systemctl is-active baft-ex baft-ir
    python3 tests/e2e/echo.py send 1443 2 2
    V2="$(state_version)"; BIN2="$(sha256sum /usr/local/bin/baft)"

    log "upgrade: the old release (the installer's own URL) is refused (downgrade) and nothing changes"
    if rerun_ex --yes >"$WORK/dg.out" 2>"$WORK/dg.err"; then echo "downgrade accepted"; exit 1; fi
    grep -qi 'downgrade\|older' "$WORK/dg.err" || { cat "$WORK/dg.err"; exit 1; }
    [[ "$(state_version)" == "$V2" && "$(sha256sum /usr/local/bin/baft)" == "$BIN2" && "$(secrets)" == "$S0" ]] || { echo "refused downgrade changed the install"; exit 1; }

    for broken in brokenv brokenr; do
      log "upgrade to a release whose binary is broken ($broken): rolled back"
      PID1="$(systemctl show -p MainPID --value baft-ex)"
      if run_up "$U/$broken" --yes >"$WORK/rb.out" 2>"$WORK/rb.err"; then echo "broken upgrade reported success"; exit 1; fi
      grep -q 'rolling back' "$WORK/rb.err" || { cat "$WORK/rb.err"; exit 1; }
      [[ "$(state_version)" == "$V2" ]] || { echo "release state moved on a failed upgrade"; exit 1; }
      [[ "$(sha256sum /usr/local/bin/baft)" == "$BIN2" ]] || { echo "binary not restored"; exit 1; }
      [[ "$(secrets)" == "$S0" ]] || { echo "secrets or config changed by a failed upgrade"; exit 1; }
      sleep 5
      systemctl is-active baft-ex baft-ir
      python3 tests/e2e/echo.py send 1443 2 2
      if [[ "$broken" == brokenv ]]; then
        [[ "$(systemctl show -p MainPID --value baft-ex)" == "$PID1" ]] || { echo "a binary that never ran restarted the service"; exit 1; }
      fi
    done
  fi
fi
log "PASS"
