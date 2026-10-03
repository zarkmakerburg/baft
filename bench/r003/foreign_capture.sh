#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: $0 --iface IFACE --port PORT --out-dir DIR [--known-client-file FILE]"
  exit 2
}

IFACE=""
PORT=""
OUT=""
KNOWN=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --iface) IFACE="$2"; shift 2 ;;
    --port) PORT="$2"; shift 2 ;;
    --out-dir) OUT="$2"; shift 2 ;;
    --known-client-file) KNOWN="$2"; shift 2 ;;
    *) usage ;;
  esac
done
[[ -n "$IFACE" && -n "$PORT" && -n "$OUT" ]] || usage

mkdir -p "$OUT"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
PCAP="$OUT/capture-$STAMP.pcap"
TEXT="$OUT/connections-$STAMP.ndjson"

FILTER="tcp port $PORT"
if [[ -n "$KNOWN" && -s "$KNOWN" ]]; then
  while IFS= read -r ip; do
    [[ -z "$ip" ]] && continue
    [[ "${ip:0:1}" == "#" ]] && continue
    FILTER="$FILTER and not host $ip"
  done < "$KNOWN"
fi

echo "R-003 passive capture: iface=$IFACE port=$PORT"
echo "pcap=$PCAP"
echo "filter=$FILTER"

sudo tcpdump -i "$IFACE" -nn -s 0 -U -w "$PCAP" "$FILTER" &
TCPDUMP_PID=$!

cleanup() {
  kill "$TCPDUMP_PID" 2>/dev/null || true
  wait "$TCPDUMP_PID" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

# Sidecar connection summary. Raw source IPs stay in private raw logs only.
sudo tcpdump -i "$IFACE" -nn -l -tttt "tcp dst port $PORT and tcp[tcpflags] & tcp-syn != 0" |
while IFS= read -r line; do
  python3 - "$line" >> "$TEXT" <<'PY'
import json, re, sys
line=sys.argv[1]
m=re.search(r'^(\S+\s+\S+).* IP ([0-9a-fA-F:.]+)\.(\d+) > ([0-9a-fA-F:.]+)\.(\d+):', line)
obj={"schema":"r003.foreign.syn.v1","raw":line[:500]}
if m:
    obj.update({"ts_text":m.group(1),"src_ip":m.group(2),"src_port":int(m.group(3)),"dst_ip":m.group(4),"dst_port":int(m.group(5))})
print(json.dumps(obj,separators=(",",":"),sort_keys=True))
PY
done
