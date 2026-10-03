#!/usr/bin/env bash
set -euo pipefail
if [[ $# -lt 2 ]]; then
  echo "usage: $0 PCAP OUT.json"
  exit 2
fi
PCAP="$1"
OUT="$2"

fields() {
  tshark -G fields | awk -F '\t' '{print $3}'
}

HAS_JA3=0
HAS_JA4=0
fields | grep -qx 'tls.handshake.ja3' && HAS_JA3=1 || true
fields | grep -qx 'tls.handshake.ja4' && HAS_JA4=1 || true

JA3_FIELD=""
JA4_FIELD=""
[[ "$HAS_JA3" == 1 ]] && JA3_FIELD="-e tls.handshake.ja3"
[[ "$HAS_JA4" == 1 ]] && JA4_FIELD="-e tls.handshake.ja4"

TMP="$(mktemp)"
trap 'rm -f "$TMP"' EXIT

# One row per ClientHello. SNI and ALPN are included for interpretation.
tshark -r "$PCAP" -Y 'tls.handshake.type == 1' -T fields   -E separator=$'\t' -E quote=d -E occurrence=f   -e frame.time_epoch   -e ip.src -e ipv6.src   -e tcp.srcport   -e tls.handshake.extensions_server_name   -e tls.handshake.extensions_alpn_str   $JA3_FIELD $JA4_FIELD > "$TMP"

python3 - "$TMP" "$OUT" "$HAS_JA3" "$HAS_JA4" <<'PY'
import csv,json,sys
src,out,has3,has4=sys.argv[1],sys.argv[2],sys.argv[3]=="1",sys.argv[4]=="1"
rows=[]
with open(src,newline="",encoding="utf-8") as f:
    for row in csv.reader(f,delimiter="\t",quotechar='"'):
        while len(row)<8: row.append("")
        base={
          "ts_epoch":row[0],
          "src_ip":row[1] or row[2],
          "src_port":row[3],
          "sni":row[4],
          "alpn":row[5],
        }
        idx=6
        if has3:
            base["ja3"]=row[idx]; idx+=1
        if has4:
            base["ja4"]=row[idx] if idx < len(row) else ""
        rows.append(base)
with open(out,"w",encoding="utf-8") as f:
    json.dump({"schema":"r003.tls.fingerprint.v1","ja3_supported":has3,"ja4_supported":has4,"rows":rows},f,indent=2,sort_keys=True)
    f.write("\n")
PY
