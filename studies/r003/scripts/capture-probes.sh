#!/usr/bin/env bash
set -euo pipefail

PORT="${1:?usage: capture-probes.sh <listener-port> <output.pcap>}"
OUT="${2:?usage: capture-probes.sh <listener-port> <output.pcap>}"

if ! command -v tcpdump >/dev/null 2>&1; then
  echo "tcpdump is required" >&2
  exit 2
fi

umask 077
mkdir -p "$(dirname "$OUT")"

# Raw evidence only. Keep OUT under studies/r003/private/ or another protected path.
# snaplen 384 retains connection metadata and initial application bytes without
# intentionally collecting full sessions.
exec tcpdump -i any -nn -s 384 -U -w "$OUT" "tcp dst port $PORT"
