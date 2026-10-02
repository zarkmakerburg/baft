#!/usr/bin/env bash
# Enrolls this host as a BCC server the way SSH bootstrap will: a real
# baft-bcc, then `install.sh --agent-only` from a signed release (built with
# throwaway keys by scripts/release/local_release.sh), then a deploy job that
# the systemd-run agent must pull, verify and act on. Meant for a throwaway
# CI runner with systemd.
#
#   sudo tests/e2e/agent_enroll.sh <release-dir from local_release.sh>
set -Eeuo pipefail
cd "$(dirname "$0")/../.."
[[ "$EUID" -eq 0 ]] || { echo "run as root"; exit 1; }
REL="$(cd "${1:?usage: agent_enroll.sh <release-dir>}" && pwd)"
VERSION="$(python3 -c 'import json,base64,sys;e=json.load(open(sys.argv[1]));print(json.loads(base64.b64decode(e["payload"]))["version"])' "$REL/dist/manifest.json")"
WORK="$(mktemp -d)"
PIDS=()
log() { printf '[agent-e2e] %s\n' "$*"; }
cleanup() {
  local rc=$?
  if [[ $rc -ne 0 ]]; then
    for f in "$WORK"/*.log; do [[ -f "$f" ]] && { echo "== $f"; tail -n 40 "$f"; }; done
    journalctl -u baft-agent -n 60 --no-pager 2>/dev/null || true
    if [[ "${GITHUB_ACTIONS:-}" == "true" ]]; then
      journalctl -u baft-agent -n 15 --no-pager 2>/dev/null | sed 's/%/%25/g; s/^/::error title=baft-agent::/' || true
      for f in "$WORK"/*.log; do [[ -f "$f" ]] && tail -n 10 "$f" | sed "s/%/%25/g; s|^|::error title=$(basename "$f")::|"; done
    fi
  fi
  systemctl stop baft-agent 2>/dev/null || true
  for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null || true; done
  rm -rf "$WORK"
  exit $rc
}
trap cleanup EXIT

log "release $VERSION served over HTTP in the layout the agent fetches"
mkdir -p "$WORK/www/$VERSION"
cp "$REL"/dist/* "$WORK/www/$VERSION/"
cp "$REL/revocations.json" "$WORK/www/"
(cd "$WORK/www" && exec python3 -m http.server 18201 --bind 127.0.0.1) >"$WORK/www.log" 2>&1 & PIDS+=($!)

log "BCC"
go build -o "$WORK/baft-bcc" ./cmd/baft-bcc
echo admintok >"$WORK/admin.token"; chmod 600 "$WORK/admin.token"
"$WORK/baft-bcc" access init --access-file "$WORK/s.json.access.json" >/dev/null
AGENT_TOKEN="$(head -c 24 /dev/urandom | base64 | tr -d '/+=')"
AGENT_TOK="$AGENT_TOKEN" "$WORK/baft-bcc" --admin-token-file "$WORK/admin.token" --state-file "$WORK/s.json" \
  --listen 127.0.0.1:18200 >"$WORK/bcc.log" 2>&1 & PIDS+=($!)
for _ in $(seq 1 30); do curl -fsS -o /dev/null -H 'Authorization: Bearer admintok' http://127.0.0.1:18200/api/nodes && break; sleep 1; done
JOB_KEY="$("$WORK/baft-bcc" jobkey show --job-key-file "$WORK/s.json.job-key")"
api() { curl -fsS -H 'Authorization: Bearer admintok' "$@"; }
api -d '{"ID":"ex-e2e","Alias":"EX e2e","Address":"127.0.0.1:1","Role":"foreign","AgentTokenEnv":"AGENT_TOK"}' http://127.0.0.1:18200/api/nodes >/dev/null

log "install.sh --agent-only from the signed release"
printf '%s\n' "$AGENT_TOKEN" >"$WORK/token"; chmod 600 "$WORK/token"
env BAFT_INSTALL_FROM=release BAFT_RELEASE_URL="file://$REL/dist" BAFT_ROOT_PUB="$(cat "$REL/root.pub")" \
  BAFT_REVOCATIONS_URL="file://$REL/revocations.json" BAFT_BCC_JOB_KEY="$JOB_KEY" BAFT_AGENT_TOKEN_FILE="$WORK/token" \
  BAFT_AGENT_ALLOW_HTTP=1 BAFT_AGENT_RELEASE_BASE_URL=http://127.0.0.1:18201 \
  BAFT_AGENT_REVOCATIONS_URL=http://127.0.0.1:18201/revocations.json \
  bash install.sh --agent-only --bcc-url http://127.0.0.1:18200 --node-id ex-e2e >"$WORK/install.log" 2>&1
systemctl is-active baft-agent
[[ "$(stat -c '%U %a' /etc/baft-agent/token)" == "root 600" ]] || { echo "agent token not root-only"; exit 1; }

log "a deploy job must be pulled, verified and acted on by the systemd agent"
api -d "{\"node_ids\":[\"ex-e2e\"],\"version\":\"$VERSION\"}" http://127.0.0.1:18200/api/deploy >/dev/null
status=""
for _ in $(seq 1 60); do
  status="$(api http://127.0.0.1:18200/api/jobs | python3 -c 'import json,sys;j=json.load(sys.stdin);print(j[0]["status"]+"|"+j[0].get("message",""))')"
  case "$status" in succeeded*|failed*) break ;; esac
  sleep 2
done
log "job: $status"
# No tunnel service exists yet on this host, so the update cannot restart
# baft and must roll back; either way the agent verified and ran the job.
case "$status" in
  succeeded*|failed*"rolled back"*|failed*"not healthy"*) ;;
  *) echo "the agent did not run the signed job"; exit 1 ;;
esac
journalctl -u baft-agent --no-pager | grep -q "update_baft" || { echo "agent log has no update_baft"; exit 1; }
log "PASS"
