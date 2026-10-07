#!/usr/bin/env bash
# Launch-1 end to end on one exact commit, on a throwaway systemd host that
# plays two servers (EX and IR) side by side:
#
#   signed release of this commit -> real baft-bcc -> install.sh --agent-only
#   on both "servers" -> BCC builds the tunnel -> real traffic through it ->
#   a second change that must fail is rolled back on both sides and the
#   first tunnel keeps carrying traffic -> no secret left behind.
#
#   sudo tests/e2e/launch1.sh <release-dir from local_release.sh>
set -Eeuo pipefail
cd "$(dirname "$0")/../.."
[[ "$EUID" -eq 0 ]] || { echo "run as root"; exit 1; }
REL="$(cd "${1:?usage: launch1.sh <release-dir>}" && pwd)"
VERSION="$(python3 -c 'import json,base64,sys;e=json.load(open(sys.argv[1]));print(json.loads(base64.b64decode(e["payload"]))["version"])' "$REL/dist/manifest.json")"
COMMIT="$(python3 -c 'import json,base64,sys;e=json.load(open(sys.argv[1]));print(json.loads(base64.b64decode(e["payload"]))["commit"])' "$REL/dist/manifest.json")"
case "$(uname -m)" in x86_64) ARCH=amd64 ;; aarch64) ARCH=arm64 ;; *) echo "unsupported arch"; exit 1 ;; esac
WORK="$(mktemp -d)"
PIDS=()
BCC=http://127.0.0.1:18200
UNITS=(baft-agent-ex baft-agent-ir baft-ex baft-ir)
log() { printf '[launch1] %s\n' "$*"; }
fail() {
  echo "FAIL: $*" >&2
  # GitHub keeps only the first 10 error annotations of a step: the reason first.
  [[ "${GITHUB_ACTIONS:-}" == "true" ]] && printf '%s\n' "$*" | head -c 3000 | tr '\n' ' ' | sed 's/%/%25/g; s/^/::error title=FAIL::/; s/$/\n/'
  exit 1
}

cleanup() {
  local rc=$?
  if [[ $rc -ne 0 ]]; then
    for f in "$WORK"/*.log; do [[ -f "$f" ]] && { echo "== $f"; tail -n 40 "$f"; }; done
    for u in "${UNITS[@]}"; do echo "== journal $u"; journalctl -u "$u" -n 40 --no-pager 2>/dev/null || true; done
    curl -fsS -H 'Authorization: Bearer admintok' "$BCC/api/tunnels" 2>/dev/null || true
    if [[ "${GITHUB_ACTIONS:-}" == "true" ]]; then
      curl -fsS -H 'Authorization: Bearer admintok' "$BCC/api/cert-rotations" 2>/dev/null | head -c 6000 | sed 's/%/%25/g; s/^/::error title=rotations::/' || true
      for u in baft-agent-ir baft-agent-ex; do
        journalctl -u "$u" --no-pager 2>/dev/null | grep -E 'tunnel_cert' | tail -n 3 | cut -c1-1500 | sed "s/%/%25/g; s|^|::error title=$u-cert::|" || true
      done
      for u in "${UNITS[@]}"; do journalctl -u "$u" -n 12 --no-pager 2>/dev/null | sed "s/%/%25/g; s|^|::error title=$u::|" || true; done
      for f in "$WORK"/*.log; do [[ -f "$f" ]] && tail -n 8 "$f" | sed "s/%/%25/g; s|^|::error title=$(basename "$f")::|"; done
      curl -fsS -H 'Authorization: Bearer admintok' "$BCC/api/tunnels" 2>/dev/null | sed 's/%/%25/g; s/^/::error title=tunnels::/' || true
    fi
  fi
  for u in "${UNITS[@]}"; do systemctl stop "$u" 2>/dev/null || true; systemctl disable "$u" 2>/dev/null || true; rm -f "/etc/systemd/system/$u.service"; done
  systemctl daemon-reload 2>/dev/null || true
  for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null || true; done
  rm -rf "$WORK" /etc/baft-ex /etc/baft-ir /etc/baft-agent-ex /etc/baft-agent-ir /var/lib/baft-ex /var/lib/baft-ir /var/lib/baft-agent-ex /var/lib/baft-agent-ir
  exit $rc
}
trap cleanup EXIT

api() { curl -fsS -H 'Authorization: Bearer admintok' "$@"; }
jfield() { python3 -c 'import json,sys;d=json.load(sys.stdin);print(d'"$1"')'; }

log "release $VERSION built from $COMMIT"
[[ "$COMMIT" == "$(git rev-parse HEAD)" ]] || fail "the release was not built from the checked-out commit"
mkdir -p "$WORK/www/$VERSION"
cp "$REL"/dist/* "$WORK/www/$VERSION/"
cp "$REL/revocations.json" "$WORK/www/"
(cd "$WORK/www" && exec python3 -m http.server 18201 --bind 127.0.0.1) >"$WORK/www.log" 2>&1 & PIDS+=($!)

log "BCC (the release's own baft-bcc binary)"
BCC_BIN="$WORK/baft-bcc"; install -m 0755 "$REL/dist/baft-bcc-linux-$ARCH" "$BCC_BIN"
echo admintok >"$WORK/admin.token"; chmod 600 "$WORK/admin.token"
"$BCC_BIN" access init --access-file "$WORK/s.json.access.json" >/dev/null
TOK_EX="$(head -c 24 /dev/urandom | base64 | tr -d '/+=')"; TOK_IR="$(head -c 24 /dev/urandom | base64 | tr -d '/+=')"
TOK_EX="$TOK_EX" TOK_IR="$TOK_IR" "$BCC_BIN" --admin-token-file "$WORK/admin.token" --state-file "$WORK/s.json" \
  --listen 127.0.0.1:18200 >"$WORK/bcc.log" 2>&1 & PIDS+=($!)
for _ in $(seq 1 30); do api -o /dev/null "$BCC/api/nodes" 2>/dev/null && break; sleep 1; done
JOB_KEY="$("$BCC_BIN" jobkey show --job-key-file "$WORK/s.json.job-key")"
api -d '{"ID":"ex-e2e","Alias":"EX","Address":"127.0.0.1:22","Role":"foreign","AgentTokenEnv":"TOK_EX"}' "$BCC/api/nodes" >/dev/null
api -d '{"ID":"ir-e2e","Alias":"IR","Address":"127.0.0.1:22","Role":"worker","AgentTokenEnv":"TOK_IR"}' "$BCC/api/nodes" >/dev/null

enroll() { # name token metrics-port
  local n="$1" tok="$2" mport="$3"
  printf '%s\n' "$tok" >"$WORK/token-$n"; chmod 600 "$WORK/token-$n"
  env BAFT_INSTALL_FROM=release BAFT_RELEASE_URL="file://$REL/dist" BAFT_ROOT_PUB="$(cat "$REL/root.pub")" \
    BAFT_REVOCATIONS_URL="file://$REL/revocations.json" BAFT_BCC_JOB_KEY="$JOB_KEY" BAFT_AGENT_TOKEN_FILE="$WORK/token-$n" \
    BAFT_AGENT_ALLOW_HTTP=1 BAFT_AGENT_RELEASE_BASE_URL=http://127.0.0.1:18201 \
    BAFT_AGENT_REVOCATIONS_URL=http://127.0.0.1:18201/revocations.json \
    BAFT_AGENT_DIR="/etc/baft-agent-$n" BAFT_AGENT_STATE_DIR="/var/lib/baft-agent-$n" BAFT_AGENT_UNIT="baft-agent-$n" \
    BAFT_AGENT_INTERVAL=2s BAFT_CONFIG_DIR="/etc/baft-$n" BAFT_STATE_DIR="/var/lib/baft-$n" BAFT_SERVICE="baft-$n" \
    BAFT_METRICS_LISTEN="127.0.0.1:$mport" \
    bash install.sh --agent-only --bcc-url "$BCC" --node-id "$n-e2e" >"$WORK/install-$n.log" 2>&1
  systemctl is-active "baft-agent-$n" >/dev/null || fail "agent $n is not running"
}
log "install.sh --agent-only on both servers, from the signed release"
enroll ex "$TOK_EX" 9291
enroll ir "$TOK_IR" 9292
[[ "$(stat -c '%U %a' /etc/baft-agent-ex/token)" == "root 600" ]] || fail "agent token not root-only"

python3 tests/e2e/echo.py serve 2443 >"$WORK/target.log" 2>&1 & PIDS+=($!)
sleep 1

wait_tunnel() { # id, seconds, accepted phases...
  local id="$1" secs="$2"; shift 2
  local phase="" i
  for ((i = 0; i < secs / 2; i++)); do
    phase="$(api "$BCC/api/tunnels?id=$id" | jfield '["phase"]')"
    for want in "$@"; do [[ "$phase" == "$want" ]] && { echo "$phase"; return 0; }; done
    sleep 2
  done
  echo "$phase"; return 1
}
drift_check() { # id -> prints the drift state once both nodes answered
  local id="$1" i out
  api -X POST "$BCC/api/tunnels/drift?id=$id" >/dev/null
  for ((i = 0; i < 60; i++)); do
    out="$(api "$BCC/api/tunnels?id=$id" | python3 -c 'import json,sys;d=json.load(sys.stdin);print(len(d.get("drift_jobs",[])),(d.get("drift") or {}).get("state",""))')"
    [[ "${out%% *}" == 0 ]] && { echo "${out#* }"; return 0; }
    sleep 2
  done
  echo "$out"; return 1
}
traffic() { # retry: the dialer reconnects within a few seconds of a restart
  local i
  for i in $(seq 1 15); do
    python3 tests/e2e/echo.py send 1443 2 2 >"$WORK/traffic.log" 2>&1 && return 0
    sleep 2
  done
  return 1
}

log "BCC builds tunnel 1 (EX 18443 <-> IR 1443 -> target 2443)"
REQ1='{"ex_node":"ex-e2e","ir_node":"ir-e2e","public_address":"127.0.0.1","port":18443}'
# The plan is reviewed first and deployed by its hash; its gates need both
# agents to have contacted BCC, which they do every 2 s.
PLAN_OK=False
for _ in $(seq 1 30); do
  PLAN="$(api -d "$REQ1" "$BCC/api/tunnels/plan")"
  PLAN_OK="$(printf '%s' "$PLAN" | jfield '["ok"]')"
  [[ "$PLAN_OK" == "True" ]] && break
  sleep 1
done
[[ "$PLAN_OK" == "True" ]] || fail "the deployment plan never passed its gates: $PLAN"
HASH1="$(printf '%s' "$PLAN" | jfield '["hash"]')"
T1="$(api -d "${REQ1%\}},\"plan_hash\":\"$HASH1\"}" "$BCC/api/tunnels" | jfield '["id"]')"
phase="$(wait_tunnel "$T1" 240 active rolled_back rollback_failed)" || true
[[ "$phase" == "active" ]] || fail "tunnel 1 ended as '$phase'"
systemctl is-active baft-ex baft-ir >/dev/null || fail "tunnel services are not running"
[[ "$(stat -c '%U:%G %a' /etc/baft-ex/baft.yaml)" == "root:baft 640" ]] || fail "EX config has wrong owner/mode"
python3 - "$(api "$BCC/api/tunnels?id=$T1")" "$HASH1" <<'PY' || fail "tunnel 1 lacks its deployment evidence"
import json, sys
t = json.loads(sys.argv[1])
ev = t.get("evidence", [])
assert t["plan_hash"] == sys.argv[2], "plan hash not recorded"
assert sorted(e["step"] for e in ev) == ["health", "health", "observe", "observe"], ev
assert all(e["ok"] for e in ev), ev
assert all('"generation":1' in e["detail"] for e in ev if e["step"] == "observe"), ev
PY
traffic || fail "no traffic through tunnel 1"
log "traffic passes through tunnel 1"
log "drift detection: in sync, then a hand edit, then fixed"
[[ "$(drift_check "$T1")" == "IN_SYNC" ]] || fail "a fresh tunnel is not IN_SYNC"
cp /etc/baft-ex/baft.yaml "$WORK/ex.yaml.keep"
printf '\n# edited by hand\n' >> /etc/baft-ex/baft.yaml
[[ "$(drift_check "$T1")" == "DRIFTED" ]] || fail "a hand-edited config was not reported as DRIFTED"
cat "$WORK/ex.yaml.keep" > /etc/baft-ex/baft.yaml
# Coupled tamper: edit the config AND rewrite the marker's hash to match. Every
# node-side comparison passes; BCC's own verified digest must still catch it.
cp /etc/baft-ex/baft.managed.json "$WORK/ex.marker.keep"
printf '\n# edited by hand, marker rewritten\n' >> /etc/baft-ex/baft.yaml
python3 - <<'PY'
import json, hashlib
p = "/etc/baft-ex/baft.managed.json"
m = json.load(open(p))
m["config_sha256"] = hashlib.sha256(open("/etc/baft-ex/baft.yaml", "rb").read()).hexdigest()
json.dump(m, open(p, "w"))
PY
[[ "$(drift_check "$T1")" == "DRIFTED" ]] || fail "a config edit with a matching marker rewrite was not reported as DRIFTED"
cat "$WORK/ex.yaml.keep" > /etc/baft-ex/baft.yaml
cat "$WORK/ex.marker.keep" > /etc/baft-ex/baft.managed.json
[[ "$(drift_check "$T1")" == "IN_SYNC" ]] || fail "the restored config is not IN_SYNC"

log "baft menu on a real terminal; direct commands stay plain"
python3 - <<'PY' || fail "the baft menu did not behave on a terminal"
import os, pty, re, select, struct, fcntl, termios, subprocess, time
def run_menu(env, cols):
    pid, fd = pty.fork()
    if pid == 0:
        e = {"PATH": os.environ["PATH"], "HOME": "/tmp"}; e.update(env)
        os.execve("/usr/local/bin/baft", ["baft", "menu", "--file", "/etc/baft-ex/baft.yaml", "--service", "baft-ex", "--release-state", "/opt/baft/release-state.json"], e)
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 40, cols, 0, 0))
    out = b""
    def drain(t):
        nonlocal out
        end = time.time() + t
        while time.time() < end:
            r, _, _ = select.select([fd], [], [], 0.1)
            if r:
                try: d = os.read(fd, 65536)
                except OSError: return
                if not d: return
                out += d
    drain(1.5)
    for line in (b"11\n", b"\n", b"0\n"):
        os.write(fd, line); drain(0.7)
    _, status = os.waitpid(pid, 0)
    return out.decode(errors="replace"), os.WEXITSTATUS(status)
colored, rc = run_menu({"TERM": "xterm-256color", "COLORTERM": "truecolor", "LANG": "C.UTF-8"}, 120)
assert rc == 0, rc
assert "\x1b[" in colored and "B  A  F  T" in re.sub(r"\x1b\[[0-9;]*m", "", colored), "no gold header on a color terminal"
seen = re.sub(r"\x1b\[[0-9;]*m", "", colored)
assert "Update (planned)" in seen and "not available in this version yet" in seen, seen[:500]
plain, rc = run_menu({"TERM": "xterm", "NO_COLOR": "1", "LANG": "C"}, 60)
assert rc == 0 and "\x1b[" not in plain, "NO_COLOR was not respected"
assert "BAFT | v" in plain, plain[:300]
# Direct commands print no splash and no escape sequence, even when asked on a terminal-capable host.
for args in (["status", "--json"], ["doctor", "--json"], ["version", "--json"]):
    out = subprocess.run(["/usr/local/bin/baft"] + args + (["--file", "/etc/baft-ex/baft.yaml", "--service", "baft-ex"] if args[0] != "version" else []),
                         capture_output=True, text=True).stdout
    assert "\x1b" not in out and "Resilient Network Fabric" not in out, args
PY

log "discovery (report only): both nodes MANAGED, nothing on the hosts changes"
dc_sum() { sha256sum /etc/baft-ex/baft.yaml /etc/baft-ir/baft.yaml /etc/baft-ex/baft.managed.json /etc/baft-ir/baft.managed.json \
  /etc/systemd/system/baft-ex.service /etc/systemd/system/baft-ir.service | sha256sum; }
DC_BEFORE="$(dc_sum)"
api -X POST "$BCC/api/discovery?all=1" >/dev/null
for _ in $(seq 1 60); do
  if api "$BCC/api/discovery" | python3 -c 'import json,sys;d=json.load(sys.stdin)["nodes"];sys.exit(0 if d and all(not n.get("pending") and n.get("at") for n in d) else 1)'; then break; fi
  sleep 2
done
api "$BCC/api/discovery" | python3 -c '
import json, sys
nodes = {n["node_id"]: n for n in json.load(sys.stdin)["nodes"]}
for nid in ("ex-e2e", "ir-e2e"):
    n = nodes[nid]
    assert not n.get("problem"), (nid, n)
    prim = [v for v in n["instances"] if v["primary"]]
    assert len(prim) == 1 and prim[0]["state"] == "MANAGED", (nid, n["instances"])
    assert not any(v["state"] == "MISSING" for v in n["instances"]), (nid, n["instances"])
' || fail "discovery did not report both nodes as MANAGED"
[[ "$(dc_sum)" == "$DC_BEFORE" ]] || fail "discovery changed a config, marker or unit on the host"
systemctl is-active baft-ex baft-ir >/dev/null || fail "discovery disturbed the tunnel services"
traffic || fail "discovery disturbed the traffic"

log "certificate rotation (A4): PREPARE -> DISTRIBUTE -> VERIFY -> ACTIVATE -> CONFIRM -> RETIRE_OLD on real services"
IR_CA="/var/lib/baft-ir/tunnels/$T1/peer-ca.pem"
served_sha() { python3 -c 'import ssl,hashlib;print(hashlib.sha256(ssl.PEM_cert_to_DER_cert(ssl.get_server_certificate(("127.0.0.1",18443)))).hexdigest())'; }
# The invariant at every poll: the running EX verifies against the IR's
# trust file, and the files both would boot from verify too.
trusted() {
  # A listener that is restarting refuses the connection; that is not a
  # trust failure. A completed handshake must verify.
  if ! openssl s_client -connect 127.0.0.1:18443 -CAfile "$IR_CA" -verify_return_error -verify_ip 127.0.0.1 </dev/null >"$WORK/sclient.log" 2>&1; then
    grep -qiE 'connect:errno|connection refused' "$WORK/sclient.log" || { cat "$WORK/sclient.log"; return 1; }
  fi
  openssl verify -CAfile "$IR_CA" -verify_ip 127.0.0.1 /etc/baft-ex/pki/server.pem >/dev/null 2>&1
}
wait_rotation() { # id seconds wanted-phases... ; checks the invariant every poll
  local id="$1" secs="$2"; shift 2
  local phase="" i
  # Every 3 s: BCC rate-limits /api/ per address and the agents share it.
  for ((i = 0; i < secs / 3; i++)); do
    trusted || fail "the IR could not verify the EX during rotation (phase $phase)"
    phase="$(api "$BCC/api/cert-rotations?id=$id" | jfield '["phase"]')"
    # wait_rotation runs in a command substitution: record in a file.
    [[ "$(grep -c 'BEGIN CERTIFICATE' "$IR_CA")" == 2 ]] && touch "$WORK/saw-window"
    for want in "$@"; do [[ "$phase" == "$want" ]] && { echo "$phase"; return 0; }; done
    sleep 3
  done
  echo "$phase"; return 1
}
trusted || fail "the IR does not verify the EX before rotation"
OLD_LEAF="$(served_sha)"
rm -f "$WORK/saw-window"
R1="$(api -d "{\"tunnel_id\":\"$T1\"}" "$BCC/api/tunnels/rotate-cert" | jfield '["id"]')"
phase="$(wait_rotation "$R1" 300 complete rolled_back rollback_failed retire_failed)" || true
[[ "$phase" == "complete" ]] || fail "rotation ended as '$phase': $(api "$BCC/api/cert-rotations?id=$R1")"
[[ -e "$WORK/saw-window" ]] || fail "the IR never trusted old + new during the window"
NEW_LEAF="$(served_sha)"
[[ "$NEW_LEAF" != "$OLD_LEAF" ]] || fail "the EX still serves the old certificate"
[[ "$(grep -c 'BEGIN CERTIFICATE' "$IR_CA")" == 1 ]] || fail "the IR still trusts more than the new CA"
ls -d /etc/baft-ex/pki.prev-* /etc/baft-ex/pki.next-* 2>/dev/null && fail "old certificate material survived RETIRE"
python3 - "$(api "$BCC/api/cert-rotations?id=$R1")" "$(api "$BCC/api/tunnels?id=$T1")" "$NEW_LEAF" "$OLD_LEAF" <<'PY' || fail "rotation evidence or tunnel epoch is wrong"
import json, sys
r, t, new, old = json.loads(sys.argv[1]), json.loads(sys.argv[2]), sys.argv[3], sys.argv[4]
assert r["cert_sha256"] == new and r["old_cert_sha256"] == old and r["epoch"] == 1, r
steps = [(e["step"], e["node"]) for e in r["evidence"]]
for want in [("preparing", "ex-e2e"), ("distributing", "ir-e2e"), ("verifying_ir", "ir-e2e"), ("verifying_ex", "ex-e2e"),
             ("activating", "ex-e2e"), ("confirming", "ir-e2e"), ("retiring_ir", "ir-e2e"), ("retiring_ex", "ex-e2e")]:
    assert want in steps, (want, steps)
assert all(e["ok"] for e in r["evidence"]), r["evidence"]
assert t["cert_epoch"] == 1 and t["cert_sha256"] == new and t["phase"] == "active", t
PY
grep -q '"cert.rotation.complete"' "$WORK"/s.json.audit.jsonl || fail "audit lacks cert.rotation.complete"
if grep -aq 'PRIVATE KEY' "$WORK"/s.json*; then fail "a private key reached BCC's state"; fi
systemctl is-active baft-ex baft-ir >/dev/null || fail "tunnel services are not running after the rotation"
traffic || fail "no traffic through tunnel 1 after the rotation"
[[ "$(drift_check "$T1")" == "IN_SYNC" ]] || fail "the rotation made the tunnel drift"
log "rotation complete: new certificate served and trusted alone; traffic passes"

log "a cancelled rotation (after CONFIRM, during HOLD) rolls back EX then IR to the working certificate"
rm -f "$WORK/saw-window"
R2="$(api -d "{\"tunnel_id\":\"$T1\",\"hold_seconds\":3600}" "$BCC/api/tunnels/rotate-cert" | jfield '["id"]')"
phase="$(wait_rotation "$R2" 300 holding rolled_back rollback_failed)" || true
[[ "$phase" == "holding" ]] || fail "rotation 2 ended as '$phase'"
[[ "$(served_sha)" != "$NEW_LEAF" ]] || fail "rotation 2 did not activate"
api -d "{\"id\":\"$R2\",\"reason\":\"e2e\"}" "$BCC/api/cert-rotations/cancel" >/dev/null
phase="$(wait_rotation "$R2" 300 rolled_back rollback_failed)" || true
[[ "$phase" == "rolled_back" ]] || fail "cancelled rotation ended as '$phase': $(api "$BCC/api/cert-rotations?id=$R2")"
[[ "$(served_sha)" == "$NEW_LEAF" ]] || fail "the working certificate was not restored"
[[ "$(grep -c 'BEGIN CERTIFICATE' "$IR_CA")" == 1 ]] || fail "the IR kept the cancelled CA"
grep -q '"cert.rotation.rolled_back"' "$WORK"/s.json.audit.jsonl || fail "audit lacks cert.rotation.rolled_back"
traffic || fail "no traffic after the cancelled rotation"
log "cancelled rotation rolled back; traffic passes"

EX_CFG_SUM="$(sha256sum /etc/baft-ex/baft.yaml | cut -d' ' -f1)"
IR_CFG_SUM="$(sha256sum /etc/baft-ir/baft.yaml | cut -d' ' -f1)"
EX_MARK_SUM="$(sha256sum /etc/baft-ex/baft.managed.json | cut -d' ' -f1)"
python3 - "$T1" <<'PY' || fail "ownership marker of tunnel 1 is wrong"
import json, hashlib, sys
for d in ("/etc/baft-ex", "/etc/baft-ir"):
    m = json.load(open(d + "/baft.managed.json"))
    assert m["managed_by"] == "baft" and m["tunnel_id"] == sys.argv[1] and m["generation"] == 1, m
    assert m["config_sha256"] == hashlib.sha256(open(d + "/baft.yaml", "rb").read()).hexdigest(), "config sha"
PY

log "a bad change (EX port taken by BCC) must roll back on both sides"
T2="$(api -d '{"ex_node":"ex-e2e","ir_node":"ir-e2e","public_address":"127.0.0.1","port":18200}' "$BCC/api/tunnels" | jfield '["id"]')"
phase="$(wait_tunnel "$T2" 300 active rolled_back rollback_failed)" || true
[[ "$phase" == "rolled_back" ]] || fail "tunnel 2 ended as '$phase', want rolled_back"
[[ "$(sha256sum /etc/baft-ex/baft.yaml | cut -d' ' -f1)" == "$EX_CFG_SUM" ]] || fail "EX config was not restored"
[[ "$(sha256sum /etc/baft-ir/baft.yaml | cut -d' ' -f1)" == "$IR_CFG_SUM" ]] || fail "IR config was not restored"
[[ "$(api "$BCC/api/tunnels?id=$T1" | jfield '["phase"]')" == "active" ]] || fail "tunnel 1 lost its state"
[[ "$(sha256sum /etc/baft-ex/baft.managed.json | cut -d' ' -f1)" == "$EX_MARK_SUM" ]] || fail "EX ownership marker was not restored"
systemctl is-active baft-ex baft-ir >/dev/null || fail "tunnel services are not running after the rollback"
traffic || fail "tunnel 1 no longer carries traffic after the rollback"
log "tunnel 1 still carries traffic after the rolled-back change"

log "no secret left behind"
if grep -aqE 'BAFTPAIR1:|BAFTREPLY1:' "$WORK"/s.json* ; then fail "a pairing code is still in BCC's state"; fi
if api "$BCC/api/jobs" | grep -qE 'BAFTPAIR1:|BAFTREPLY1:'; then fail "a pairing code is listed by /api/jobs"; fi
for n in ex ir; do
  if find "/var/lib/baft-$n/tunnels" -name psk -o -name pending.json -o -name pairing.pending.json | grep -q .; then fail "$n kept one-time pairing files"; fi
done
log "support bundle from a running node contains no key material"
BUNDLE="$WORK/bundle-ex.tar.gz"
/usr/local/bin/baft support-bundle --service baft-ex --file /etc/baft-ex/baft.yaml --release-state /opt/baft/release-state.json --out "$BUNDLE" >/dev/null \
  || fail "baft support-bundle failed"
[[ "$(stat -c '%a' "$BUNDLE")" == "600" ]] || fail "support bundle is not owner-only"
tar -tzf "$BUNDLE" | grep -qx manifest.json || fail "support bundle has no manifest"
python3 - "$BUNDLE" /etc/baft-ex/noise-key.json /etc/baft-ex/pki/server.key <<'PY' || fail "support bundle leaks key material"
import re, sys, tarfile
bundle, *keyfiles = sys.argv[1:]
text = b"".join(tarfile.open(bundle).extractfile(m).read() for m in tarfile.open(bundle).getmembers() if m.isfile())
for kf in keyfiles:
    for tok in re.findall(rb"[A-Za-z0-9_+/=-]{32,}", open(kf, "rb").read()):
        if tok in text:
            print("leaked from", kf); sys.exit(1)
PY
log "M-018C: decommission the legacy test tunnel before the scoped-instance lifecycle test"
DPLAN="$(api -d "{\"id\":\"$T1\"}" "$BCC/api/tunnels/decommission/plan")"
[[ "$(printf '%s' "$DPLAN" | jfield '["ok"]')" == "True" ]] || fail "legacy decommission plan is blocked: $DPLAN"
DHASH="$(printf '%s' "$DPLAN" | jfield '["hash"]')"
api -d "{\"id\":\"$T1\",\"plan_hash\":\"$DHASH\"}" "$BCC/api/tunnels/decommission" >/dev/null
phase="$(wait_tunnel "$T1" 240 decommissioned decommission_failed)" || true
[[ "$phase" == "decommissioned" ]] || fail "legacy tunnel decommission ended as '$phase'"
python3 - <<'PY' || fail "legacy decommission left route/listener reachable"
import socket
for port in (1443, 18443):
    sock=socket.socket(); sock.settimeout(.5)
    try:
        rc=sock.connect_ex(("127.0.0.1", port))
    finally:
        sock.close()
    assert rc != 0, port
PY

log "M-018C: two instance-scoped sibling tunnels carry traffic independently"
UNITS+=(baft-ex-slot-a baft-ir-slot-a baft-ex-slot-b baft-ir-slot-b)
REQ_A='{"instance_id":"slot-a","ex_node":"ex-e2e","ir_node":"ir-e2e","public_address":"127.0.0.1","port":19443,"route_listen":"127.0.0.1:15443","route_id":"service-a","ex_metrics_listen":"127.0.0.1:9391","ir_metrics_listen":"127.0.0.1:9392"}'
REQ_B='{"instance_id":"slot-b","ex_node":"ex-e2e","ir_node":"ir-e2e","public_address":"127.0.0.1","port":19444,"route_listen":"127.0.0.1:15444","route_id":"service-b","ex_metrics_listen":"127.0.0.1:9393","ir_metrics_listen":"127.0.0.1:9394"}'
deploy_scoped() {
  local req="$1" plan hash id phase
  plan="$(api -d "$req" "$BCC/api/tunnels/plan")"
  [[ "$(printf '%s' "$plan" | jfield '["ok"]')" == "True" ]] || fail "scoped deployment plan blocked: $plan"
  hash="$(printf '%s' "$plan" | jfield '["hash"]')"
  id="$(api -d "${req%\}},\"plan_hash\":\"$hash\"}" "$BCC/api/tunnels" | jfield '["id"]')"
  phase="$(wait_tunnel "$id" 240 active rolled_back rollback_failed)" || true
  [[ "$phase" == "active" ]] || fail "scoped tunnel $id ended as '$phase'"
  echo "$id"
}
SA="$(deploy_scoped "$REQ_A")"
SB="$(deploy_scoped "$REQ_B")"
for port in 15443 15444; do
  ok=false
  for _ in $(seq 1 15); do
    if python3 tests/e2e/echo.py send "$port" 2 2 >"$WORK/traffic-$port.log" 2>&1; then ok=true; break; fi
    sleep 2
  done
  [[ "$ok" == true ]] || fail "scoped route $port carries no verified traffic"
done

log "M-018C: external edit must fail closed before any retirement"
cp /etc/baft-ir/instances/slot-a/baft.yaml "$WORK/slot-a-ir.keep"
printf '\n# M-018C external edit\n' >> /etc/baft-ir/instances/slot-a/baft.yaml
DPLAN="$(api -d "{\"id\":\"$SA\"}" "$BCC/api/tunnels/decommission/plan")"
[[ "$(printf '%s' "$DPLAN" | jfield '["ok"]')" == "True" ]] || fail "slot-a pre-drift decommission plan unexpectedly blocked: $DPLAN"
DHASH="$(printf '%s' "$DPLAN" | jfield '["hash"]')"
api -d "{\"id\":\"$SA\",\"plan_hash\":\"$DHASH\"}" "$BCC/api/tunnels/decommission" >/dev/null
phase="$(wait_tunnel "$SA" 120 decommission_failed decommissioned)" || true
[[ "$phase" == "decommission_failed" ]] || fail "externally edited slot-a ended as '$phase', want decommission_failed"
grep -q 'M-018C external edit' /etc/baft-ir/instances/slot-a/baft.yaml || fail "failed decommission did not preserve external edit"
systemctl is-active baft-ir-slot-a >/dev/null || fail "failed decommission stopped the edited IR instance"
python3 tests/e2e/echo.py send 15443 1 1 >"$WORK/traffic-slot-a-after-refusal.log" 2>&1 || fail "failed decommission disrupted slot-a traffic"
cat "$WORK/slot-a-ir.keep" > /etc/baft-ir/instances/slot-a/baft.yaml
log "M-018C: edited material restored; retry decommission from explicit failed state"
log "M-018C: decommission slot-a only; slot-b must remain live"
DPLAN="$(api -d "{\"id\":\"$SA\"}" "$BCC/api/tunnels/decommission/plan")"
[[ "$(printf '%s' "$DPLAN" | jfield '["ok"]')" == "True" ]] || fail "slot-a decommission plan blocked: $DPLAN"
DHASH="$(printf '%s' "$DPLAN" | jfield '["hash"]')"
api -d "{\"id\":\"$SA\",\"plan_hash\":\"$DHASH\"}" "$BCC/api/tunnels/decommission" >/dev/null
phase="$(wait_tunnel "$SA" 240 decommissioned decommission_failed)" || true
[[ "$phase" == "decommissioned" ]] || fail "slot-a decommission ended as '$phase'"
[[ "$(api "$BCC/api/tunnels?id=$SB" | jfield '["phase"]')" == "active" ]] || fail "slot-b lost active state"

python3 - <<'PY' || fail "slot-a endpoints remain reachable or slot-b endpoints disappeared"
import socket
def openp(port):
    sock=socket.socket(); sock.settimeout(.5)
    try: return sock.connect_ex(("127.0.0.1", port)) == 0
    finally: sock.close()
assert not openp(15443), "retired IR route still accepts"
assert not openp(19443), "retired EX listener still accepts"
assert openp(15444), "sibling IR route disappeared"
assert openp(19444), "sibling EX listener disappeared"
PY
python3 tests/e2e/echo.py send 15444 4 2 >"$WORK/traffic-sibling-after-decommission.log" 2>&1 \
  || fail "sibling tunnel stopped carrying verified traffic after slot-a decommission"
[[ ! -e /etc/baft-ex/instances/slot-a/baft.yaml && ! -e /etc/baft-ir/instances/slot-a/baft.yaml ]] \
  || fail "selected slot-a managed config survived decommission"
[[ -e /etc/baft-ex/instances/slot-b/baft.yaml && -e /etc/baft-ir/instances/slot-b/baft.yaml ]] \
  || fail "sibling slot-b managed config was removed"
grep -q '"tunnel.decommissioned"' "$WORK"/s.json.audit.jsonl || fail "audit lacks tunnel.decommissioned"
log "M-018C decommission PASS: selected route/listener gone; sibling traffic still passes"
if grep -aqE 'BAFTPAIR1:|BAFTREPLY1:' "$WORK"/s.json*; then fail "M-018C left a pairing code in BCC state"; fi
for root in /var/lib/baft-ex/instances /var/lib/baft-ir/instances; do
  [[ -d "$root" ]] || continue
  if find "$root" \( -name psk -o -name pending.json -o -name pairing.pending.json \) -print -quit | grep -q .; then fail "M-018C left one-time pairing material in instance state"; fi
done
log "PASS"
