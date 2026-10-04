#!/usr/bin/env bash
# End-to-end test of the WebSocket carrier transport (transport.primary=ws):
# pair a real EX/IR pair with --transport ws, run both nodes, and push real
# data through the route, verifying byte-exact delivery over the WS carrier.
# Loopback only; never targets any third-party host.
set -Eeuo pipefail
cd "$(dirname "$0")/../.."
ROOT="$PWD"
WORK="$(mktemp -d)"; PIDS=()
log() { printf '[ws-e2e] %s\n' "$*" >&2; }
cleanup() {
  local p
  for p in "${PIDS[@]:-}"; do kill "$p" 2>/dev/null || true; done
  [[ -n "${TARGET_PID:-}" ]] && kill "$TARGET_PID" 2>/dev/null || true
  rm -rf "$WORK"
  return 0
}
trap cleanup EXIT

free_port() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])'; }
wait_port() { for _ in $(seq 1 200); do python3 -c "import socket;socket.create_connection(('127.0.0.1',$1),0.2)" 2>/dev/null && return 0; sleep 0.1; done; log "port $1 never opened"; return 1; }

BIN="$WORK/bin"; go build -o "$BIN/" ./cmd/baft ./cmd/baft-pair
PAIR="$BIN/baft-pair"; BAFT="$BIN/baft"

TARGET=$(free_port); CARRIER=$(free_port); ENTRY=$(free_port)
EX="$WORK/ex"; IR="$WORK/ir"; mkdir -p "$EX" "$IR/state"
python3 tests/e2e/echo.py serve "$TARGET" >"$WORK/target.log" 2>&1 & TARGET_PID=$!
wait_port "$TARGET"

log "pair EX/IR with transport=ws"
"$PAIR" keygen --file "$EX/k.json" >/dev/null
# BAFT_WS_UTLS=1 additionally presents a browser-fidelity (uTLS) ClientHello.
# Browsers do not support Ed25519 server certificates, so uTLS requires an
# ECDSA (or RSA) cert — exactly what a Cloudflare Origin Certificate is.
UTLS_FLAG=(); PKI_KEYTYPE=()
if [[ "${BAFT_WS_UTLS:-0}" == 1 ]]; then UTLS_FLAG=(--utls); PKI_KEYTYPE=(--key-type ecdsa); log "uTLS enabled (ECDSA cert)"; fi
"$PAIR" pki --dir "$EX/pki" --host 127.0.0.1 "${PKI_KEYTYPE[@]}" >/dev/null
code=$("$PAIR" ex-code --key "$EX/k.json" --address "127.0.0.1:$CARRIER" --server-name 127.0.0.1 \
  --identity urn:baft:node:ex-ws --ca-file "$EX/pki/ca.pem" --psk-out "$EX/p.psk" --pending-out "$EX/p.json" --ttl 10m)
"$PAIR" keygen --file "$IR/k.json" >/dev/null
reply=$("$PAIR" ir-apply --code "$code" --key "$IR/k.json" --state-dir "$IR/state" --config-out "$IR/baft.yaml" \
  --transport ws "${UTLS_FLAG[@]}" --route-listen "127.0.0.1:$ENTRY" --metrics-listen "127.0.0.1:$(free_port)")
"$PAIR" ex-accept --reply "$reply" --pending "$EX/p.json" --psk-file "$EX/p.psk" --key "$EX/k.json" \
  --transport ws "${UTLS_FLAG[@]}" --listen "127.0.0.1:$CARRIER" --ca-file "$EX/pki/ca.pem" --cert-file "$EX/pki/server.pem" \
  --cert-key-file "$EX/pki/server.key" --target "127.0.0.1:$TARGET" --metrics-listen "127.0.0.1:$(free_port)" \
  --unix-socket "$EX/admin.sock" --config-out "$EX/baft.yaml" >/dev/null

grep -q '"primary": "ws"' "$EX/baft.yaml" || { log "EX config is not ws"; exit 1; }
grep -q '"primary": "ws"' "$IR/baft.yaml" || { log "IR config is not ws"; exit 1; }
if [[ "${BAFT_WS_UTLS:-0}" == 1 ]]; then
  grep -q '"utls": true' "$IR/baft.yaml" || { log "IR config did not enable utls"; exit 1; }
fi

"$BAFT" run --file "$EX/baft.yaml" >"$WORK/ex.log" 2>&1 & PIDS+=($!)
wait_port "$CARRIER" || { log "EX carrier port never opened"; tail -n 40 "$WORK/ex.log" >&2; exit 1; }
"$BAFT" run --file "$IR/baft.yaml" >"$WORK/ir.log" 2>&1 & PIDS+=($!)
wait_port "$ENTRY" || { log "IR entry port never opened"; tail -n 40 "$WORK/ir.log" >&2; exit 1; }

log "push data through the WS carrier"
# 512 KiB exercises many 32 KiB WebSocket frames and the receive-window growth,
# well past a single frame. (Multi-MiB single shots are bounded by the echo
# harness itself, equally on h2 and ws, so they are not used here.)
ok=1
for sz in 1 64 512; do
  if ! timeout 60 python3 tests/e2e/echo.py send "$ENTRY" "$sz" 1 >/dev/null 2>&1; then
    log "transfer of ${sz}KiB failed over ws carrier"; ok=0; break
  fi
done
if [[ $ok != 1 ]]; then
  log "=== ex.log ==="; tail -n 30 "$WORK/ex.log" >&2
  log "=== ir.log ==="; tail -n 30 "$WORK/ir.log" >&2
  exit 1
fi

# A probe to the carrier path that is not a WebSocket upgrade must get the
# ordinary cover page (probe resistance), not a BAFT response.
probe=$(curl -ks "https://127.0.0.1:$CARRIER/baft/v1/carrier" 2>/dev/null || true)
echo "$probe" | grep -qi '<html' || { log "probe did not receive the cover page: $probe"; exit 1; }

log "WS carrier e2e passed (byte-exact delivery + probe cover)"
