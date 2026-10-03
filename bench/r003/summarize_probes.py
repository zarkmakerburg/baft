#!/usr/bin/env python3
import argparse, hashlib, json, pathlib
from collections import Counter
from datetime import datetime

def main():
    ap=argparse.ArgumentParser(description="Redact and aggregate R-003 passive probe logs")
    ap.add_argument("logs",nargs="+")
    ap.add_argument("--salt",required=True,help="study-local secret salt; never put it in shared report")
    ap.add_argument("--asn-map",help="optional JSON object mapping source IP to ASN label")
    ap.add_argument("--out",required=True)
    args=ap.parse_args()
    amap={}
    if args.asn_map:
        amap=json.load(open(args.asn_map,encoding="utf-8"))
    seen={}
    asns=Counter()
    first=None
    total=0
    for path in args.logs:
        with open(path,encoding="utf-8") as f:
            for line in f:
                line=line.strip()
                if not line: continue
                x=json.loads(line)
                ip=x.get("src_ip")
                if not ip: continue
                total+=1
                token=hashlib.sha256((args.salt+"\x00"+ip).encode()).hexdigest()[:16]
                seen[token]=seen.get(token,0)+1
                if ip in amap:
                    asns[str(amap[ip])]+=1
                ts=x.get("ts_text")
                if ts and (first is None or ts<first):
                    first=ts
    out={
      "schema":"r003.probe.summary.v1",
      "unique_sources":len(seen),
      "connection_attempts":total,
      "source_tokens":sorted(seen.keys()),
      "asn_counts":dict(sorted(asns.items())),
      "first_probe_time_text":first,
      "note":"source_tokens are salted one-study pseudonyms; source IPs are omitted"
    }
    with open(args.out,"w",encoding="utf-8") as f:
        json.dump(out,f,indent=2,sort_keys=True); f.write("\n")
if __name__=="__main__":
    main()
