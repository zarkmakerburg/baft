#!/usr/bin/env bash
# Advanced adversary harness: combines loopback destruction, intrusion,
# recovery/security regressions, race stress, fuzz smoke and installer
# fault-injection into one reproducible report. It never targets third-party
# hosts; all network activity is local to the test machine.
set -Eeuo pipefail
cd "$(dirname "$0")/../.."

OUT="${1:?usage: tests/adversary/advanced.sh OUT_DIR}"
mkdir -p "$OUT"
RESULTS="$OUT/results.jsonl"
REPORT="$OUT/report.md"
: >"$RESULTS"

ADV_PROFILE="${ADV_PROFILE:-bounded}"
ADV_FUZZTIME="${ADV_FUZZTIME:-10s}"
export GOCACHE="${GOCACHE:-$PWD/.gocache}"
export GOMODCACHE="${GOMODCACHE:-$PWD/.gomodcache}"

log() { printf '[adv-v2] %s\n' "$*" >&2; }
json_escape() {
  python3 -c 'import json,sys; print(json.dumps(sys.stdin.read())[1:-1])'
}

run_step() {
  local name="$1"; shift
  local dir="$OUT/$name"
  mkdir -p "$dir"
  log "== $name"
  local start end rc
  start="$(date +%s)"
  set +e
  ( "$@" ) >"$dir/stdout.log" 2>"$dir/stderr.log"
  rc=$?
  set -e
  end="$(date +%s)"
  local status detail
  if [[ "$rc" -eq 0 ]]; then status=pass; else status=fail; fi
  detail="$(tail -n 20 "$dir/stderr.log" "$dir/stdout.log" 2>/dev/null | json_escape)"
  printf '{"step":"%s","status":"%s","rc":%d,"seconds":%d,"detail":"%s"}\n' \
    "$name" "$status" "$rc" "$((end - start))" "$detail" | tee -a "$RESULTS" >&2
  return "$rc"
}

run_sudo_step() {
  local name="$1"; shift
  if [[ "${EUID:-$(id -u)}" -eq 0 ]]; then
    run_step "$name" "$@"
  elif command -v sudo >/dev/null 2>&1; then
    run_step "$name" sudo env "PATH=$PATH" "HOME=$HOME" "GOCACHE=$GOCACHE" "GOMODCACHE=$GOMODCACHE" "$@"
  else
    log "skip $name: sudo is unavailable"
    printf '{"step":"%s","status":"skip","rc":0,"seconds":0,"detail":"sudo unavailable"}\n' "$name" | tee -a "$RESULTS" >&2
  fi
}

VERDICT=0
run_step "destruction-soak" env ADV_PROFILE="$ADV_PROFILE" tests/adversary/run.sh "$OUT/destruction-soak/out" || VERDICT=1
run_step "intrusion-boundaries" tests/adversary/intrude.sh "$OUT/intrusion-boundaries/out" || VERDICT=1
run_step "ecrl-adversarial-model" go test ./internal/recovery \
  -run '^(TestECRLF|TestECRLKReleaseNotRequiredForCurrentInMemoryResume|TestECRLFutureDurableSlowTargetReplayBoundNoDeadlock|TestECRLMutationCatalogCoversEveryF|TestStage1DifferentialGenerated|TestEngine)' \
  -count=1 -v || VERDICT=1
run_step "race-recovery-session-topology" env GOMAXPROCS=2 go test -race \
  ./internal/session ./internal/recovery ./internal/tunnelnode ./tests/integration -count=1 || VERDICT=1
run_step "recordshape-fuzz-smoke" go test ./internal/recordshape -run '^$' \
  -fuzz FuzzMorphingReadFrame -fuzztime="$ADV_FUZZTIME" || VERDICT=1
if [[ "${ADV_SKIP_INSTALLER_FAULTS:-0}" != "1" ]]; then
  run_sudo_step "installer-fault-injection" tests/e2e/installer_faults.sh || VERDICT=1
fi

python3 - "$RESULTS" "$REPORT" <<'PY'
import json
import sys
from pathlib import Path

rows = [json.loads(line) for line in Path(sys.argv[1]).read_text().splitlines() if line.strip()]
verdict = "PASS" if all(r["status"] in ("pass", "skip") for r in rows) else "FAIL"
with open(sys.argv[2], "w", encoding="utf-8") as w:
    w.write(f"# BAFT Advanced Adversary Report\n\n")
    w.write(f"**Verdict: {verdict}**\n\n")
    w.write("| step | status | rc | seconds |\n")
    w.write("|---|---:|---:|---:|\n")
    for r in rows:
        mark = {"pass": "PASS", "fail": "FAIL", "skip": "SKIP"}.get(r["status"], r["status"])
        w.write(f"| {r['step']} | {mark} | {r['rc']} | {r['seconds']} |\n")
    failures = [r for r in rows if r["status"] == "fail"]
    if failures:
        w.write("\n## Failures\n\n")
        for r in failures:
            detail = r.get("detail", "").strip()
            if len(detail) > 3000:
                detail = detail[-3000:]
            w.write(f"### {r['step']}\n\n```text\n{detail}\n```\n\n")
PY

cat "$REPORT" >&2
exit "$VERDICT"
