#!/usr/bin/env bash
# Isolated Linux drill against the actual v0.2.0 BCC binary and current HEAD.
# It proves forward migration, refusal by the older binary, and recovery with
# the current binary while retaining a write made after migration.
set -Eeuo pipefail
cd "$(dirname "$0")/../.."
[[ "$(uname -s)" == Linux ]] || { echo "Linux required" >&2; exit 1; }
work="$(mktemp -d)"
old_src="$work/old-src"
server_pid=""
cleanup() {
  if [[ -n "$server_pid" ]]; then kill "$server_pid" 2>/dev/null || true; wait "$server_pid" 2>/dev/null || true; fi
  git worktree remove --force "$old_src" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT
fail() { echo "[bcc-schema-drill] FAIL: $*" >&2; for f in "$work"/*.log; do [[ -f "$f" ]] && tail -n 30 "$f" >&2; done; exit 1; }

if ! git cat-file -e 'v0.2.0^{commit}' 2>/dev/null; then git fetch origin tag v0.2.0; fi
old_sha="$(git rev-parse 'v0.2.0^{commit}')"
[[ "$old_sha" == 4d806e01bdc37d0797dc3f7130b50f81307e1090 ]] || fail "v0.2.0 tag moved: $old_sha"
git worktree add --detach "$old_src" "$old_sha" >/dev/null
go -C "$old_src" build -o "$work/old-bcc" ./cmd/baft-bcc
go build -o "$work/new-bcc" ./cmd/baft-bcc

state="$work/state.db"
access="$work/access.json"
jobkey="$work/jobkey"
token_file="$work/admin-token"
printf 'isolated-drill-token\n' >"$token_file"
chmod 0600 "$token_file"
"$work/old-bcc" access init --access-file "$access" >/dev/null
"$work/old-bcc" jobkey show --job-key-file "$jobkey" >/dev/null
port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
url="http://127.0.0.1:$port"
start_bcc() {
  local binary="$1" logfile="$2" attempt
  "$binary" --listen "127.0.0.1:$port" --state-file "$state" \
    --admin-token-file "$token_file" --access-file "$access" --job-key-file "$jobkey" \
    >"$logfile" 2>&1 &
  server_pid=$!
  for attempt in $(seq 1 100); do
    if curl -fsS -H 'Authorization: Bearer isolated-drill-token' "$url/api/nodes" >/dev/null 2>&1; then return 0; fi
    kill -0 "$server_pid" 2>/dev/null || fail "$binary exited before serving"
    sleep 0.1
  done
  fail "$binary did not serve on loopback"
}
stop_bcc() {
  kill "$server_pid"
  wait "$server_pid" || true
  server_pid=""
}
schema() {
  python3 - "$state" <<'PY'
import sqlite3, sys
db = sqlite3.connect(f"file:{sys.argv[1]}?mode=ro", uri=True)
print(db.execute("SELECT MAX(version) FROM schema_migrations").fetchone()[0])
db.close()
PY
}
post_node() {
  local id="$1" code
  code="$(curl -sS -o "$work/post.json" -w '%{http_code}' \
    -H 'Authorization: Bearer isolated-drill-token' -H 'Content-Type: application/json' \
    -d "{\"ID\":\"$id\",\"Alias\":\"$id\",\"Address\":\"127.0.0.1:1\",\"Role\":\"foreign\"}" \
    "$url/api/nodes")"
  [[ "$code" == 201 ]] || fail "POST node $id returned $code: $(cat "$work/post.json")"
}
assert_nodes() {
  curl -fsS -H 'Authorization: Bearer isolated-drill-token' "$url/api/nodes" >"$work/nodes.json"
  python3 - "$work/nodes.json" "$@" <<'PY'
import json, sys
nodes = json.load(open(sys.argv[1]))
ids = {node.get("id", node.get("ID")) for node in nodes}
missing = set(sys.argv[2:]) - ids
if missing:
    raise SystemExit(f"missing nodes after recovery: {sorted(missing)}")
PY
}

start_bcc "$work/old-bcc" "$work/old.log"
[[ "$(schema)" == 5 ]] || fail "old binary did not create schema 5"
post_node before-upgrade
stop_bcc
start_bcc "$work/new-bcc" "$work/new.log"
[[ "$(schema)" == 12 ]] || fail "new binary did not migrate to schema 12"
post_node after-upgrade
stop_bcc

set +e
timeout 10s "$work/old-bcc" --listen "127.0.0.1:$port" --state-file "$state" \
  --admin-token-file "$token_file" --access-file "$access" --job-key-file "$jobkey" \
  >"$work/old-refusal.log" 2>&1
old_rc=$?
set -e
[[ "$old_rc" != 0 && "$old_rc" != 124 ]] || fail "old binary accepted migrated schema or hung"
grep -q 'newer than this build supports (5)' "$work/old-refusal.log" ||
  fail "old binary refused for an unexpected reason"

start_bcc "$work/new-bcc" "$work/recovered.log"
assert_nodes before-upgrade after-upgrade
[[ "$(schema)" == 12 ]] || fail "recovery changed schema unexpectedly"
stop_bcc
echo "[bcc-schema-drill] PASS: v0.2.0 rejects schema 12; current binary recovers both writes"
