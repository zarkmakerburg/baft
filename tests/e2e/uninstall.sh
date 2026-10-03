#!/usr/bin/env bash
# Safe uninstall acceptance (HQ A3) on a throwaway runner with systemd:
# EX + IR from a signed release, a labelled BCC, an enrolled agent and two
# units the operator wrote by hand. Then every HQ case:
#   ownership conflict blocks | an active tunnel is never stopped silently |
#   an interrupted uninstall is restored, or resumed | normal uninstall keeps
#   user data and the node comes back on reinstall without re-pairing |
#   install -> upgrade -> uninstall -> reinstall (anti-rollback kept) |
#   BCC state deleted only after a verified emergency backup | full uninstall
#   removes only confirmed data | verify clean | fresh reinstall |
#   the unmanaged units survive all of it.
#
#   sudo tests/e2e/uninstall.sh <rel1 dir> <rel2 dir>   (local_release.sh, same root)
set -Eeuo pipefail
cd "$(dirname "$0")/../.."
ROOT="$PWD"
[[ "$EUID" -eq 0 ]] || { echo "run as root"; exit 1; }
REL1="$(cd "${1:?usage: uninstall.sh <rel1> <rel2>}" && pwd)"
REL2="$(cd "${2:?usage: uninstall.sh <rel1> <rel2>}" && pwd)"
WORK="$(mktemp -d)"
PIDS=()
log() { printf '[uninstall-e2e] %s\n' "$*"; }
fail() {
  printf '[uninstall-e2e] FAIL: %s\n' "$*" >&2
  printf '%s\n' "$*" >>"$WORK/zz-fail.log"
  if [[ "${GITHUB_ACTIONS:-}" == "true" ]]; then
    local m="$*"; m="${m:0:3500}"; m="${m//%/%25}"; printf '::error title=uninstall e2e FAIL::%s\n' "${m//$'\n'/%0A}"
  fi
  exit 1
}
cleanup() {
  local rc=$?
  if [[ $rc -ne 0 ]]; then
    {
      for f in "$WORK"/*.out "$WORK"/*.err "$WORK"/*.log; do [[ -f "$f" ]] && { echo "== $f"; tail -n 40 "$f"; }; done
      for s in baft-ex baft-ir baft-agent baft-bcc baft-hand; do echo "== journal $s"; journalctl -u "$s" -n 20 --no-pager 2>/dev/null || true; done
      echo "== uninstall journals"; for r in /var/lib/baft-uninstall/run-*; do [[ -f "$r/journal.json" ]] && python3 -c 'import json,sys;j=json.load(open(sys.argv[1]+"/journal.json"));print(sys.argv[1], j["status"], len(j["files"]), "files", j.get("error",""))' "$r" 2>&1; done
      echo "== BAFT paths left"; find /etc/baft* /var/lib/baft* /opt/baft* /usr/local/bin/baft* -maxdepth 2 2>/dev/null | grep -v '^/var/lib/baft-uninstall/' | head -60 || true
    } >"$WORK/diag" 2>&1
    cat "$WORK/diag"
    if [[ "${GITHUB_ACTIONS:-}" == "true" ]]; then
      awk '/^== /{if (msg != "") print msg; msg=$0; next} {msg = msg "\\n" $0} END{if (msg != "") print msg}' "$WORK/diag" |
        tail -n 8 | while IFS= read -r section; do
          section="${section:0:3500}"; section="${section//%/%25}"
          printf '::error title=uninstall e2e diagnostics::%s\n' "${section//\\n/%0A}"
        done
    fi
  fi
  if [[ -f "$WORK/prov.log" ]]; then
    echo "== provenance of service-home entries"; cat "$WORK/prov.log"
    if [[ "${GITHUB_ACTIONS:-}" == "true" ]]; then
      m="$(tail -c 3500 "$WORK/prov.log")"; m="${m//%/%25}"; printf '::notice title=uninstall e2e provenance (.ghcup and skeleton copies)::%s\n' "${m//$'\n'/%0A}"
    fi
  fi
  # Keep the evidence where a later workflow step can report it.
  mkdir -p /tmp/uninstall-e2e-report
  cp "$WORK"/diag "$WORK"/zz-fail.log "$WORK"/prov.log "$WORK"/*.out "$WORK"/clean.txt /tmp/uninstall-e2e-report/ 2>/dev/null || true
  chmod -R a+rX /tmp/uninstall-e2e-report 2>/dev/null || true
  for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null || true; done
  systemctl stop baft-hand baft-ex baft-ir baft-agent baft-bcc 2>/dev/null || true
  rm -rf "$WORK"
  exit $rc
}
trap cleanup EXIT
on_err() { printf '[uninstall-e2e] line %s: %s\n' "$1" "$2" >&2; [[ "${GITHUB_ACTIONS:-}" == "true" ]] && printf '::error title=uninstall e2e::line %s: %s\n' "$1" "${2//%/%25}"; return 0; }
trap 'on_err "$LINENO" "$BASH_COMMAND"' ERR

ARCH="$(dpkg --print-architecture)"
# Every uninstall runs from a copy outside BAFT's paths: the installed binary
# is one of the things removed.
install -m 0755 "$REL1/dist/baft-linux-$ARCH" "$WORK/baft"
install -m 0755 "$REL1/dist/baft-bcc-linux-$ARCH" "$WORK/baft-bcc"
UN() { "$WORK/baft" uninstall "$@"; }
rel_env() { echo "BAFT_RELEASE_URL=file://$1/dist" "BAFT_REVOCATIONS_URL=file://$1/revocations.json" "BAFT_ROOT_PUB=$(cat "$1/root.pub")"; }
EX_ENV=(BAFT_SERVICE=baft-ex BAFT_CONFIG_DIR=/etc/baft-ex BAFT_STATE_DIR=/var/lib/baft-ex BAFT_PREFIX=/opt/baft-ex BAFT_PORT=8443
  BAFT_METRICS_LISTEN=127.0.0.1:9191 BAFT_TARGET=127.0.0.1:2443)
IR_ENV=(BAFT_SERVICE=baft-ir BAFT_CONFIG_DIR=/etc/baft-ir BAFT_STATE_DIR=/var/lib/baft-ir BAFT_PREFIX=/opt/baft-ir
  BAFT_METRICS_LISTEN=127.0.0.1:9192 BAFT_ROUTE_LISTEN=127.0.0.1:1443)
inst_ex() { local r="$1"; shift; env $(rel_env "$r") BAFT_INSTALL_FROM=release "${EX_ENV[@]}" "$@"; }
inst_ir() { local r="$1"; shift; env $(rel_env "$r") BAFT_INSTALL_FROM=release "${IR_ENV[@]}" "$@"; }

# Provenance of what appears in the service user's home (the EX state
# directory, created by useradd --create-home from /etc/skel): recorded at
# every phase, before any behaviour depends on it.
prov() {
  {
    echo "--- phase: $1"
    for d in /var/lib/baft-ex /var/lib/baft-ir; do
      if [[ -d "$d" ]]; then
        stat -c '%n uid=%u gid=%g mode=%a mtime=%y ctime=%z' "$d"
        for e in "$d"/.[!.]*; do
          [[ -e "$e" || -L "$e" ]] || continue
          stat -c '  %n uid=%u gid=%g mode=%a type=%F mtime=%y ctime=%z' "$e"
          s="/etc/skel/$(basename "$e")"
          if [[ -e "$s" || -L "$s" ]]; then
            echo "    skeleton original: $(stat -c 'uid=%u gid=%g mode=%a type=%F' "$s"); differences (no dereference):"
            timeout 60 diff -rq --no-dereference "$s" "$e" 2>&1 | head -5 | sed 's/^/      /'
            echo "    symlinks inside: $(find "$e" -type l 2>/dev/null | wc -l), files: $(find "$e" -type f 2>/dev/null | wc -l)"
          else
            echo "    no skeleton original"
          fi
        done
      else
        echo "$d absent"
      fi
    done
  } >>"$WORK/prov.log" 2>&1
}
{ echo "--- /etc/skel before any install:"; ls -la /etc/skel; getent passwd baft || echo "no baft user yet"; } >"$WORK/prov.log" 2>&1

python3 tests/e2e/echo.py serve 2443 >"$WORK/target.log" 2>&1 & PIDS+=($!)

pair_fresh() { # rel
  local r="$1"
  rm -f "$WORK/reply"; mkfifo "$WORK/reply"; exec 3<>"$WORK/reply"
  inst_ex "$r" bash install.sh --role ex --public-address 127.0.0.1 <"$WORK/reply" >"$WORK/ex.out" 2>"$WORK/ex.err" &
  local expid=$!
  for _ in $(seq 1 600); do
    grep -q '^BAFTPAIR1:' "$WORK/ex.out" 2>/dev/null && break
    kill -0 "$expid" 2>/dev/null || fail "EX installer exited early"
    sleep 1
  done
  inst_ir "$r" BAFT_NONINTERACTIVE=1 BAFT_PAIRING_CODE="$(grep -m1 '^BAFTPAIR1:' "$WORK/ex.out")" bash install.sh --role ir </dev/null >"$WORK/ir.out" 2>"$WORK/ir.err"
  grep -m1 '^BAFTREPLY1:' "$WORK/ir.out" >&3
  wait "$expid"
  exec 3>&-
}
traffic() {
  for _ in $(seq 1 60); do
    if systemctl is-active --quiet baft-ex && systemctl is-active --quiet baft-ir &&
       python3 -c "import socket;socket.create_connection(('127.0.0.1',1443),0.5)" 2>/dev/null; then break; fi
    sleep 1
  done
  python3 tests/e2e/echo.py send 1443 2 2 >"$WORK/send.log" 2>&1 || { cat "$WORK/send.log"; fail "no traffic through the tunnel"; }
}
datahash() { sha256sum /etc/baft-ex/baft.yaml /etc/baft-ex/noise-key.json /etc/baft-ex/pki/* /etc/baft-ir/baft.yaml /etc/baft-ir/noise-key.json /var/lib/baft-ir/peer-ca.pem; }
units() { systemctl list-unit-files --no-legend 'baft*' | awk '{print $1}' | sort | tr '\n' ' '; }

log "install EX and IR from release 1 and pass traffic"
pair_fresh "$REL1"
prov "fresh install"
traffic
prov "services started, traffic"

log "a labelled BCC (systemd) with state, and an agent enrolled to it"
install -m 0755 "$WORK/baft-bcc" /usr/local/bin/baft-bcc
install -d -m 0700 /var/lib/baft-bcc
echo admintok >/var/lib/baft-bcc/admin.token; chmod 600 /var/lib/baft-bcc/admin.token
/usr/local/bin/baft-bcc access init --access-file /var/lib/baft-bcc/state.db.access.json >/dev/null
AGENT_TOKEN="$(head -c 24 /dev/urandom | base64 | tr -d '/+=')"
cat >/etc/systemd/system/baft-bcc.service <<UNIT
# baft-managed: true
# baft-component: bcc
[Unit]
Description=BAFT Command Center (e2e)

[Service]
WorkingDirectory=/var/lib/baft-bcc
Environment=AGENT_TOK=$AGENT_TOKEN
ExecStart=/usr/local/bin/baft-bcc --admin-token-file /var/lib/baft-bcc/admin.token --state-file /var/lib/baft-bcc/state.db --listen 127.0.0.1:18200
Restart=on-failure

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable --now baft-bcc
for _ in $(seq 1 30); do curl -fsS -o /dev/null -H 'Authorization: Bearer admintok' http://127.0.0.1:18200/api/nodes && break; sleep 1; done
curl -fsS -H 'Authorization: Bearer admintok' -d '{"ID":"node-e2e","Address":"127.0.0.1:1","Role":"foreign","AgentTokenEnv":"AGENT_TOK"}' http://127.0.0.1:18200/api/nodes >/dev/null
JOB_KEY="$(/usr/local/bin/baft-bcc jobkey show --job-key-file /var/lib/baft-bcc/state.db.job-key)"
printf '%s\n' "$AGENT_TOKEN" >"$WORK/token"; chmod 600 "$WORK/token"
env $(rel_env "$REL1") BAFT_INSTALL_FROM=release BAFT_BCC_JOB_KEY="$JOB_KEY" BAFT_AGENT_TOKEN_FILE="$WORK/token" BAFT_AGENT_ALLOW_HTTP=1 \
  bash install.sh --agent-only --bcc-url http://127.0.0.1:18200 --node-id node-e2e </dev/null >"$WORK/agent.out" 2>"$WORK/agent.err"
systemctl is-active baft-agent baft-bcc

log "two units the operator wrote: a running 'tunnel' and one that runs the BAFT binary"
install -d /etc/baft-hand
printf 'operator data\n' >/etc/baft-hand/hand.conf
cat >/etc/systemd/system/baft-hand.service <<UNIT
[Unit]
Description=operator's own tunnel (not BAFT's)

[Service]
ExecStart=/usr/bin/python3 $ROOT/tests/e2e/echo.py serve 3443

[Install]
WantedBy=multi-user.target
UNIT
cat >/etc/systemd/system/baft-manual.service <<UNIT
[Unit]
Description=operator's manual BAFT run (not installer-made)

[Service]
ExecStart=/usr/local/bin/baft run --file /etc/baft-hand/manual.yaml
UNIT
systemctl daemon-reload
systemctl enable --now baft-hand
HAND="$(sha256sum /etc/systemd/system/baft-hand.service /etc/systemd/system/baft-manual.service /etc/baft-hand/hand.conf)"
hand_ok() {
  systemctl is-active --quiet baft-hand || fail "the operator's running unit was stopped"
  [[ "$(sha256sum /etc/systemd/system/baft-hand.service /etc/systemd/system/baft-manual.service /etc/baft-hand/hand.conf)" == "$HAND" ]] || fail "the operator's files changed"
  python3 -c "import socket;s=socket.create_connection(('127.0.0.1',3443),2);s.sendall(b'x');assert s.recv(1)==b'x'" || fail "the operator's service does not answer"
}
hand_ok

log "preview: exact plan, nothing changes"
SNAP0="$(datahash; units)"
UN --preview >"$WORK/preview.out"
UN --preview --json >"$WORK/preview.json"
python3 - "$WORK/preview.json" <<'PY'
import json, sys
p = json.load(open(sys.argv[1]))
stop = {s["unit"] for s in p["stop"]}
assert {"baft-ex.service", "baft-ir.service", "baft-agent.service"} <= stop and "baft-bcc.service" not in stop, stop
untouched = {a["path"] for a in p["untouched"]}
assert "/etc/systemd/system/baft-hand.service" in untouched and "/etc/systemd/system/baft-manual.service" in untouched, untouched
keep = {a["path"]: a["reason"] for a in p["keep"]}
assert "baft-manual.service" in keep.get("/usr/local/bin/baft", ""), keep.get("/usr/local/bin/baft")
assert "/etc/baft-ex/baft.yaml" in keep and "/etc/baft-ex/noise-key.json" in keep, keep
assert p["default"] == "KEEP DATA" and len(p["active_tunnels"]) == 2 and p["blocked"] == [], p
PY
grep -q "BAFT Uninstall Preview" "$WORK/preview.out"
[[ "$(datahash; units)" == "$SNAP0" ]] || fail "preview changed something"
systemctl is-active baft-ex baft-ir baft-agent >/dev/null

log "ownership conflict blocks the destructive action"
cp /etc/baft-ir/baft.yaml "$WORK/ir.yaml"
printf '{"managed_by":"baft","tunnel_id":"x","unit_sha256":"0","config_sha256":"0"}\n' >/etc/baft-ir/baft.managed.json
rc=0; UN --yes --stop-active-tunnels >"$WORK/conflict.out" 2>&1 || rc=$?
[[ "$rc" == 3 ]] || { cat "$WORK/conflict.out"; fail "conflict exited $rc, want 3"; }
grep -q OWNERSHIP_CONFLICT "$WORK/conflict.out"
systemctl is-active baft-ex baft-ir baft-agent >/dev/null || fail "a blocked uninstall stopped something"
rm /etc/baft-ir/baft.managed.json
[[ "$(datahash; units)" == "$SNAP0" ]] || fail "a blocked uninstall changed something"

log "an active tunnel is never stopped without explicit consent"
rc=0; UN --yes >"$WORK/noconsent.out" 2>&1 || rc=$?
[[ "$rc" == 4 ]] || { cat "$WORK/noconsent.out"; fail "no consent exited $rc, want 4"; }
grep -q -- "--stop-active-tunnels" "$WORK/noconsent.out"
systemctl is-active baft-ex baft-ir >/dev/null || fail "tunnels stopped without consent"
traffic

log "interrupted uninstall (killed after the first file moved) -> --restore"
rc=0; BAFT_UNINSTALL_CRASH_AT=after-first-move UN --yes --stop-active-tunnels >"$WORK/crash1.out" 2>&1 || rc=$?
[[ "$rc" == 86 ]] || { cat "$WORK/crash1.out"; fail "crash hook exited $rc"; }
systemctl is-active --quiet baft-ex && fail "services were not stopped before the crash point"
rc=0; UN --yes --stop-active-tunnels >"$WORK/pending.out" 2>&1 || rc=$?
[[ "$rc" == 5 ]] || { cat "$WORK/pending.out"; fail "a new run with a pending journal exited $rc, want 5"; }
UN --restore >"$WORK/restore.out" 2>&1 || { cat "$WORK/restore.out"; fail "restore failed"; }
[[ "$(datahash; units)" == "$SNAP0" ]] || fail "restore did not put everything back"
systemctl is-active baft-ex baft-ir baft-agent baft-bcc >/dev/null || fail "restore did not restart the services"
traffic
hand_ok
prov "crash and restore"

log "interrupted uninstall (killed before the commit) -> --resume: the normal uninstall"
rc=0; BAFT_UNINSTALL_CRASH_AT=before-commit UN --yes --stop-active-tunnels >"$WORK/crash2.out" 2>&1 || rc=$?
[[ "$rc" == 86 ]] || { cat "$WORK/crash2.out"; fail "crash hook exited $rc"; }
# The crashed run's journal stays in "applying"; --resume takes that very run
# to "committed".
latest_run() { ls -1d /var/lib/baft-uninstall/run-* | sort | tail -n 1; }
run_status() { python3 -c 'import json,sys;print(json.load(open(sys.argv[1]+"/journal.json"))["status"])' "$1"; }
CRASHED="$(latest_run)"
[[ "$(run_status "$CRASHED")" == applying ]] || fail "after the crash the journal of $CRASHED is $(run_status "$CRASHED"), want applying"
rc=0; UN --resume >"$WORK/resume.out" 2>&1 || rc=$?
[[ "$rc" == 0 ]] || fail "resume exited $rc:
$(tail -n 30 "$WORK/resume.out")"
grep -q "Uninstall finished and verified" "$WORK/resume.out" || fail "resume did not finish the run:
$(tail -n 30 "$WORK/resume.out")"
grep -q "journal:  $CRASHED" "$WORK/resume.out" || fail "resume finished another run than $CRASHED:
$(tail -n 30 "$WORK/resume.out")"
[[ "$(run_status "$CRASHED")" == committed ]] || fail "after resume the journal of $CRASHED is $(run_status "$CRASHED"), want committed"
for u in baft-ex baft-ir baft-agent; do
  [[ ! -e /etc/systemd/system/$u.service ]] || fail "$u.service left"
  systemctl is-active --quiet "$u" && fail "$u still running"
done
[[ ! -e /usr/local/bin/baft-agent && ! -e /usr/local/bin/baft-pair ]] || fail "binaries left"
[[ -e /usr/local/bin/baft ]] || fail "removed /usr/local/bin/baft although baft-manual.service runs it"
[[ -e /usr/local/bin/baft-bcc ]] && systemctl is-active --quiet baft-bcc || fail "BCC is not in the standard scope but was touched"
[[ "$(datahash)" == "$(echo "$SNAP0" | head -n "$(datahash | wc -l)")" ]] || fail "user data changed by the normal uninstall"
[[ -e /etc/baft-agent/token && -e /opt/baft-ex/release-state.json ]] || fail "agent token or release state removed without being chosen"
hand_ok
prov "normal uninstall (resumed)"

log "reinstall over the kept data: same node, no re-pairing"
D0="$(datahash)"
inst_ex "$REL1" BAFT_NONINTERACTIVE=1 bash install.sh --role ex --public-address 127.0.0.1 --yes </dev/null >"$WORK/rex.out" 2>"$WORK/rex.err"
inst_ir "$REL1" BAFT_NONINTERACTIVE=1 bash install.sh --role ir --yes </dev/null >"$WORK/rir.out" 2>"$WORK/rir.err"
prov "reinstall"
grep -q 'BAFTPAIR1:\|BAFTREPLY1:' "$WORK/rex.out" "$WORK/rir.out" && fail "reinstall paired again"
[[ "$(datahash)" == "$D0" ]] || fail "reinstall changed the kept config or keys"
traffic

log "upgrade to release 2, uninstall, reinstall: release 1 is refused (anti-rollback kept), release 2 is fine"
inst_ex "$REL2" BAFT_NONINTERACTIVE=1 bash install.sh --role ex --public-address 127.0.0.1 --yes </dev/null >"$WORK/uex.out" 2>"$WORK/uex.err"
inst_ir "$REL2" BAFT_NONINTERACTIVE=1 bash install.sh --role ir --yes </dev/null >"$WORK/uir.out" 2>"$WORK/uir.err"
prov "upgrade"
V2="$(sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' /opt/baft-ex/release-state.json)"
traffic
UN --yes --stop-active-tunnels >"$WORK/un2.out" 2>&1 || { cat "$WORK/un2.out"; fail "uninstall after upgrade"; }
[[ "$(sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' /opt/baft-ex/release-state.json)" == "$V2" ]] || fail "release state not kept"
rc=0; inst_ex "$REL1" BAFT_NONINTERACTIVE=1 bash install.sh --role ex --public-address 127.0.0.1 --yes </dev/null >"$WORK/dg.out" 2>"$WORK/dg.err" || rc=$?
[[ "$rc" != 0 ]] && grep -qi "downgrade\|older" "$WORK/dg.err" || { cat "$WORK/dg.err"; fail "an older release was accepted after uninstall + reinstall"; }
inst_ex "$REL2" BAFT_NONINTERACTIVE=1 bash install.sh --role ex --public-address 127.0.0.1 --yes </dev/null >"$WORK/r2ex.out" 2>"$WORK/r2ex.err"
inst_ir "$REL2" BAFT_NONINTERACTIVE=1 bash install.sh --role ir --yes </dev/null >"$WORK/r2ir.out" 2>"$WORK/r2ir.err"
traffic
hand_ok

prov "uninstall after upgrade, reinstall"

log "BCC state: deleted only after a verified emergency backup"
rc=0; UN --bcc --delete-bcc-state >"$WORK/bccno.out" 2>&1 || rc=$?
[[ "$rc" == 4 ]] || fail "BCC removal without --yes exited $rc"
UN --bcc --delete-bcc-state --yes >"$WORK/bcc.out" 2>&1 || { cat "$WORK/bcc.out"; fail "BCC uninstall"; }
BK="$(sed -n 's/.*BCC emergency backup: \([^ ]*\) (verified: \([^;)]*\).*/\1 \2/p' "$WORK/bcc.out")"
[[ "$BK" == /var/backups/baft/bcc-emergency-*" sha256+baft-bcc" ]] || { cat "$WORK/bcc.out"; fail "no backup verified by baft-bcc: $BK"; }
BKDIR="${BK%% *}"
"$WORK/baft-bcc" verify-backup --dir "$BKDIR" >"$WORK/verify.out" || { cat "$WORK/verify.out"; fail "the emergency backup does not verify"; }
grep -q "1 node" "$WORK/verify.out" || { cat "$WORK/verify.out"; fail "the backup lacks the BCC node"; }
[[ ! -e /var/lib/baft-bcc/state.db && ! -e /var/lib/baft-bcc/state.db.job-key && ! -e /var/lib/baft-bcc/state.db.access.json ]] || fail "BCC state left"
[[ -e /var/lib/baft-bcc/state.db.audit.jsonl ]] || fail "BCC audit removed without being chosen"
[[ -e /var/lib/baft-bcc/admin.token ]] || fail "the operator's admin token was removed"
[[ ! -e /etc/systemd/system/baft-bcc.service && ! -e /usr/local/bin/baft-bcc ]] || fail "BCC unit or binary left"
traffic
hand_ok

log "full uninstall removes only the confirmed data (tunnel configs)"
UN --full --delete-tunnel-configs --yes --stop-active-tunnels >"$WORK/full1.out" 2>&1 || { cat "$WORK/full1.out"; fail "full uninstall"; }
[[ ! -e /etc/baft-ex/baft.yaml && ! -e /etc/baft-ir/baft.yaml && ! -e /var/lib/baft-ir/pairing.pending.json ]] || fail "chosen tunnel configs left"
[[ -e /etc/baft-ex/noise-key.json && -e /etc/baft-ex/pki/ca.key && -e /opt/baft-ex/release-state.json && -e /etc/baft-agent/token && -e /var/lib/baft-bcc/state.db.audit.jsonl ]] || fail "data removed without being chosen"
[[ -d "$BKDIR" ]] || fail "the emergency backup was removed"
hand_ok
prov "full uninstall (tunnel configs)"

log "everything chosen: verify clean (the operator first removes their manual unit)"
rm /etc/systemd/system/baft-manual.service; systemctl daemon-reload
HAND="$(sha256sum /etc/systemd/system/baft-hand.service /etc/baft-hand/hand.conf)"
hand_ok() {
  systemctl is-active --quiet baft-hand || fail "the operator's running unit was stopped"
  [[ "$(sha256sum /etc/systemd/system/baft-hand.service /etc/baft-hand/hand.conf)" == "$HAND" ]] || fail "the operator's files changed"
}
ALL=(--full --delete-certificates --delete-backups --delete-audit --delete-tunnel-configs --yes --stop-active-tunnels)
UN "${ALL[@]}" --bcc-state-file /var/lib/baft-bcc/state.db >"$WORK/full2.out" 2>&1 || { cat "$WORK/full2.out"; fail "purge"; }
for p in ex ir; do
  UN "${ALL[@]}" --prefix /opt/baft-$p --config-dir /etc/baft-$p --state-dir /var/lib/baft-$p >"$WORK/purge-$p.out" 2>&1 || { cat "$WORK/purge-$p.out"; fail "purge $p"; }
done
prov "purge"
# Ownership-aware clean: every proven, selected BAFT artifact is gone; what
# survives is exactly what the uninstall reports as UNKNOWN / unmanaged (and
# the directories that hold it).
for b in /usr/local/bin/baft /usr/local/bin/baft-pair /usr/local/bin/baft-agent /usr/local/bin/baft-bcc; do
  [[ -e "$b" ]] && fail "binary left: $b"
done
UN --preview --json --full --delete-certificates --delete-backups --delete-audit --delete-tunnel-configs --bcc-state-file /var/lib/baft-bcc/state.db >"$WORK/clean-default.json"
for p in ex ir; do
  UN --preview --json --full --delete-certificates --delete-backups --delete-audit --delete-tunnel-configs \
    --prefix /opt/baft-$p --config-dir /etc/baft-$p --state-dir /var/lib/baft-$p >"$WORK/clean-$p.json"
done
python3 - "$WORK" <<'PY' || fail "not clean: $(cat "$WORK/clean.txt")"
import json, os, sys
work = sys.argv[1]
untouched, problems = set(), []
for n in ("default", "ex", "ir"):
    p = json.load(open(f"{work}/clean-{n}.json"))
    if p["remove"]:
        problems.append(f"{n}: still eligible: " + ", ".join(a["path"] for a in p["remove"]))
    for a in p["keep"]:
        problems.append(f"{n}: a proven BAFT artifact is kept: {a['path']} ({a['reason']})")
    for a in p["untouched"] + p["hold"]:
        untouched.add(a["path"])
roots = ["/etc/baft-ex", "/etc/baft-ir", "/var/lib/baft-ex", "/var/lib/baft-ir", "/opt/baft-ex", "/opt/baft-ir", "/opt/baft",
         "/etc/baft", "/var/lib/baft", "/etc/baft-agent", "/var/lib/baft-agent"]
def covered(path):
    return any(path == u or path.startswith(u + "/") for u in untouched)
survivors = []
for r in roots:
    if not os.path.lexists(r):
        continue
    for dirpath, dirnames, filenames in os.walk(r):
        for name in filenames + [d for d in dirnames if os.path.islink(os.path.join(dirpath, d))]:
            path = os.path.join(dirpath, name)
            if covered(path):
                survivors.append(path)
            else:
                problems.append(f"left but not reported as UNKNOWN/unmanaged: {path}")
        for d in list(dirnames):
            full = os.path.join(dirpath, d)
            if not os.path.islink(full) and not covered(full) and not any(s.startswith(full + "/") for s in untouched):
                if not any(True for _ in os.scandir(full)):
                    problems.append(f"empty BAFT directory left: {full}")
    if not covered(r) and not any(u.startswith(r + "/") for u in untouched):
        problems.append(f"{r} left although nothing in it is preserved")
for f in ("/var/lib/baft-bcc/state.db.audit.jsonl", "/var/lib/baft-bcc/state.db.lock"):
    if os.path.lexists(f):
        problems.append(f"BCC file left: {f}")
open(f"{work}/clean.txt", "w").write("\n".join(problems))
print("preserved (UNKNOWN / unmanaged):", len(survivors))
for s in sorted(survivors)[:20]:
    print("  ", s)
sys.exit(1 if problems else 0)
PY
[[ "$(units)" == "baft-hand.service " ]] || fail "units left: $(units)"
[[ -d "$BKDIR" ]] || fail "the emergency backup was removed"
hand_ok

log "fresh reinstall after the purge pairs anew and passes traffic"
pair_fresh "$REL2"
traffic
hand_ok
log "PASS"
