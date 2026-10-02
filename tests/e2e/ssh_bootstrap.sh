#!/usr/bin/env bash
# SSH bootstrap against a real sshd: BCC reads the host key, refuses a wrong
# fingerprint without running anything, then installs baft-agent over SSH
# (install.sh --agent-only from a signed release built with throwaway keys)
# and the enrolled agent runs a signed job. Meant for a throwaway CI runner.
#
#   sudo tests/e2e/ssh_bootstrap.sh <release-dir from local_release.sh>
set -Eeuo pipefail
cd "$(dirname "$0")/../.."
[[ "$EUID" -eq 0 ]] || { echo "run as root"; exit 1; }
REL="$(cd "${1:?usage: ssh_bootstrap.sh <release-dir>}" && pwd)"
VERSION="$(python3 -c 'import json,base64,sys;e=json.load(open(sys.argv[1]));print(json.loads(base64.b64decode(e["payload"]))["version"])' "$REL/dist/manifest.json")"
WORK="$(mktemp -d)"
PIDS=()
BCC=http://127.0.0.1:18300
SSHPORT=2322
log() { printf '[ssh-e2e] %s\n' "$*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }
cleanup() {
  local rc=$?
  if [[ $rc -ne 0 ]]; then
    for f in "$WORK"/*.log; do [[ -f "$f" ]] && { echo "== $f"; tail -n 40 "$f"; }; done
    journalctl -u baft-agent -n 40 --no-pager 2>/dev/null || true
    if [[ "${GITHUB_ACTIONS:-}" == "true" ]]; then
      journalctl -u baft-agent -n 10 --no-pager 2>/dev/null | sed 's/%/%25/g; s/^/::error title=baft-agent::/' || true
      for f in "$WORK"/*.log; do [[ -f "$f" ]] && tail -n 12 "$f" | sed "s/%/%25/g; s|^|::error title=$(basename "$f")::|"; done
    fi
  fi
  systemctl stop baft-agent 2>/dev/null || true; systemctl disable baft-agent 2>/dev/null || true
  rm -f /etc/systemd/system/baft-agent.service; systemctl daemon-reload 2>/dev/null || true
  for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null || true; done
  rm -rf "$WORK" /etc/baft-agent /var/lib/baft-agent
  exit $rc
}
trap cleanup EXIT
api() { curl -fsS -H 'Authorization: Bearer admintok' "$@"; }

command -v sshd >/dev/null || { apt-get update -qq && apt-get install -y -qq openssh-server >/dev/null; }
SSHD="$(command -v sshd || echo /usr/sbin/sshd)"
mkdir -p /run/sshd

log "release $VERSION served over HTTP in the layout the agent fetches"
mkdir -p "$WORK/www/$VERSION"; cp "$REL"/dist/* "$WORK/www/$VERSION/"; cp "$REL/revocations.json" "$WORK/www/"
(cd "$WORK/www" && exec python3 -m http.server 18301 --bind 127.0.0.1) >"$WORK/www.log" 2>&1 & PIDS+=($!)

log "a real sshd on 127.0.0.1:$SSHPORT; the client key carries the installer's test settings"
ssh-keygen -q -t ed25519 -N '' -f "$WORK/hostkey"
ssh-keygen -q -t ed25519 -N '' -f "$WORK/clientkey"
ENVOPTS="environment=\"BAFT_INSTALL_FROM=release\",environment=\"BAFT_RELEASE_URL=file://$REL/dist\",environment=\"BAFT_ROOT_PUB=$(cat "$REL/root.pub")\",environment=\"BAFT_REVOCATIONS_URL=file://$REL/revocations.json\",environment=\"BAFT_AGENT_RELEASE_BASE_URL=http://127.0.0.1:18301\",environment=\"BAFT_AGENT_REVOCATIONS_URL=http://127.0.0.1:18301/revocations.json\""
printf '%s %s\n' "$ENVOPTS" "$(cat "$WORK/clientkey.pub")" >"$WORK/authorized_keys"
cat >"$WORK/sshd_config" <<CFG
Port $SSHPORT
ListenAddress 127.0.0.1
HostKey $WORK/hostkey
PidFile $WORK/sshd.pid
PermitRootLogin yes
PubkeyAuthentication yes
PasswordAuthentication no
KbdInteractiveAuthentication no
AuthorizedKeysFile $WORK/authorized_keys
PermitUserEnvironment yes
StrictModes no
UsePAM no
CFG
"$SSHD" -D -e -f "$WORK/sshd_config" >"$WORK/sshd.log" 2>&1 & PIDS+=($!)
for _ in $(seq 1 20); do (exec 3<>/dev/tcp/127.0.0.1/$SSHPORT) 2>/dev/null && break; sleep 0.5; done

log "BCC with the install script and public URL that enable /api/bootstrap"
BCC_BIN="$WORK/baft-bcc"
case "$(uname -m)" in x86_64) ARCH=amd64 ;; aarch64) ARCH=arm64 ;; *) fail "unsupported arch" ;; esac
install -m 0755 "$REL/dist/baft-bcc-linux-$ARCH" "$BCC_BIN"
echo admintok >"$WORK/admin.token"; chmod 600 "$WORK/admin.token"
"$BCC_BIN" access init --access-file "$WORK/s.json.access.json" >/dev/null
"$BCC_BIN" --admin-token-file "$WORK/admin.token" --state-file "$WORK/s.json" --listen 127.0.0.1:18300 \
  --install-script install.sh --public-url "$BCC" --allow-insecure-http >"$WORK/bcc.log" 2>&1 & PIDS+=($!)
for _ in $(seq 1 30); do api -o /dev/null "$BCC/api/nodes" 2>/dev/null && break; sleep 1; done

log "read the host key and compare it with the real one"
FP="$(api -d "{\"host\":\"127.0.0.1\",\"port\":$SSHPORT}" "$BCC/api/bootstrap/hostkey" | python3 -c 'import json,sys;print(json.load(sys.stdin)["fingerprint"])')"
REAL="$(ssh-keygen -lf "$WORK/hostkey.pub" | awk '{print $2}')"
[[ "$FP" == "$REAL" ]] || fail "BCC read $FP but the server's key is $REAL"

body() { # fingerprint
  python3 - "$1" "$SSHPORT" "$WORK/clientkey" <<'PY'
import json, sys
print(json.dumps({"node_id": "ssh-e2e", "alias": "ssh e2e", "role": "foreign", "host": "127.0.0.1", "port": int(sys.argv[2]),
                  "user": "root", "host_key_fingerprint": sys.argv[1], "private_key": open(sys.argv[3]).read()}))
PY
}

log "a wrong fingerprint must stop before anything runs"
code="$(body 'SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA' | curl -sS -o "$WORK/wrong.out" -w '%{http_code}' -H 'Authorization: Bearer admintok' -d @- "$BCC/api/bootstrap")"
[[ "$code" == "502" ]] || fail "wrong fingerprint answered $code"
grep -q 'host key mismatch' "$WORK/wrong.out" || fail "no mismatch message: $(cat "$WORK/wrong.out")"
[[ ! -e /etc/systemd/system/baft-agent.service ]] || fail "something was installed despite the wrong host key"

log "bootstrap over SSH with the confirmed fingerprint"
body "$FP" | api -d @- "$BCC/api/bootstrap" >"$WORK/bootstrap.json" || { cat "$WORK/bootstrap.json"; fail "bootstrap failed"; }
systemctl is-active baft-agent >/dev/null || fail "baft-agent is not running after the bootstrap"
[[ "$(stat -c '%U %a' /etc/baft-agent/token)" == "root 600" ]] || fail "agent token not root-only"

log "the enrolled agent must run a signed job"
api -d "{\"node_ids\":[\"ssh-e2e\"],\"version\":\"$VERSION\"}" "$BCC/api/deploy" >/dev/null
status=""
for _ in $(seq 1 50); do
  status="$(api "$BCC/api/jobs" | python3 -c 'import json,sys;j=json.load(sys.stdin);print(j[0]["status"]+"|"+j[0].get("message",""))')"
  case "$status" in succeeded*|failed*) break ;; esac
  sleep 2
done
log "job: $status"
case "$status" in
  succeeded*|failed*"rolled back"*|failed*"not healthy"*) ;;
  *) fail "the agent did not run the signed job" ;;
esac
journalctl -u baft-agent --no-pager | grep -q update_baft || fail "agent log has no update_baft"

log "credentials were not kept"
KEYBODY="$(sed -n '2p' "$WORK/clientkey")"
if grep -aqF "$KEYBODY" "$WORK"/s.json* ; then fail "the SSH private key is in BCC's state or audit log"; fi
log "PASS"
