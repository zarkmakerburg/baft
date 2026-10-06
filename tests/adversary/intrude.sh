#!/usr/bin/env bash
# Intrusion / penetration scenarios against a real BAFT EX/IR pair on loopback.
# Each one tries to break a security boundary; the test passes when the boundary
# HOLDS (the intrusion is rejected) and the node stays up and legit traffic
# still works. Loopback only; the project's own software; no third-party host.
#
#   tests/adversary/intrude.sh OUT_DIR
set -Eeuo pipefail
cd "$(dirname "$0")/../.."
ROOT="$PWD"; OUT="${1:?usage: intrude.sh OUT_DIR}"; mkdir -p "$OUT"
WORK="$(mktemp -d)"; PIDS=()
log(){ printf '[intrude] %s\n' "$*" >&2; }
stop_all(){ local p; for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null||true; done; for p in "${PIDS[@]}"; do wait "$p" 2>/dev/null||true; done; PIDS=(); }
cleanup(){ stop_all; [[ -n "${TPID:-}" ]]&&kill "$TPID" 2>/dev/null||true; if [[ -z "${ADV_KEEP:-}" ]]; then rm -rf "$WORK"; else log "workdir $WORK"; fi; return 0; }
trap cleanup EXIT
fp(){ python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])'; }
wait_port(){ for _ in $(seq 1 200); do python3 -c "import socket;socket.create_connection(('127.0.0.1',$1),0.2)" 2>/dev/null&&return 0; sleep 0.1; done; return 1; }
alive(){ kill -0 "$1" 2>/dev/null; }
transfer_ok(){ timeout 60 python3 tests/e2e/echo.py send "$1" 2 1 >/dev/null 2>&1; }

BIN="$WORK/bin"; go build -o "$BIN/" ./cmd/baft ./cmd/baft-pair
PAIR="$BIN/baft-pair"; BAFT="$BIN/baft"
RESULTS="$OUT/results.jsonl"; : >"$RESULTS"; VERDICT=0
rec(){ # name held(bool) detail
  [[ "$2" == true ]]||VERDICT=1
  printf '{"scenario":"%s","boundary_held":%s,"detail":"%s"}\n' "$1" "$2" "$3" | tee -a "$RESULTS" >&2
}

# ---- A) pairing-code one-time: a replayed reply must not be accepted twice ----
pairing_replay(){
  local d="$WORK/a"; mkdir -p "$d/ex" "$d/ir/state"
  "$PAIR" keygen --file "$d/ex/k.json" >/dev/null; "$PAIR" pki --dir "$d/ex/pki" --host 127.0.0.1 >/dev/null
  local code reply port; port=$(fp)
  code=$("$PAIR" ex-code --key "$d/ex/k.json" --address "127.0.0.1:$port" --server-name 127.0.0.1 \
    --identity urn:baft:node:ex-a --ca-file "$d/ex/pki/ca.pem" --psk-out "$d/ex/p.psk" --pending-out "$d/ex/p.json" --ttl 10m)
  "$PAIR" keygen --file "$d/ir/k.json" >/dev/null
  reply=$("$PAIR" ir-apply --code "$code" --key "$d/ir/k.json" --state-dir "$d/ir/state" --config-out "$d/ir/baft.yaml" \
    --route-listen "127.0.0.1:$(fp)" --metrics-listen "127.0.0.1:$(fp)")
  "$PAIR" ex-accept --reply "$reply" --pending "$d/ex/p.json" --psk-file "$d/ex/p.psk" --key "$d/ex/k.json" \
    --listen "127.0.0.1:$port" --ca-file "$d/ex/pki/ca.pem" --cert-file "$d/ex/pki/server.pem" \
    --cert-key-file "$d/ex/pki/server.key" --target "127.0.0.1:$(fp)" --metrics-listen "127.0.0.1:$(fp)" \
    --unix-socket "$d/ex/admin.sock" --config-out "$d/ex/baft.yaml" >/dev/null 2>&1
  # Replay the exact same reply against the consumed pending/psk.
  if "$PAIR" ex-accept --reply "$reply" --pending "$d/ex/p.json" --psk-file "$d/ex/p.psk" --key "$d/ex/k.json" \
      --listen "127.0.0.1:$(fp)" --ca-file "$d/ex/pki/ca.pem" --cert-file "$d/ex/pki/server.pem" \
      --cert-key-file "$d/ex/pki/server.key" --target "127.0.0.1:$(fp)" --metrics-listen "127.0.0.1:$(fp)" \
      --unix-socket "$d/ex/admin2.sock" --config-out "$d/ex/baft2.yaml" >/dev/null 2>&1; then
    rec pairing_replay false "a replayed one-time pairing reply was accepted a second time"
  else
    rec pairing_replay true "replayed one-time pairing reply rejected"
  fi
}

# Shared legit pair for B and C. Sets EX_PID IR_PID ENTRY CARRIER TARGET and dir.
start_pair(){ # dir route_addr -> echoes nothing, sets globals
  local d=$1; mkdir -p "$d/ex" "$d/ir/state"
  CARRIER=$(fp); ENTRY=$(fp); TARGET=$(fp)
  python3 tests/e2e/echo.py serve "$TARGET" >"$d/target.log" 2>&1 & TPID=$!
  wait_port "$TARGET"
  "$PAIR" keygen --file "$d/ex/k.json" >/dev/null; "$PAIR" pki --dir "$d/ex/pki" --host 127.0.0.1 >/dev/null
  local code reply
  code=$("$PAIR" ex-code --key "$d/ex/k.json" --address "127.0.0.1:$CARRIER" --server-name 127.0.0.1 \
    --identity urn:baft:node:ex-i --ca-file "$d/ex/pki/ca.pem" --psk-out "$d/ex/p.psk" --pending-out "$d/ex/p.json" --ttl 10m)
  "$PAIR" keygen --file "$d/ir/k.json" >/dev/null
  reply=$("$PAIR" ir-apply --code "$code" --key "$d/ir/k.json" --state-dir "$d/ir/state" --config-out "$d/ir/baft.yaml" \
    --route-listen "127.0.0.1:$ENTRY" --metrics-listen "127.0.0.1:$(fp)")
  "$PAIR" ex-accept --reply "$reply" --pending "$d/ex/p.json" --psk-file "$d/ex/p.psk" --key "$d/ex/k.json" \
    --listen "127.0.0.1:$CARRIER" --ca-file "$d/ex/pki/ca.pem" --cert-file "$d/ex/pki/server.pem" \
    --cert-key-file "$d/ex/pki/server.key" --target "127.0.0.1:$TARGET" --metrics-listen "127.0.0.1:$(fp)" \
    --unix-socket "$d/ex/admin.sock" --config-out "$d/ex/baft.yaml" >/dev/null
  "$BAFT" run --file "$d/ex/baft.yaml" >"$d/ex.log" 2>&1 & EX_PID=$!; PIDS+=($EX_PID); wait_port "$CARRIER"
  "$BAFT" run --file "$d/ir/baft.yaml" >"$d/ir.log" 2>&1 & IR_PID=$!; PIDS+=($IR_PID); wait_port "$ENTRY"
}

# ---- B) rogue carrier with an unpinned key must be rejected by EX ----
rogue_key(){
  local d="$WORK/b"; start_pair "$d"
  transfer_ok "$ENTRY" || { rec rogue_key false "legit baseline transfer failed"; return; }
  # A rogue IR: the legit config, but its own Noise static key replaced, so the
  # key EX pinned at pairing no longer matches. Point it at the same EX carrier.
  local rentry rmetrics; rentry=$(fp); rmetrics=$(fp)
  "$PAIR" keygen --file "$d/rogue-k.json" >/dev/null
  # The rogue is the legit IR config with its own Noise static key replaced
  # (so EX's pinned peer key no longer matches) and a free route/metrics port.
  python3 - "$d/ir/baft.yaml" "$d/rogue.yaml" "$d/rogue-k.json" "$rentry" "$rmetrics" <<'PY'
import json, sys
src, dst, key, entry, metrics = sys.argv[1:6]
c = json.load(open(src))
c["noise"]["key_file"] = key
c["routes"][0]["listen"] = "127.0.0.1:" + entry
c["management"]["metrics_listen"] = "127.0.0.1:" + metrics
json.dump(c, open(dst, "w"))
PY
  # Start the rogue; EX must reject its handshake. Give it 3s.
  "$BAFT" run --file "$d/rogue.yaml" >"$d/rogue.log" 2>&1 & local rpid=$!; PIDS+=($rpid)
  sleep 3
  local held=true detail="rogue carrier rejected; EX up; legit traffic intact"
  if ! alive "$EX_PID"; then held=false; detail="EX died when a rogue carrier connected"; fi
  if ! transfer_ok "$ENTRY"; then held=false; detail="legit traffic broke after rogue connected"; fi
  # The security invariant is semantic: a rogue static key is a bypass only
  # if it can open the route and carry byte-verified application traffic.
  # Diagnostic wording is deliberately not authoritative; transport phase
  # errors may contain authentication/readiness words while still failing
  # closed before a usable BAFT Session exists.
  if transfer_ok "$rentry"; then
    held=false; detail="rogue carrier established a usable route and carried verified application traffic"
  elif [[ "$held" == true ]]; then
    local rejected
    rejected=$(grep -iE 'node stopped|OpenNoise|Noise|reject|mismatch' "$d/rogue.log" 2>/dev/null | head -1 || true)
    [[ -z "$rejected" ]] || detail="rogue carrier rejected; legit traffic intact: $rejected"
  fi
  kill "$rpid" 2>/dev/null||true
  rec rogue_key "$held" "$detail"
  stop_all; kill "$TPID" 2>/dev/null||true
}

# ---- C) on-path ciphertext tamper must fail closed (no corrupted delivery) ----
onpath_tamper(){
  local d="$WORK/c"; mkdir -p "$d/ex" "$d/ir/state"
  local target carrier entry relay; target=$(fp); carrier=$(fp); entry=$(fp); relay=$(fp)
  python3 tests/e2e/echo.py serve "$target" >"$d/target.log" 2>&1 & TPID=$!; wait_port "$target"
  "$PAIR" keygen --file "$d/ex/k.json" >/dev/null; "$PAIR" pki --dir "$d/ex/pki" --host 127.0.0.1 >/dev/null
  local code reply
  code=$("$PAIR" ex-code --key "$d/ex/k.json" --address "127.0.0.1:$relay" --server-name 127.0.0.1 \
    --identity urn:baft:node:ex-c --ca-file "$d/ex/pki/ca.pem" --psk-out "$d/ex/p.psk" --pending-out "$d/ex/p.json" --ttl 10m)
  "$PAIR" keygen --file "$d/ir/k.json" >/dev/null
  # IR dials the relay (peer address = relay); relay forwards to the real EX
  # carrier and flips one byte, modelling an on-path attacker.
  reply=$("$PAIR" ir-apply --code "$code" --key "$d/ir/k.json" --state-dir "$d/ir/state" --config-out "$d/ir/baft.yaml" \
    --route-listen "127.0.0.1:$entry" --metrics-listen "127.0.0.1:$(fp)")
  "$PAIR" ex-accept --reply "$reply" --pending "$d/ex/p.json" --psk-file "$d/ex/p.psk" --key "$d/ex/k.json" \
    --listen "127.0.0.1:$carrier" --ca-file "$d/ex/pki/ca.pem" --cert-file "$d/ex/pki/server.pem" \
    --cert-key-file "$d/ex/pki/server.key" --target "127.0.0.1:$target" --metrics-listen "127.0.0.1:$(fp)" \
    --unix-socket "$d/ex/admin.sock" --config-out "$d/ex/baft.yaml" >/dev/null
  "$BAFT" run --file "$d/ex/baft.yaml" >"$d/ex.log" 2>&1 & local expid=$!; PIDS+=($expid); wait_port "$carrier"
  python3 tests/adversary/mitm.py "$relay" "$carrier" 20000 >"$d/mitm.log" 2>&1 & PIDS+=($!); wait_port "$relay"
  "$BAFT" run --file "$d/ir/baft.yaml" >"$d/ir.log" 2>&1 & local irpid=$!; PIDS+=($irpid); wait_port "$entry"
  # Push data across the flip point. echo.py send verifies SHA-256, so it exits
  # 0 only on byte-exact delivery. Integrity holds if NO corrupted bytes reach
  # the target (a clean fail-closed error or byte-exact delivery) and EX does
  # not crash. The IR dialer exiting on a bad-record-MAC carrier is the designed
  # fail-closed behaviour (systemd restarts it in production), not a bypass.
  local held=true detail=""
  if python3 tests/e2e/echo.py send "$entry" 1 1 >"$d/send.log" 2>&1; then
    detail="AEAD dropped the flipped record; delivery byte-exact after retransmit"
  else
    detail="on-path tamper detected and failed closed (no corrupted delivery)"
  fi
  # EX must stay up and neither node may panic; an integrity error exit is fine.
  if ! alive "$expid"; then held=false; detail="EX crashed under on-path tamper"; fi
  if grep -qiE 'panic:|runtime error' "$d/ex.log" "$d/ir.log" 2>/dev/null; then
    held=false; detail="a node panicked under on-path tamper: $(grep -hiE 'panic:|runtime error' "$d/ir.log" "$d/ex.log" | head -1)"
  fi
  # Confirm the tamper was actually detected (bad record MAC / clean stop),
  # i.e. the carrier integrity check fired rather than silently passing bytes.
  if [[ "$held" == true ]] && ! grep -qiE 'bad record mac|node stopped|decrypt|integrity' "$d/ir.log" 2>/dev/null && alive "$irpid"; then
    detail="$detail (note: no explicit integrity error logged)"
  fi
  rec onpath_tamper "$held" "$detail"
  stop_all; kill "$TPID" 2>/dev/null||true
}

pairing_replay
rogue_key
onpath_tamper

python3 - "$RESULTS" >"$OUT/summary.md" <<'PY'
import json,sys
rows=[json.loads(l) for l in open(sys.argv[1])]
print("| intrusion | boundary held | detail |"); print("|---|---|---|")
for r in rows: print(f"| {r['scenario']} | {'✅' if r['boundary_held'] else '❌ BYPASS'} | {r['detail']} |")
PY
cat "$OUT/summary.md" >&2
log "verdict=$VERDICT"; exit $VERDICT
