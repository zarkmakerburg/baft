#!/usr/bin/env bash
set -euo pipefail
MODE="${1:?apply|clear}"
if [[ "$MODE" == clear ]]; then
  sudo -n tc qdisc del dev lo root 2>/dev/null || true
  exit 0
fi
PORT="${2:?outer port}"
RTT="${3:?RTT ms}"
LOSS="${4:?loss percent}"
RATE="${5:?rate Mbit/s}"
sudo -n tc qdisc del dev lo root 2>/dev/null || true
sudo -n tc qdisc add dev lo root handle 1: prio bands 3
HALF=$(python3 - <<PY
print(float("$RTT")/2)
PY
)
sudo -n tc qdisc add dev lo parent 1:3 handle 30: netem delay "${HALF}ms" loss "${LOSS}%" rate "${RATE}mbit"
sudo -n tc filter add dev lo protocol ip parent 1: prio 3 u32 match ip protocol 6 0xff match ip dport "$PORT" 0xffff flowid 1:3
sudo -n tc filter add dev lo protocol ip parent 1: prio 3 u32 match ip protocol 6 0xff match ip sport "$PORT" 0xffff flowid 1:3
