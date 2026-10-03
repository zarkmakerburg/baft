#!/usr/bin/env bash
# Failure-injection and "nothing changes before confirmation" checks for
# install.sh on a throwaway fake tree: every path lives under one temp
# directory, systemctl is a stateful stub (active/enabled files) and apt-get
# only records its calls, so no systemd is needed. Needs root (install.sh
# checks) and Go (a signed release is built with throwaway keys).
#
#   sudo tests/e2e/installer_faults.sh
set -Eeuo pipefail
cd "$(dirname "$0")/../.."
ROOT="$PWD"
[[ "$EUID" -eq 0 ]] || { echo "run as root"; exit 1; }
W="$(mktemp -d)"
trap 'rm -rf "$W"' EXIT
log() { printf '[faults] %s\n' "$*"; }
# Diagnostics for CI (job logs are not always retrievable; annotations are).
on_err() { local rc=$? line=$1 cmd=$2
  { echo "line $line (rc $rc): $cmd"; for f in "$W"/e "$W"/o "$W"/ex.err "$W"/ir.err; do [[ -f "$f" ]] && { echo "== $f"; tail -n 12 "$f"; }; done; } >&2
  if [[ "${GITHUB_ACTIONS:-}" == "true" ]]; then
    printf '::error title=installer-faults::line %s (rc %s): %s\n' "$line" "$rc" "${cmd//%/%25}"
  fi
}
trap 'on_err "$LINENO" "$BASH_COMMAND"' ERR
fail() { printf '[faults] FAIL: %s\n' "$*" >&2; exit 1; }

mkdir -p "$W/stubs" "$W/sysd" "$W/state"
cat >"$W/stubs/systemctl" <<'STUB'
#!/bin/sh
S="$STUB_DIR/state"
echo "$@" >>"$STUB_DIR/calls"
case "$1" in
  is-active) [ -f "$S/active.$2" ] && echo active || echo inactive ;;
  is-enabled) [ -f "$S/enabled.$2" ] && echo enabled || echo disabled ;;
  enable) touch "$S/enabled.$2" ;;
  disable) rm -f "$S/enabled.$2" ;;
  start|restart) touch "$S/active.$2" ;;
  stop) rm -f "$S/active.$2" ;;
  show) echo 4242 ;;
esac
exit 0
STUB
cat >"$W/stubs/apt-get" <<'STUB'
#!/bin/sh
echo "$@" >>"$STUB_DIR/apt.log"
exit 0
STUB
chmod +x "$W/stubs/"*

# A PATH that has everything except curl (a missing prerequisite).
mkdir -p "$W/nocurl"
for d in /usr/bin /bin /usr/sbin /sbin; do
  for f in "$d"/*; do
    n="$(basename "$f")"
    [[ "$n" == curl || -e "$W/nocurl/$n" ]] && continue
    ln -s "$f" "$W/nocurl/$n" 2>/dev/null || true
  done
done

log "build a signed release (v1), a newer one (v2) with the same root"
export BAFT_LOCAL_RELEASE_ROOT="$W/root"
scripts/release/local_release.sh "$W/rel1" v0.0.1-f >/dev/null
scripts/release/local_release.sh "$W/rel2" v0.0.2-f >/dev/null
unset BAFT_LOCAL_RELEASE_ROOT

BASEPATH="$W/stubs:$PATH"
common=(STUB_DIR="$W" BAFT_USER=root BAFT_INSTALL_FROM=release BAFT_ROOT_PUB="$(cat "$W/rel1/root.pub")"
  BAFT_BIN="$W/bin/baft" BAFT_PAIR_BIN="$W/bin/baft-pair" BAFT_AGENT_BIN="$W/bin/baft-agent"
  BAFT_SYSTEMD_DIR="$W/sysd" BAFT_AGENT_STATE_DIR="$W/ags" BAFT_METRICS_LISTEN=127.0.0.1:9191)
rel() { # release dir -> env
  echo "BAFT_RELEASE_URL=file://$1/dist" "BAFT_REVOCATIONS_URL=file://$1/revocations.json"; }
# role env for an instance named $1 (ex/ir)
role_env() { echo "BAFT_SERVICE=baft-$1" "BAFT_PREFIX=$W/opt-$1" "BAFT_CONFIG_DIR=$W/etc-$1" "BAFT_STATE_DIR=$W/var-$1" "BAFT_RELEASE_STATE=$W/opt-$1/release-state.json"; }
run() { # PATH-prefix env... -- args
  local p="$BASEPATH"; if [[ "$1" == nocurl ]]; then p="$W/stubs:$W/nocurl"; shift; fi
  env PATH="$p" "${common[@]}" "$@" <"${RUN_STDIN:-/dev/null}"
}
snapshot() { # everything an install could touch, minus stub bookkeeping
  { { find "$W/bin" "$W/sysd" "$W"/etc-* "$W"/var-* "$W"/opt-* "$W/ag" "$W/ags" "$W/src" -xdev 2>/dev/null || true; } | sort |
      while read -r f; do [[ -f "$f" ]] && sha256sum "$f" && stat -c '%n %U:%G %a' "$f" || echo "$f"; done
    ls "$W/state" | sort; { ls /usr/local/go 2>/dev/null || true; } | head -1; } | sha256sum
}
active() { [[ -f "$W/state/active.$1.service" ]]; }
enabled() { [[ -f "$W/state/enabled.$1.service" ]]; }

log "install a paired EX and IR from release v1 on the fake tree"
mkfifo "$W/ff"; exec 3<>"$W/ff"
RUN_STDIN="$W/ff" run $(rel "$W/rel1") $(role_env ex) BAFT_NONINTERACTIVE=0 BAFT_PORT=8443 BAFT_TARGET=127.0.0.1:2443 \
  bash install.sh --role ex --public-address 127.0.0.1 >"$W/ex.out" 2>"$W/ex.err" &
EXPID=$!
for _ in $(seq 100); do grep -q '^BAFTPAIR1:' "$W/ex.out" 2>/dev/null && break; sleep 0.2; done
CODE="$(grep -m1 '^BAFTPAIR1:' "$W/ex.out")"
run $(rel "$W/rel1") $(role_env ir) BAFT_NONINTERACTIVE=1 BAFT_PAIRING_CODE="$CODE" BAFT_METRICS_LISTEN=127.0.0.1:9192 \
  BAFT_ROUTE_LISTEN=127.0.0.1:1443 bash install.sh --role ir >"$W/ir.out" 2>"$W/ir.err"
grep -m1 '^BAFTREPLY1:' "$W/ir.out" >&3
wait "$EXPID"; exec 3>&-
active baft-ex && enabled baft-ex || { cat "$W/ex.err" "$W/ir.err" | tail -20; ls "$W/state"; fail "EX not installed"; }

# Leading NAME=value arguments are environment; the rest are installer arguments.
EXI() { local e=(); while [[ $# -gt 0 && "$1" == *=* ]]; do e+=("$1"); shift; done
  run $(rel "$W/rel1") $(role_env ex) BAFT_NONINTERACTIVE=1 BAFT_PORT=8443 BAFT_TARGET=127.0.0.1:2443 "${e[@]}" bash install.sh --role ex --public-address 127.0.0.1 "$@"; }

# ---------------------------------------------------------------------------
log "1. nothing on the host changes before the plan is confirmed"
rm -f "$W/state/active.baft-ex.service"            # the install now needs a (changing) repair
: >"$W/apt.log"
S0="$(snapshot)"
rc=0; EXI >"$W/o" 2>"$W/e" || rc=$?
[[ "$rc" == 4 ]] || { cat "$W/e"; fail "changing plan without --yes exited $rc, want 4"; }
[[ "$(snapshot)" == "$S0" ]] || fail "a refused plan changed the host"
[[ ! -e "$W/bin/baft.new" && ! -e "$W/bin/baft-pair.new" ]] || fail "*.new left in the bin directory"

log "1b. a missing prerequisite on an existing install: no apt before confirmation"
rc=0; run nocurl $(rel "$W/rel1") $(role_env ex) BAFT_NONINTERACTIVE=1 bash install.sh --role ex --public-address 127.0.0.1 >"$W/o" 2>"$W/e" || rc=$?
[[ "$rc" == 4 ]] || { cat "$W/e"; fail "missing curl without --yes exited $rc, want 4"; }
grep -q 'packages' "$W/e" || { cat "$W/e"; fail "the plan does not list the packages"; }
[[ ! -s "$W/apt.log" ]] || fail "apt ran before confirmation: $(cat "$W/apt.log")"
[[ "$(snapshot)" == "$S0" ]] || fail "missing-prerequisite refusal changed the host"

log "1c. --from-source on an existing install is refused the same way (no Go, no source tree)"
rc=0; run nocurl BAFT_INSTALL_FROM=source BAFT_REPO_URL="file://$ROOT" BAFT_REF=HEAD $(role_env ex) BAFT_NONINTERACTIVE=1 \
  bash install.sh --role ex --public-address 127.0.0.1 >"$W/o" 2>"$W/e" || rc=$?
[[ "$rc" == 4 ]] || { cat "$W/e"; fail "source rerun without --yes exited $rc, want 4"; }
[[ ! -s "$W/apt.log" && ! -e "$W/opt-ex/src" ]] || fail "apt or the source tree changed before confirmation"
[[ "$(snapshot)" == "$S0" ]] || fail "source refusal changed the host"

log "1d. a broken install is refused before any mutation, even with --yes and a missing prerequisite"
cp "$W/etc-ex/baft.yaml" "$W/baft.yaml.good"
printf 'not: [valid\n' >"$W/etc-ex/baft.yaml"
S1="$(snapshot)"
rc=0; run nocurl $(rel "$W/rel1") $(role_env ex) BAFT_NONINTERACTIVE=1 bash install.sh --role ex --public-address 127.0.0.1 --yes >"$W/o" 2>"$W/e" || rc=$?
[[ "$rc" == 3 ]] || { cat "$W/e"; fail "broken install exited $rc, want 3"; }
[[ ! -s "$W/apt.log" ]] || fail "apt ran on a broken install"
[[ "$(snapshot)" == "$S1" ]] || fail "a refused broken install changed the host"
cp "$W/baft.yaml.good" "$W/etc-ex/baft.yaml"; chown root:root "$W/etc-ex/baft.yaml"; chmod 0640 "$W/etc-ex/baft.yaml"
touch "$W/state/active.baft-ex.service"

log "1e. valid config + Noise key + PKI + unit, but the installed baft binary is MISSING: binary only, config and pairing untouched"
cfgsum() { sha256sum "$W/etc-ex/baft.yaml" "$W/etc-ex/noise-key.json" "$W"/etc-ex/pki/* "$W/sysd/baft-ex.service"; stat -c '%n %U:%G %a' "$W/etc-ex/baft.yaml"; }
K0="$(cfgsum)"
rm -f "$W/bin/baft"
rc=0; EXI --plan --json >"$W/plan.json" 2>/dev/null || rc=$?
python3 - "$W/plan.json" <<'PY' || fail "plan for a missing binary re-pairs or overwrites the config"
import json, sys
p = {s["item"]: s["action"] for s in json.load(open(sys.argv[1]))["steps"]}
assert p["config"] == "keep" and p["pairing"] == "skip" and p["baft binary"] == "create", p
PY
rc=0; EXI >"$W/o" 2>"$W/e" || rc=$?
[[ "$rc" == 4 ]] || { cat "$W/e"; fail "repair without --yes exited $rc"; }
EXI --yes >"$W/o" 2>"$W/e" || { cat "$W/e"; fail "repair of a missing binary failed"; }
grep -q '^BAFTPAIR1:' "$W/o" && fail "a repair printed a pairing code"
grep -q 'config .*keep' "$W/e" && grep -q 'pairing .*skip' "$W/e" || { cat "$W/e"; fail "final plan does not keep the config and skip pairing"; }
[[ "$(cfgsum)" == "$K0" ]] || fail "config, key, PKI or unit changed while repairing a missing binary"
[[ -x "$W/bin/baft" ]] || fail "binary not restored"
active baft-ex && enabled baft-ex || fail "service not running after the repair"

log "1f. INVALID config + missing binary: refused, config unchanged, binary still missing"
printf 'not: [valid\n' >"$W/etc-ex/baft.yaml"
chown root:root "$W/etc-ex/baft.yaml"; chmod 0640 "$W/etc-ex/baft.yaml"
rm -f "$W/bin/baft"
K1="$(cfgsum)"; S1="$(snapshot)"
rc=0; EXI --yes >"$W/o" 2>"$W/e" || rc=$?
[[ "$rc" == 3 ]] || { cat "$W/e"; fail "invalid config + missing binary exited $rc, want 3"; }
grep -q '^BAFTPAIR1:' "$W/o" && fail "paired over an invalid config"
[[ "$(cfgsum)" == "$K1" && ! -e "$W/bin/baft" ]] || fail "a refused install changed config or binary"
[[ "$(snapshot)" == "$S1" ]] || fail "a refused install changed the host"
cp "$W/baft.yaml.good" "$W/etc-ex/baft.yaml"; chown root:root "$W/etc-ex/baft.yaml"; chmod 0640 "$W/etc-ex/baft.yaml"
EXI --yes >"$W/o" 2>"$W/e" || { cat "$W/e"; fail "restoring the binary failed"; }

# ---------------------------------------------------------------------------
cfg_hash() { sha256sum "$W/etc-ex/baft.yaml" "$W/etc-ex/noise-key.json" "$W"/etc-ex/pki/* "$W/sysd/baft-ex.service" "$W/opt-ex/release-state.json" "$W/bin/baft"; }
log "2a. failure after restart/enable, service was ACTIVE + ENABLED: restored to active + enabled, old binary"
C0="$(cfg_hash)"
rc=0; EXI $(rel "$W/rel2") BAFT_TEST_FAIL_AT=pre-commit --yes >"$W/o" 2>"$W/e" || rc=$?
[[ "$rc" != 0 ]] || fail "injected failure reported success"
grep -q 'rolling back' "$W/e" || { cat "$W/e"; fail "no rollback"; }
active baft-ex && enabled baft-ex || fail "service state not restored (active+enabled)"
[[ "$(cfg_hash)" == "$C0" ]] || fail "files not restored after a failed upgrade"

log "2b. failure after start/enable, service was INACTIVE + DISABLED: back to inactive + disabled"
rm -f "$W/state/active.baft-ex.service" "$W/state/enabled.baft-ex.service"
C0="$(cfg_hash)"
rc=0; EXI BAFT_TEST_FAIL_AT=pre-commit --yes >"$W/o" 2>"$W/e" || rc=$?
[[ "$rc" != 0 ]] || fail "injected failure reported success"
active baft-ex && fail "a service that was stopped is left running after rollback"
enabled baft-ex && fail "a service that was disabled is left enabled after rollback"
[[ "$(cfg_hash)" == "$C0" ]] || fail "files changed by a rolled-back repair"
log "2b'. the same repair without a failure starts and enables it"
EXI --yes >"$W/o" 2>"$W/e" || { cat "$W/e"; fail "repair failed"; }
active baft-ex && enabled baft-ex || fail "repair did not start and enable the service"

log "2c. failure on a FRESH role install: unit, key, PKI removed and nothing left enabled or running"
run $(rel "$W/rel1") $(role_env fr) BAFT_SERVICE=baft-fr BAFT_NONINTERACTIVE=1 BAFT_PORT=8444 BAFT_TEST_FAIL_AT=pre-commit \
  bash install.sh --role ex --public-address 127.0.0.1 >"$W/o" 2>"$W/e" && fail "injected failure reported success"
[[ ! -e "$W/sysd/baft-fr.service" && ! -e "$W/etc-fr/noise-key.json" && ! -e "$W/etc-fr/pki/ca.key" && ! -e "$W/etc-fr/baft.yaml" ]] || fail "files of the failed fresh install were left"
enabled baft-fr && fail "enablement of a removed unit was left behind"
active baft-fr && fail "a failed fresh install left the service running"

# ---------------------------------------------------------------------------
AG=(BAFT_AGENT_DIR="$W/ag" BAFT_BCC_URL=https://bcc.example.com BAFT_NODE_ID=n1 BAFT_BCC_JOB_KEY=jobkeyabc BAFT_AGENT_TOKEN=tok1
    BAFT_PREFIX="$W/opt-ag" BAFT_CONFIG_DIR="$W/etc-ag" BAFT_STATE_DIR="$W/var-ag" BAFT_RELEASE_STATE="$W/opt-ag/release-state.json" BAFT_NONINTERACTIVE=1)
AGI() { local e=(); while [[ $# -gt 0 && "$1" == *=* ]]; do e+=("$1"); shift; done
  run $(rel "$W/rel1") "${AG[@]}" "${e[@]}" bash install.sh --agent-only "$@"; }
log "3a. agent: failure on a FRESH install leaves nothing behind"
AGI BAFT_TEST_FAIL_AT=pre-commit >"$W/o" 2>"$W/e" && fail "injected failure reported success"
[[ ! -e "$W/sysd/baft-agent.service" && ! -e "$W/ag/token" && ! -e "$W/ag/bcc-job.pub" ]] || fail "agent files left after a failed fresh install"
enabled baft-agent && fail "agent enablement left behind"
active baft-agent && fail "agent left running"
AGI >"$W/o" 2>"$W/e" || { cat "$W/e"; fail "agent install"; }
active baft-agent && enabled baft-agent || fail "agent not running and enabled"
AH() { sha256sum "$W/ag"/* "$W/sysd/baft-agent.service"; }
log "3b. agent: a different token fails after restart -> old token, still active + enabled"
A0="$(AH)"
AGI BAFT_AGENT_TOKEN=tok2 BAFT_TEST_FAIL_AT=pre-commit --yes >"$W/o" 2>"$W/e" && fail "injected failure reported success"
[[ "$(AH)" == "$A0" ]] || fail "agent files not restored"
active baft-agent && enabled baft-agent || fail "agent state not restored (active+enabled)"
log "3c. agent: inactive + disabled before, failure after start/enable -> inactive + disabled"
rm -f "$W/state/active.baft-agent.service" "$W/state/enabled.baft-agent.service"
AGI BAFT_TEST_FAIL_AT=pre-commit --yes >"$W/o" 2>"$W/e" && fail "injected failure reported success"
active baft-agent && fail "agent that was stopped is left running"
enabled baft-agent && fail "agent that was disabled is left enabled"
[[ "$(AH)" == "$A0" ]] || fail "agent files changed"
log "PASS"
