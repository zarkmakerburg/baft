#!/usr/bin/env bash
# Stage E D-008B item 2: BAFT vs FRP vs Rathole vs Xray vs direct TCP under
# identical conditions. One echo target, one probe (tput.py), the tunnel's
# carrier port delayed/lossy with netem on loopback, everything else equal.
# Measurement only; meant for a throwaway CI runner (uses sudo tc).
#
#   bench/comparators/run.sh OUT_DIR
# Env: CELLS="rtt:loss ..." (default "0:0 50:0 150:0 300:0 150:1"),
#      REPS (5), MIB (16), CONNS (1), TOOLS ("direct baft frp rathole xray")
set -Eeuo pipefail
shopt -s inherit_errexit
cd "$(dirname "$0")/../.."
ROOT="$PWD"
OUT="${1:?usage: run.sh OUT_DIR}"; mkdir -p "$OUT"
WORK="$(mktemp -d)"; PIDS=()
CELLS="${CELLS:-0:0 50:0 150:0 300:0 150:1}"; REPS="${REPS:-5}"; MIB="${MIB:-16}"; CONNS="${CONNS:-1}"
TOOLS="${TOOLS:-direct baft frp rathole xray}"
log() { printf '[cmp] %s\n' "$*" >&2; }
stop_all() { local p; for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null || true; done; for p in "${PIDS[@]}"; do wait "$p" 2>/dev/null || true; done; PIDS=(); }
netem_off() { sudo -n tc qdisc del dev lo root 2>/dev/null || true; }
cleanup() { stop_all; [[ -n "${TARGET_PID:-}" ]] && kill "$TARGET_PID" 2>/dev/null; netem_off; rm -rf "$WORK"; }
trap cleanup EXIT
free_port() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])'; }
wait_port() {
  for _ in $(seq 1 200); do
    python3 -c "import socket;socket.create_connection(('127.0.0.1',$1),0.2)" 2>/dev/null && return 0
    sleep 0.1
  done
  log "port $1 never opened"; return 1
}
netem_on() { # PORT RTT_MS LOSS_PCT: delay/lose both directions of one port
  netem_off
  [[ "$2" == 0 && "$3" == 0 ]] && return 0
  sudo -n tc qdisc add dev lo root handle 1: prio bands 3
  sudo -n tc qdisc add dev lo parent 1:3 handle 30: netem delay "$(awk "BEGIN{print $2/2}")ms" loss "$3%" limit 100000
  sudo -n tc filter add dev lo protocol ip parent 1:0 prio 3 u32 match ip dport "$1" 0xffff flowid 1:3
  sudo -n tc filter add dev lo protocol ip parent 1:0 prio 3 u32 match ip sport "$1" 0xffff flowid 1:3
}

BIN="$WORK/bin"; mkdir -p "$BIN"
fetch() { # REPO ASSET_REGEX -> path of downloaded asset; records the version
  local repo=$1 re=$2 tag url
  gh api "repos/$repo/releases/latest" >"$WORK/rel.json"
  tag="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["tag_name"])' "$WORK/rel.json")"
  url="$(python3 -c 'import json,re,sys;print(next(a["browser_download_url"] for a in json.load(open(sys.argv[1]))["assets"] if re.search(sys.argv[2],a["name"])))' "$WORK/rel.json" "$re")"
  curl -fsSL --retry 3 "$url" -o "$WORK/$(basename "$url")"
  printf '%s %s %s sha256=%s\n' "$repo" "$tag" "$(basename "$url")" "$(sha256sum "$WORK/$(basename "$url")" | cut -d' ' -f1)" >>"$OUT/provenance.txt"
  echo "$WORK/$(basename "$url")"
}

{ echo "sha=$(git rev-parse HEAD)"; go version; uname -a; lscpu | grep 'Model name' || true; echo "cells=$CELLS reps=$REPS mib=$MIB conns=$CONNS"; } >"$OUT/provenance.txt"
for t in $TOOLS; do
  case $t in
    baft) go build -o "$BIN/" ./cmd/baft ./cmd/baft-pair ;;
    frp) f="$(fetch fatedier/frp 'linux_amd64\.tar\.gz$')"; tar -xzf "$f" -C "$WORK"; cp "$WORK"/frp_*_linux_amd64/frp{s,c} "$BIN/" ;;
    rathole) f="$(fetch rapiz1/rathole 'x86_64-unknown-linux-gnu\.zip$')"; unzip -q -o "$f" -d "$WORK/rh"; cp "$WORK/rh/rathole" "$BIN/"; chmod +x "$BIN/rathole" ;;
    xray) f="$(fetch XTLS/Xray-core '^Xray-linux-64\.zip$')"; unzip -q -o "$f" -d "$WORK/xr"; cp "$WORK/xr/xray" "$BIN/"; chmod +x "$BIN/xray" ;;
  esac
done

TARGET=$(free_port)
python3 bench/comparators/tput.py serve "$TARGET" >"$WORK/target.log" 2>&1 & TARGET_PID=$!
wait_port "$TARGET"

# start_TOOL sets ENTRY (the port the probe connects to) and CARRIER (the
# tunnel port netem shapes).
start_direct() { ENTRY=$TARGET; CARRIER=$TARGET; }
start_baft() {
  local EX="$WORK/bex" IR="$WORK/bir" P="$BIN/baft-pair"; rm -rf "$EX" "$IR"; mkdir -p "$EX" "$IR/state"
  CARRIER=$(free_port); ENTRY=$(free_port)
  "$P" keygen --file "$EX/k.json" >/dev/null; "$P" pki --dir "$EX/pki" --host 127.0.0.1 >/dev/null
  local code reply
  code=$("$P" ex-code --key "$EX/k.json" --address "127.0.0.1:$CARRIER" --server-name 127.0.0.1 \
    --identity urn:baft:node:ex-bench --ca-file "$EX/pki/ca.pem" --psk-out "$EX/p.psk" --pending-out "$EX/p.json" --ttl 5m)
  "$P" keygen --file "$IR/k.json" >/dev/null
  reply=$("$P" ir-apply --code "$code" --key "$IR/k.json" --state-dir "$IR/state" --config-out "$IR/baft.yaml" \
    --route-listen "127.0.0.1:$ENTRY" --metrics-listen "127.0.0.1:$(free_port)")
  "$P" ex-accept --reply "$reply" --pending "$EX/p.json" --psk-file "$EX/p.psk" --key "$EX/k.json" \
    --listen "127.0.0.1:$CARRIER" --ca-file "$EX/pki/ca.pem" --cert-file "$EX/pki/server.pem" \
    --cert-key-file "$EX/pki/server.key" --target "127.0.0.1:$TARGET" --metrics-listen "127.0.0.1:$(free_port)" \
    --unix-socket "$EX/admin.sock" --config-out "$EX/baft.yaml" >/dev/null
  "$BIN/baft" run --file "$EX/baft.yaml" >"$WORK/bex.log" 2>&1 & PIDS+=($!)
  wait_port "$CARRIER"
  "$BIN/baft" run --file "$IR/baft.yaml" >"$WORK/bir.log" 2>&1 & PIDS+=($!)
  wait_port "$ENTRY"
}
start_frp() {
  CARRIER=$(free_port); ENTRY=$(free_port)
  printf 'bindAddr = "127.0.0.1"\nbindPort = %s\nauth.token = "bench"\n' "$CARRIER" >"$WORK/frps.toml"
  printf 'serverAddr = "127.0.0.1"\nserverPort = %s\nauth.token = "bench"\n[[proxies]]\nname = "t"\ntype = "tcp"\nlocalIP = "127.0.0.1"\nlocalPort = %s\nremotePort = %s\n' \
    "$CARRIER" "$TARGET" "$ENTRY" >"$WORK/frpc.toml"
  "$BIN/frps" -c "$WORK/frps.toml" >"$WORK/frps.log" 2>&1 & PIDS+=($!); wait_port "$CARRIER"
  "$BIN/frpc" -c "$WORK/frpc.toml" >"$WORK/frpc.log" 2>&1 & PIDS+=($!); wait_port "$ENTRY"
}
start_rathole() {
  CARRIER=$(free_port); ENTRY=$(free_port)
  printf '[server]\nbind_addr = "127.0.0.1:%s"\n[server.services.t]\ntoken = "bench"\nbind_addr = "127.0.0.1:%s"\n' "$CARRIER" "$ENTRY" >"$WORK/rs.toml"
  printf '[client]\nremote_addr = "127.0.0.1:%s"\n[client.services.t]\ntoken = "bench"\nlocal_addr = "127.0.0.1:%s"\n' "$CARRIER" "$TARGET" >"$WORK/rc.toml"
  "$BIN/rathole" -s "$WORK/rs.toml" >"$WORK/rs.log" 2>&1 & PIDS+=($!); wait_port "$CARRIER"
  "$BIN/rathole" -c "$WORK/rc.toml" >"$WORK/rc.log" 2>&1 & PIDS+=($!); wait_port "$ENTRY"; sleep 1
}
start_xray() { # VLESS over plain TCP (no TLS): the lightest Xray carrier
  CARRIER=$(free_port); ENTRY=$(free_port)
  local id=6b3d0d2e-2c1f-4c43-9a6f-2f0e1b9c7a11
  cat >"$WORK/xs.json" <<J
{"log":{"loglevel":"warning"},"inbounds":[{"listen":"127.0.0.1","port":$CARRIER,"protocol":"vless","settings":{"clients":[{"id":"$id"}],"decryption":"none"},"streamSettings":{"network":"tcp"}}],"outbounds":[{"protocol":"freedom"}]}
J
  cat >"$WORK/xc.json" <<J
{"log":{"loglevel":"warning"},"inbounds":[{"listen":"127.0.0.1","port":$ENTRY,"protocol":"dokodemo-door","settings":{"address":"127.0.0.1","port":$TARGET,"network":"tcp"}}],"outbounds":[{"protocol":"vless","settings":{"vnext":[{"address":"127.0.0.1","port":$CARRIER,"users":[{"id":"$id","encryption":"none"}]}]},"streamSettings":{"network":"tcp"}}]}
J
  "$BIN/xray" run -c "$WORK/xs.json" >"$WORK/xs.log" 2>&1 & PIDS+=($!); wait_port "$CARRIER"
  "$BIN/xray" run -c "$WORK/xc.json" >"$WORK/xc.log" 2>&1 & PIDS+=($!); wait_port "$ENTRY"
}

RESULTS="$OUT/results.jsonl"; : >"$RESULTS"
for t in $TOOLS; do
  log "== $t"
  "start_$t"
  python3 bench/comparators/tput.py run "$ENTRY" 4 1 >/dev/null || { log "$t warm-up failed"; tail -n 20 "$WORK"/*.log >&2; exit 1; }
  for cell in $CELLS; do
    rtt=${cell%%:*}; loss=${cell##*:}
    netem_on "$CARRIER" "$rtt" "$loss"
    for r in $(seq 1 "$REPS"); do
      res="$(timeout 900 python3 bench/comparators/tput.py run "$ENTRY" "$MIB" "$CONNS" || echo '{"ok": false, "mbps_per_direction": 0}')"
      printf '{"tool":"%s","rtt_ms":%s,"loss_pct":%s,"rep":%s,"result":%s}\n' "$t" "$rtt" "$loss" "$r" "$res" | tee -a "$RESULTS" >&2
    done
    netem_off
  done
  stop_all
done
kill "$TARGET_PID" 2>/dev/null || true

python3 - "$RESULTS" >"$OUT/summary.md" <<'PY'
import collections, json, math, statistics, sys
rows = [json.loads(l) for l in open(sys.argv[1])]
cells = collections.defaultdict(list); fails = collections.Counter()
for r in rows:
    k = (r["rtt_ms"], r["loss_pct"], r["tool"])
    if r["result"].get("ok"): cells[k].append(r["result"]["mbps_per_direction"])
    else: fails[k] += 1
tools = list(dict.fromkeys(r["tool"] for r in rows))
conds = sorted({(r["rtt_ms"], r["loss_pct"]) for r in rows})
print("| RTT ms | loss % | " + " | ".join(tools) + " |")
print("|---|---|" + "---|" * len(tools))
for c in conds:
    out = []
    for t in tools:
        v = cells[c + (t,)]
        if not v: out.append(f"FAIL x{fails[c+(t,)]}"); continue
        m = statistics.median(v)
        ci = 1.96 * statistics.stdev(v) / math.sqrt(len(v)) if len(v) > 1 else 0
        out.append(f"{m:.2f} (±{ci:.2f}, n={len(v)}{', fail ' + str(fails[c+(t,)]) if fails[c+(t,)] else ''})")
    print(f"| {c[0]} | {c[1]} | " + " | ".join(out) + " |")
print("\nMedian Mbps per direction, ± is the 95% CI half-width of the mean (1.96·s/√n).")
PY
cat "$OUT/summary.md"
