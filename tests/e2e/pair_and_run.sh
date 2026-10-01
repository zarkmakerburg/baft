#!/usr/bin/env bash
# End-to-end check with the real binaries: pair an EX and an IR with
# baft-pair exactly as install.sh does, start both with `baft run`, and push
# data through IR -> EX -> a local echo target, comparing SHA-256 both ways.
#
#   tests/e2e/pair_and_run.sh            # builds into a temp dir
#   BIN_DIR=/path tests/e2e/pair_and_run.sh
# Env: E2E_MIB (per connection, default 8), E2E_CONNS (default 4),
#      E2E_RECORD_SHAPING=1 to pair with --record-shaping (Stealth Pro).
set -Eeuo pipefail
cd "$(dirname "$0")/../.."
ROOT="$PWD"
WORK="$(mktemp -d)"
PIDS=()
cleanup() {
  local rc=$?
  for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null || true; done
  wait 2>/dev/null || true
  if [[ $rc -ne 0 ]]; then
    for f in "$WORK"/*.log; do [[ -f "$f" ]] && { echo "== $f"; tail -n 50 "$f"; }; done
  fi
  rm -rf "$WORK"
  exit $rc
}
trap cleanup EXIT
log() { printf '[e2e] %s\n' "$*"; }

if [[ -z "${BIN_DIR:-}" ]]; then
  BIN_DIR="$WORK/bin"
  go build -o "$BIN_DIR/" ./cmd/baft ./cmd/baft-pair
fi
BAFT="$BIN_DIR/baft"; PAIR="$BIN_DIR/baft-pair"
umask 077
SHAPING=""
[[ "${E2E_RECORD_SHAPING:-0}" == "1" ]] && SHAPING="--record-shaping"

free_port() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])'; }
EX_PORT=$(free_port); IR_PORT=$(free_port); TARGET_PORT=$(free_port); EX_M=$(free_port); IR_M=$(free_port)
EX="$WORK/ex"; IR="$WORK/ir"; mkdir -p "$EX" "$IR/state"

log "EX: keys, outer PKI, pairing code"
"$PAIR" keygen --file "$EX/noise-key.json" >/dev/null
"$PAIR" pki --dir "$EX/pki" --host 127.0.0.1
CODE=$("$PAIR" ex-code --key "$EX/noise-key.json" --address "127.0.0.1:$EX_PORT" \
  --server-name 127.0.0.1 --identity urn:baft:node:ex-e2e --ca-file "$EX/pki/ca.pem" \
  --psk-out "$EX/pairing.psk" --pending-out "$EX/pairing.ex.json" --ttl 5m $SHAPING)

log "IR: apply code, write config, print reply"
"$PAIR" keygen --file "$IR/noise-key.json" >/dev/null
REPLY=$("$PAIR" ir-apply --code "$CODE" --key "$IR/noise-key.json" --state-dir "$IR/state" \
  --config-out "$IR/baft.yaml" --route-listen "127.0.0.1:$IR_PORT" --metrics-listen "127.0.0.1:$IR_M")

log "EX: a reply from someone without the pairing code must be rejected"
"$PAIR" keygen --file "$WORK/rogue-key.json" >/dev/null
ROGUE=$("$PAIR" ex-code --key "$WORK/rogue-key.json" --address 127.0.0.1:1 --server-name x \
  --identity urn:baft:node:rogue --ca-file "$EX/pki/ca.pem" --psk-out "$WORK/rogue.psk" --ttl 5m)
mkdir -p "$WORK/rogue"
ROGUE_REPLY=$("$PAIR" ir-apply --code "$ROGUE" --key "$WORK/rogue-ir.json" --state-dir "$WORK/rogue" \
  --config-out "$WORK/rogue/baft.yaml")
if "$PAIR" ex-accept --reply "$ROGUE_REPLY" --pending "$EX/pairing.ex.json" --key "$EX/noise-key.json" \
   --ca-file "$EX/pki/ca.pem" --cert-file "$EX/pki/server.pem" --cert-key-file "$EX/pki/server.key" \
   --config-out "$WORK/rogue/ex.yaml" 2>"$WORK/rogue/err"; then
  echo "foreign reply accepted"; exit 1
fi
grep -q "not authentic" "$WORK/rogue/err" || { cat "$WORK/rogue/err"; exit 1; }

log "EX: accept reply, write config"
"$PAIR" ex-accept --reply "$REPLY" --pending "$EX/pairing.ex.json" --psk-file "$EX/pairing.psk" \
  --key "$EX/noise-key.json" --listen "127.0.0.1:$EX_PORT" --ca-file "$EX/pki/ca.pem" \
  --cert-file "$EX/pki/server.pem" --cert-key-file "$EX/pki/server.key" \
  --target "127.0.0.1:$TARGET_PORT" --metrics-listen "127.0.0.1:$EX_M" \
  --unix-socket "$EX/admin.sock" --config-out "$EX/baft.yaml" >/dev/null
[[ ! -e "$EX/pairing.psk" && ! -e "$EX/pairing.ex.json" ]] || { echo "one-time PSK not removed"; exit 1; }
grep -q cert_file "$IR/baft.yaml" && { echo "IR config should not carry a client certificate"; exit 1; }
"$BAFT" config validate --file "$EX/baft.yaml"
"$BAFT" config validate --file "$IR/baft.yaml"
if [[ -n "$SHAPING" ]]; then
  for f in "$EX/baft.yaml" "$IR/baft.yaml"; do
    python3 -c 'import json,sys;assert json.load(open(sys.argv[1]))["noise"]["record_shaping"]["enabled"]' "$f"
  done
fi

log "start target and EX, then IR"
python3 "$ROOT/tests/e2e/echo.py" serve "$TARGET_PORT" >"$WORK/target.log" 2>&1 & PIDS+=($!)
"$BAFT" run --file "$EX/baft.yaml" >"$WORK/ex.log" 2>&1 & PIDS+=($!)

wait_port() {
  for _ in $(seq 1 100); do
    python3 -c "import socket,sys;socket.create_connection(('127.0.0.1',$1),0.2)" 2>/dev/null && return 0
    for p in "${PIDS[@]}"; do kill -0 "$p" 2>/dev/null || { echo "process $p exited"; return 1; }; done
    sleep 0.1
  done
  echo "port $1 never opened"; return 1
}
# The dialer exits if its first carrier dial fails (systemd's Restart= covers
# that in production), so start it only once the EX is accepting.
wait_port "$EX_PORT"
"$BAFT" run --file "$IR/baft.yaml" >"$WORK/ir.log" 2>&1 & PIDS+=($!)
wait_port "$IR_PORT"

log "traffic: ${E2E_CONNS:-4} x ${E2E_MIB:-8} MiB through IR -> EX -> target"
python3 "$ROOT/tests/e2e/echo.py" send "$IR_PORT" "${E2E_MIB:-8}" "${E2E_CONNS:-4}"
for p in "${PIDS[@]:1}"; do kill -0 "$p" || { echo "a node died during traffic"; exit 1; }; done
log "PASS"
