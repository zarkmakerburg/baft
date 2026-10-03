#!/usr/bin/env bash
set -euo pipefail
if [[ $# -lt 2 ]]; then
  echo "usage: $0 INPUT.pcap OUT.tsv"
  exit 2
fi
PCAP="$1"
OUT="$2"

# Private/raw diagnostic. Contains source IPs and must never be copied directly
# into the shared R-003 report.
tshark -r "$PCAP"   -Y 'tcp.flags.syn == 1 || tls.handshake.type == 1 || http.request'   -T fields -E separator=$'\t' -E quote=d -E occurrence=f   -e frame.time_epoch   -e tcp.stream   -e ip.src -e ipv6.src   -e tcp.srcport -e tcp.dstport   -e tls.handshake.extensions_server_name   -e tls.handshake.extensions_alpn_str   -e http.request.method   -e http.request.uri   > "$OUT"
