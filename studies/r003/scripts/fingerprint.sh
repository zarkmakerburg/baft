#!/usr/bin/env bash
set -euo pipefail

PCAP="${1:?usage: fingerprint.sh <capture.pcapng> [out.tsv]}"
OUT="${2:-/dev/stdout}"

if ! command -v tshark >/dev/null 2>&1; then
  echo "tshark is required (Wireshark 4.2+ recommended for JA4)" >&2
  exit 2
fi

JA4_OK=0
if tshark -G fields 2>/dev/null | grep -q $'tls.handshake.ja4'; then
  JA4_OK=1
fi

if [[ "$JA4_OK" -eq 1 ]]; then
  tshark -r "$PCAP" \
    -Y 'tls.handshake.type == 1' \
    -T fields \
    -E header=y -E separator=$'\t' -E quote=d \
    -e frame.time_epoch \
    -e tcp.dstport \
    -e tls.handshake.extensions_server_name \
    -e tls.handshake.ja3 \
    -e tls.handshake.ja4 > "$OUT"
else
  tshark -r "$PCAP" \
    -Y 'tls.handshake.type == 1' \
    -T fields \
    -E header=y -E separator=$'\t' -E quote=d \
    -e frame.time_epoch \
    -e tcp.dstport \
    -e tls.handshake.extensions_server_name \
    -e tls.handshake.ja3 > "$OUT"
  echo "JA4 unavailable in installed tshark; use Wireshark/TShark 4.2+" >&2
fi
