#!/usr/bin/env bash
# Adversarial resilience soak: start a real BAFT EX/IR pair on loopback, run
# each adversary scenario (attack.py) against it, and after each one assert the
# nodes are still up, memory and fds are bounded, and a real transfer still
# passes. Measurement + resilience assertions only; loopback only; never
# targets any third-party host.
#
#   tests/adversary/run.sh OUT_DIR
# Profile (env): ADV_PROFILE=bounded (default, sized for a 2-CPU CI runner) or
# ADV_PROFILE=heavy (for a dedicated self-hosted box; much higher volume).
# Individual knobs (SECONDS_PER, WORKERS, CONNS, SLOWLORIS_CONNS, RSS_LIMIT_MB,
# FD_LIMIT) override the profile.
set -Eeuo pipefail
cd "$(dirname "$0")/../.."
ROOT="$PWD"
OUT="${1:?usage: run.sh OUT_DIR}"; mkdir -p "$OUT"
WORK="$(mktemp -d)"; PIDS=()
log() { printf '[adv] %s\n' "$*" >&2; }
stop_all() { local p; for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null || true; done; for p in "${PIDS[@]}"; do wait "$p" 2>/dev/null || true; done; PIDS=(); }
cleanup() { stop_all; [[ -n "${TARGET_PID:-}" ]] && kill "$TARGET_PID" 2>/dev/null || true; if [[ -z "${ADV_KEEP:-}" ]]; then rm -rf "$WORK"; else log "workdir $WORK"; fi; return 0; }
trap cleanup EXIT

case "${ADV_PROFILE:-bounded}" in
  heavy)   : "${SECONDS_PER:=20}" "${WORKERS:=64}" "${CONNS:=2000}" "${SLOWLORIS_CONNS:=1000}" "${RSS_LIMIT_MB:=1024}" "${FD_LIMIT:=4096}" ;;
  *)       : "${SECONDS_PER:=6}"  "${WORKERS:=16}" "${CONNS:=300}"  "${SLOWLORIS_CONNS:=200}"  "${RSS_LIMIT_MB:=512}"  "${FD_LIMIT:=1024}" ;;
esac

free_port() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])'; }
wait_port() { for _ in $(seq 1 200); do python3 -c "import socket;socket.create_connection(('127.0.0.1',$1),0.2)" 2>/dev/null && return 0; sleep 0.1; done; log "port $1 never opened"; return 1; }
rss_mb() { awk '/VmRSS/{print int($2/1024)}' "/proc/$1/status" 2>/dev/null || echo -1; }
fd_count() { ls "/proc/$1/fd" 2>/dev/null | wc -l | tr -d ' '; }
alive() { kill -0 "$1" 2>/dev/null; }

BIN="$WORK/bin"; go build -o "$BIN/" ./cmd/baft ./cmd/baft-pair
PAIR="$BIN/baft-pair"; BAFT="$BIN/baft"
NOISE="${ADV_NOISE:-1}"; SHAPE=()
[[ "$NOISE" == 1 ]] && TLS_FLAG="--tls" || TLS_FLAG=""

TARGET=$(free_port); CARRIER=$(free_port); ENTRY=$(free_port)
EX="$WORK/ex"; IR="$WORK/ir"; mkdir -p "$EX" "$IR/state"
python3 tests/e2e/echo.py serve "$TARGET" >"$WORK/target.log" 2>&1 & TARGET_PID=$!
wait_port "$TARGET"

log "pair EX/IR (noise=$NOISE)"
"$PAIR" keygen --file "$EX/k.json" >/dev/null; "$PAIR" pki --dir "$EX/pki" --host 127.0.0.1 >/dev/null
code=$("$PAIR" ex-code --key "$EX/k.json" --address "127.0.0.1:$CARRIER" --server-name 127.0.0.1 \
  --identity urn:baft:node:ex-adv --ca-file "$EX/pki/ca.pem" --psk-out "$EX/p.psk" --pending-out "$EX/p.json" --ttl 10m)
"$PAIR" keygen --file "$IR/k.json" >/dev/null
reply=$("$PAIR" ir-apply --code "$code" --key "$IR/k.json" --state-dir "$IR/state" --config-out "$IR/baft.yaml" \
  --route-listen "127.0.0.1:$ENTRY" --metrics-listen "127.0.0.1:$(free_port)")
"$PAIR" ex-accept --reply "$reply" --pending "$EX/p.json" --psk-file "$EX/p.psk" --key "$EX/k.json" \
  --listen "127.0.0.1:$CARRIER" --ca-file "$EX/pki/ca.pem" --cert-file "$EX/pki/server.pem" \
  --cert-key-file "$EX/pki/server.key" --target "127.0.0.1:$TARGET" --metrics-listen "127.0.0.1:$(free_port)" \
  --unix-socket "$EX/admin.sock" --config-out "$EX/baft.yaml" >/dev/null

"$BAFT" run --file "$EX/baft.yaml" >"$WORK/ex.log" 2>&1 & EX_PID=$!; PIDS+=($EX_PID); wait_port "$CARRIER"
"$BAFT" run --file "$IR/baft.yaml" >"$WORK/ir.log" 2>&1 & IR_PID=$!; PIDS+=($IR_PID); wait_port "$ENTRY"

transfer_ok() { timeout 60 python3 tests/e2e/echo.py send "$ENTRY" 4 1 >/dev/null 2>&1; }
transfer_ok || { log "baseline transfer failed"; tail -n 20 "$WORK"/*.log >&2; exit 1; }
base_ex_rss=$(rss_mb "$EX_PID"); base_ir_rss=$(rss_mb "$IR_PID")
{ echo "profile=${ADV_PROFILE:-bounded} seconds_per=$SECONDS_PER workers=$WORKERS conns=$CONNS slowloris=$SLOWLORIS_CONNS noise=$NOISE"
  echo "sha=$(git rev-parse HEAD)"; go version; uname -a
  echo "baseline rss: ex=${base_ex_rss}MB ir=${base_ir_rss}MB limit=${RSS_LIMIT_MB}MB fd_limit=$FD_LIMIT"; } >"$OUT/provenance.txt"

RESULTS="$OUT/results.jsonl"; : >"$RESULTS"; VERDICT=0
record() { # name json
  local ex_rss ir_rss ex_fd ir_fd heal pass=1 reason=""
  if ! alive "$EX_PID"; then pass=0; reason="EX died"; fi
  if ! alive "$IR_PID"; then pass=0; reason="IR died"; fi
  if [[ $pass == 1 ]]; then
    ex_rss=$(rss_mb "$EX_PID"); ir_rss=$(rss_mb "$IR_PID"); ex_fd=$(fd_count "$EX_PID"); ir_fd=$(fd_count "$IR_PID")
    (( ex_rss <= RSS_LIMIT_MB )) || { pass=0; reason="EX rss ${ex_rss}MB > ${RSS_LIMIT_MB}MB"; }
    (( ir_rss <= RSS_LIMIT_MB )) || { pass=0; reason="IR rss ${ir_rss}MB > ${RSS_LIMIT_MB}MB"; }
    (( ex_fd <= FD_LIMIT )) || { pass=0; reason="EX fd $ex_fd > $FD_LIMIT"; }
    (( ir_fd <= FD_LIMIT )) || { pass=0; reason="IR fd $ir_fd > $FD_LIMIT"; }
  fi
  heal=skipped
  if [[ $pass == 1 ]]; then
    if transfer_ok; then heal=ok; else
      sleep 3; if transfer_ok; then heal=recovered; else pass=0; reason="transfer broken after attack"; heal=failed; fi
    fi
  fi
  [[ $pass == 1 ]] || VERDICT=1
  printf '{"scenario":"%s","pass":%s,"reason":"%s","heal":"%s","ex_rss_mb":%s,"ir_rss_mb":%s,"ex_fd":%s,"ir_fd":%s,"attack":%s}\n' \
    "$1" "$([[ $pass == 1 ]] && echo true || echo false)" "$reason" "$heal" "${ex_rss:- -1}" "${ir_rss:- -1}" "${ex_fd:-0}" "${ir_fd:-0}" "$2" | tee -a "$RESULTS" >&2
}

for sc in connflood garbage slowloris probe manyflows; do
  log "== $sc"
  case $sc in
    connflood) att=$(python3 tests/adversary/attack.py connflood --carrier "$CARRIER" --seconds "$SECONDS_PER" --workers "$WORKERS") ;;
    garbage)   att=$(python3 tests/adversary/attack.py garbage   --carrier "$CARRIER" --seconds "$SECONDS_PER" --workers "$WORKERS") ;;
    slowloris) att=$(python3 tests/adversary/attack.py slowloris --carrier "$CARRIER" --seconds "$SECONDS_PER" --conns "$SLOWLORIS_CONNS") ;;
    probe)     att=$(python3 tests/adversary/attack.py probe     --carrier "$CARRIER" $TLS_FLAG) ;;
    manyflows) att=$(python3 tests/adversary/attack.py manyflows --entry "$ENTRY" --conns "$CONNS" --seconds "$SECONDS_PER") ;;
  esac
  record "$sc" "${att:-{\}}"
done

# Probe evidence. The response to each unauthenticated probe is recorded for
# periodic human review (deep indistinguishability across layers is asserted by
# the Go unit tests in internal/carrier/h2). The hard check here is the one a
# soak can prove on its own: a replayed identical probe must get an identical
# response, i.e. the carrier exposes no stateful distinguisher to a censor that
# sends the same bytes twice.
python3 - "$RESULTS" >>"$OUT/provenance.txt" <<'PY' || VERDICT=1
import json, sys
rows = [json.loads(l) for l in open(sys.argv[1])]
pr = next((r["attack"]["result"] for r in rows if r["scenario"] == "probe"), None)
if not pr:
    print("probe: no result recorded"); sys.exit(1)
shots = {k: (v.get("bytes"), v.get("closed")) for k, v in pr.items()
         if isinstance(v, dict) and "bytes" in v}
print(f"probe fingerprints (evidence): {shots}")
rep = pr.get("replayed_random", {})
a, b = rep.get("first", {}), rep.get("second", {})
fa, fb = (a.get("bytes"), a.get("closed")), (b.get("bytes"), b.get("closed"))
if fa != fb:
    print(f"PROBE NONDETERMINISTIC: identical input gave {fa} then {fb}"); sys.exit(1)
print(f"probe deterministic: identical input gave the same response {fa}")
PY

python3 - "$RESULTS" >"$OUT/summary.md" <<'PY'
import json, sys
rows = [json.loads(l) for l in open(sys.argv[1])]
print("| scenario | pass | heal | EX rss MB | IR rss MB | EX fd | IR fd | note |")
print("|---|---|---|---|---|---|---|---|")
for r in rows:
    print(f"| {r['scenario']} | {'✅' if r['pass'] else '❌'} | {r['heal']} | {r['ex_rss_mb']} | {r['ir_rss_mb']} | {r['ex_fd']} | {r['ir_fd']} | {r['reason']} |")
PY
cat "$OUT/summary.md" >&2
log "verdict=$VERDICT"
exit $VERDICT
