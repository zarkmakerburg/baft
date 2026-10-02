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
fail() { echo "FAIL: $*" >&2; exit 1; }

cleanup() {
  local rc=$?
  if [[ $rc -ne 0 ]]; then
    for f in "$WORK"/*.log; do [[ -f "$f" ]] && { echo "== $f"; tail -n 40 "$f"; }; done
    for u in "${UNITS[@]}"; do echo "== journal $u"; journalctl -u "$u" -n 40 --no-pager 2>/dev/null || true; done
    curl -fsS -H 'Authorization: Bearer admintok' "$BCC/api/tunnels" 2>/dev/null || true
    if [[ "${GITHUB_ACTIONS:-}" == "true" ]]; then
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
[[ "$(drift_check "$T1")" == "IN_SYNC" ]] || fail "the restored config is not IN_SYNC"

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
log "PASS"
