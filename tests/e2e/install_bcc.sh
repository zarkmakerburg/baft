#!/usr/bin/env bash
# First-class BCC installer acceptance:
# - signed-release install starts a real systemd BCC and hands off web access
# - a healthy rerun rotates nothing and does not restart the service
# - source-mode install also starts a separate real BCC instance
set -Eeuo pipefail
cd "$(dirname "$0")/../.."
ROOT="$PWD"
REL="${1:?usage: install_bcc.sh <local-signed-release-dir>}"
case "$(uname -m)" in x86_64) ARCH=amd64 ;; aarch64) ARCH=arm64 ;; *) echo "unsupported arch" >&2; exit 1 ;; esac
[[ "$EUID" -eq 0 ]] || { echo "run as root"; exit 1; }

W="$(mktemp -d)"
services=(baft-bcc-e2e-rel baft-bcc-e2e-src)
cleanup() {
  local rc=$?
  trap - EXIT
  for s in "${services[@]}"; do
    systemctl stop "$s.service" >/dev/null 2>&1 || true
    systemctl disable "$s.service" >/dev/null 2>&1 || true
    rm -f "/etc/systemd/system/$s.service"
  done
  systemctl daemon-reload >/dev/null 2>&1 || true
  rm -rf /etc/baft-bcc-e2e-rel /etc/baft-bcc-e2e-src     /var/lib/baft-bcc-e2e-rel /var/lib/baft-bcc-e2e-src     /opt/baft-bcc-e2e-rel /opt/baft-bcc-e2e-src     /usr/local/bin/baft-bcc-e2e-rel /usr/local/bin/baft-bcc-e2e-src
  if [[ $rc -ne 0 ]]; then
    for s in "${services[@]}"; do
      echo "== journal $s" >&2
      journalctl -u "$s" -n 50 --no-pager >&2 2>/dev/null || true
    done
    for f in "$W"/*.out "$W"/*.err; do
      [[ -f "$f" ]] && { echo "== $f" >&2; tail -n 80 "$f" >&2; }
    done
  fi
  rm -rf "$W"
  exit $rc
}
trap cleanup EXIT
fail(){ echo "[bcc-install-e2e] FAIL: $*" >&2; exit 1; }
log(){ echo "[bcc-install-e2e] $*"; }

ROOTPUB="$(cat "$REL/root.pub")"
REV="file://$REL/revocations.json"
RELURL="file://$REL/dist"

rel_env=(
  BAFT_INSTALL_FROM=release
  BAFT_RELEASE_URL="$RELURL"
  BAFT_ROOT_PUB="$ROOTPUB"
  BAFT_REVOCATIONS_URL="$REV"
  BAFT_PREFIX=/opt/baft-bcc-e2e-rel
  BAFT_RELEASE_STATE=/opt/baft-bcc-e2e-rel/release-state.json
  BAFT_BCC_BIN=/usr/local/bin/baft-bcc-e2e-rel
  BAFT_BCC_CONFIG_DIR=/etc/baft-bcc-e2e-rel
  BAFT_BCC_STATE_DIR=/var/lib/baft-bcc-e2e-rel
  BAFT_BCC_STATE_FILE=/var/lib/baft-bcc-e2e-rel/bcc-state.json
  BAFT_BCC_ADMIN_TOKEN_FILE=/etc/baft-bcc-e2e-rel/admin-token
  BAFT_BCC_ACCESS_FILE=/etc/baft-bcc-e2e-rel/access.json
  BAFT_BCC_JOB_KEY_FILE=/etc/baft-bcc-e2e-rel/job-key
  BAFT_BCC_BACKUP_DIR=/var/lib/baft-bcc-e2e-rel/backups
  BAFT_BCC_BOOTSTRAP_SCRIPT=/opt/baft-bcc-e2e-rel/bcc-bootstrap/install.sh
  BAFT_BCC_SERVICE=baft-bcc-e2e-rel
  BAFT_BCC_LISTEN=127.0.0.1:18080
  BAFT_NONINTERACTIVE=1
)

run_rel(){ env "${rel_env[@]}" bash install.sh --bcc-only "$@"; }

log "signed-release BCC fresh install with first-class SSH enrollment"
run_rel --bcc-public-url http://127.0.0.1:18080 --bcc-allow-insecure-http >"$W/rel.out" 2>"$W/rel.err"
systemctl is-active --quiet baft-bcc-e2e-rel || fail "release BCC is not active"
systemctl is-enabled --quiet baft-bcc-e2e-rel || fail "release BCC is not enabled"
[[ "$(stat -c '%U %a' /opt/baft-bcc-e2e-rel/release-state.json 2>/dev/null)" == "root 644" ]] ||
  fail "verified BCC release did not record root-owned release provenance state"
python3 - /opt/baft-bcc-e2e-rel/release-state.json <<'PY' || fail "BCC release provenance state is invalid"
import json, re, sys
s=json.load(open(sys.argv[1]))
assert re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?", s["version"]), s
assert re.fullmatch(r"[0-9a-f]{40}", s["commit"]), s
assert isinstance(s["revocation_sequence"], int) and s["revocation_sequence"] >= 0, s
PY

for f in /etc/baft-bcc-e2e-rel/admin-token /etc/baft-bcc-e2e-rel/access.json /etc/baft-bcc-e2e-rel/job-key; do
  [[ "$(stat -c '%U %a' "$f")" == "root 600" ]] || { stat "$f"; fail "$f is not root 0600"; }
done
[[ -s /var/lib/baft-bcc-e2e-rel/bcc-state.json ]] || fail "BCC state was not created"
grep -q '^# baft-managed: true' /etc/systemd/system/baft-bcc-e2e-rel.service || fail "BAFT ownership marker missing"
grep -q '^# baft-component: bcc' /etc/systemd/system/baft-bcc-e2e-rel.service || fail "BCC component marker missing"
grep -Fq -- '--listen 127.0.0.1:18080' /etc/systemd/system/baft-bcc-e2e-rel.service || fail "listen argument missing from unit"
grep -Fq -- '--admin-token-file /etc/baft-bcc-e2e-rel/admin-token' /etc/systemd/system/baft-bcc-e2e-rel.service || fail "admin token path missing from unit"
BOOTSTRAP=/opt/baft-bcc-e2e-rel/bcc-bootstrap/install.sh
[[ "$(stat -c '%U %a' "$BOOTSTRAP")" == "root 700" ]] || fail "managed BCC bootstrap installer is not root 0700"
cmp -s "$BOOTSTRAP" "$REL/dist/baft-install-linux-$ARCH" || fail "managed bootstrap installer differs from the signed release installer"
grep -Fq -- "# baft-bootstrap-script: $BOOTSTRAP" /etc/systemd/system/baft-bcc-e2e-rel.service || fail "bootstrap ownership marker missing from unit"
BOOT_SHA="$(sha256sum "$BOOTSTRAP" | awk '{print $1}')"
grep -Fq -- "# baft-bootstrap-sha256: $BOOT_SHA" /etc/systemd/system/baft-bcc-e2e-rel.service || fail "bootstrap digest marker missing from unit"
grep -Fq -- "--install-script $BOOTSTRAP --public-url http://127.0.0.1:18080 --allow-insecure-http" /etc/systemd/system/baft-bcc-e2e-rel.service || fail "first-class bootstrap arguments missing from unit"
grep -Eq '^[[:space:]]*password: [A-Za-z0-9]{24}[[:space:]]*$' "$W/rel.out" || { cat "$W/rel.out"; fail "fresh access password was not handed off exactly once"; }
ADMIN="$(tr -d '\r\n' </etc/baft-bcc-e2e-rel/admin-token)"
[[ -n "$ADMIN" ]] || fail "admin token empty"
! grep -Fq "$ADMIN" "$W/rel.out" "$W/rel.err" || fail "admin token leaked to installer output"
bootstrap_code="$(curl -sS -o "$W/bootstrap-probe.out" -w '%{http_code}' -H "Authorization: Bearer $ADMIN" -H 'Content-Type: application/json' -d '{}' http://127.0.0.1:18080/api/bootstrap/hostkey)"
[[ "$bootstrap_code" != 501 ]] || fail "first-class installed BCC left bootstrap disabled"

SECRET="$(python3 - <<'PY'
import json
print(json.load(open("/etc/baft-bcc-e2e-rel/access.json"))["secret_path"])
PY
)"
[[ -n "$SECRET" ]] || fail "secret path empty"
code="$(curl -sS -o "$W/login.html" -w '%{http_code}' "http://127.0.0.1:18080/$SECRET/")"
[[ "$code" == 200 ]] || fail "secret dashboard path returned HTTP $code"
root_code="$(curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:18080/)"
[[ "$root_code" == 404 ]] || fail "BCC root returned HTTP $root_code, want 404"

log "healthy plan is a no-op, keeps credentials, and mutates nothing"
plan_hash_before="$(sha256sum /etc/baft-bcc-e2e-rel/admin-token /etc/baft-bcc-e2e-rel/access.json /etc/baft-bcc-e2e-rel/job-key /etc/systemd/system/baft-bcc-e2e-rel.service "$BOOTSTRAP" /opt/baft-bcc-e2e-rel/release-state.json)"
run_rel --plan --json >"$W/plan.json" 2>"$W/plan.err"
python3 - "$W/plan.json" <<'PY'
import json, sys
p=json.load(open(sys.argv[1]))
steps={s["item"]:s["action"] for s in p["steps"]}
assert p["state"] in ("ALREADY_INSTALLED","CURRENT"), p
assert p["changes"] == 0, p
for k in ("BCC admin token","BCC access","BCC job key","BCC bootstrap script","BCC SSH enrollment","BCC systemd unit","BCC service"):
    assert steps[k] == "keep", (k, steps.get(k), p)
PY
plan_hash_after="$(sha256sum /etc/baft-bcc-e2e-rel/admin-token /etc/baft-bcc-e2e-rel/access.json /etc/baft-bcc-e2e-rel/job-key /etc/systemd/system/baft-bcc-e2e-rel.service "$BOOTSTRAP" /opt/baft-bcc-e2e-rel/release-state.json)"
[[ "$plan_hash_after" == "$plan_hash_before" ]] || fail "BCC --plan mutated managed files"

cred_hash_before="$(sha256sum /etc/baft-bcc-e2e-rel/admin-token /etc/baft-bcc-e2e-rel/access.json /etc/baft-bcc-e2e-rel/job-key /etc/systemd/system/baft-bcc-e2e-rel.service "$BOOTSTRAP")"
pid_before="$(systemctl show -p MainPID --value baft-bcc-e2e-rel)"
ACCESS_PASSWORD="$(sed -n 's/^[[:space:]]*password:[[:space:]]*//p' "$W/rel.out" | head -n1)"

log "healthy rerun rotates nothing and does not restart"
run_rel >"$W/rerun.out" 2>"$W/rerun.err"
cred_hash_after="$(sha256sum /etc/baft-bcc-e2e-rel/admin-token /etc/baft-bcc-e2e-rel/access.json /etc/baft-bcc-e2e-rel/job-key /etc/systemd/system/baft-bcc-e2e-rel.service "$BOOTSTRAP")"
[[ "$cred_hash_after" == "$cred_hash_before" ]] || fail "credential/unit changed on healthy rerun"
[[ "$(systemctl show -p MainPID --value baft-bcc-e2e-rel)" == "$pid_before" ]] || fail "healthy rerun restarted BCC"
if [[ -n "$ACCESS_PASSWORD" ]] && grep -Fq "$ACCESS_PASSWORD" "$W/rerun.out"; then
  fail "rerun reprinted the old password"
fi
grep -q 'password: not stored' "$W/rerun.out" || { cat "$W/rerun.out"; fail "rerun did not clearly state password is not recoverable"; }

log "failed managed update rolls unit + bootstrap script back to one generation"
printf '
# synthetic-previous-bootstrap-generation
' >>"$BOOTSTRAP"
chmod 0700 "$BOOTSTRAP"
OLD_BOOT_SHA="$(sha256sum "$BOOTSTRAP" | awk '{print $1}')"
python3 - /etc/systemd/system/baft-bcc-e2e-rel.service "$OLD_BOOT_SHA" <<'PY'
import re,sys
path=sys.argv[1]
sha=sys.argv[2]
data=open(path).read()
data,n=re.subn(r'(?m)^# baft-bootstrap-sha256: [0-9a-f]+$', '# baft-bootstrap-sha256: '+sha, data, count=1)
assert n == 1
open(path,'w').write(data)
PY
systemctl daemon-reload
old_generation="$(sha256sum "$BOOTSTRAP" /etc/systemd/system/baft-bcc-e2e-rel.service)"
rc=0
env "${rel_env[@]}" BAFT_TEST_FAIL_AT=pre-commit bash install.sh --bcc-only --yes >"$W/rollback.out" 2>"$W/rollback.err" || rc=$?
[[ "$rc" != 0 ]] || fail "injected BCC update failure unexpectedly succeeded"
grep -q 'rolling back' "$W/rollback.err" || { cat "$W/rollback.err"; fail "failed BCC update did not roll back"; }
[[ "$(sha256sum "$BOOTSTRAP" /etc/systemd/system/baft-bcc-e2e-rel.service)" == "$old_generation" ]] ||
  fail "rollback left BCC unit and bootstrap script in different generations"
systemctl is-active --quiet baft-bcc-e2e-rel || fail "BCC was not restored active after rollback"

log "successful rerun commits the signed bootstrap generation"
run_rel --yes >"$W/post-rollback.out" 2>"$W/post-rollback.err"
cmp -s "$BOOTSTRAP" "$REL/dist/baft-install-linux-$ARCH" || fail "successful rerun did not restore the signed bootstrap generation"
NEW_BOOT_SHA="$(sha256sum "$BOOTSTRAP" | awk '{print $1}')"
grep -Fq -- "# baft-bootstrap-sha256: $NEW_BOOT_SHA" /etc/systemd/system/baft-bcc-e2e-rel.service ||
  fail "successful rerun left a unit/script generation mismatch"
systemctl is-active --quiet baft-bcc-e2e-rel || fail "BCC is not active after committed generation update"

log "source-mode BCC fresh install"
src_env=(
  BAFT_INSTALL_FROM=source
  BAFT_REPO_URL="file://$ROOT"
  BAFT_REF="$(git rev-parse HEAD)"
  BAFT_RUN_TESTS=0
  BAFT_PREFIX=/opt/baft-bcc-e2e-src
  BAFT_RELEASE_STATE=/opt/baft-bcc-e2e-src/release-state.json
  BAFT_BCC_BIN=/usr/local/bin/baft-bcc-e2e-src
  BAFT_BCC_CONFIG_DIR=/etc/baft-bcc-e2e-src
  BAFT_BCC_STATE_DIR=/var/lib/baft-bcc-e2e-src
  BAFT_BCC_STATE_FILE=/var/lib/baft-bcc-e2e-src/bcc-state.json
  BAFT_BCC_ADMIN_TOKEN_FILE=/etc/baft-bcc-e2e-src/admin-token
  BAFT_BCC_ACCESS_FILE=/etc/baft-bcc-e2e-src/access.json
  BAFT_BCC_JOB_KEY_FILE=/etc/baft-bcc-e2e-src/job-key
  BAFT_BCC_BACKUP_DIR=/var/lib/baft-bcc-e2e-src/backups
  BAFT_BCC_BOOTSTRAP_SCRIPT=/opt/baft-bcc-e2e-src/bcc-bootstrap/install.sh
  BAFT_BCC_SERVICE=baft-bcc-e2e-src
  BAFT_BCC_LISTEN=127.0.0.1:18081
  BAFT_NONINTERACTIVE=1
)
env "${src_env[@]}" bash install.sh --bcc-only >"$W/src.out" 2>"$W/src.err"
systemctl is-active --quiet baft-bcc-e2e-src || fail "source BCC is not active"
[[ ! -e /opt/baft-bcc-e2e-src/release-state.json ]] ||
  fail "source BCC must not fabricate signed-release provenance state"
for f in /etc/baft-bcc-e2e-src/admin-token /etc/baft-bcc-e2e-src/access.json /etc/baft-bcc-e2e-src/job-key; do
  [[ "$(stat -c '%U %a' "$f")" == "root 600" ]] || fail "$f is not root 0600"
done
SRC_BOOTSTRAP=/opt/baft-bcc-e2e-src/bcc-bootstrap/install.sh
[[ "$(stat -c '%U %a' "$SRC_BOOTSTRAP")" == "root 700" ]] || fail "source bootstrap installer is not root 0700"
cmp -s "$SRC_BOOTSTRAP" "$ROOT/install.sh" || fail "source bootstrap installer differs from its checked-out source generation"
if grep -q -- ' --install-script ' /etc/systemd/system/baft-bcc-e2e-src.service || grep -q -- ' --public-url ' /etc/systemd/system/baft-bcc-e2e-src.service; then
  fail "BCC without public URL unexpectedly enabled SSH bootstrap"
fi
SRC_ADMIN="$(tr -d '\r\n' </etc/baft-bcc-e2e-src/admin-token)"
src_bootstrap_code="$(curl -sS -o "$W/src-bootstrap.out" -w '%{http_code}' -H "Authorization: Bearer $SRC_ADMIN" -H 'Content-Type: application/json' -d '{}' http://127.0.0.1:18081/api/bootstrap/hostkey)"
[[ "$src_bootstrap_code" == 501 ]] || fail "BCC without public URL returned bootstrap HTTP $src_bootstrap_code, want 501"
src_root="$(curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:18081/)"
[[ "$src_root" == 404 ]] || fail "source BCC root returned HTTP $src_root"

log "PASS"
