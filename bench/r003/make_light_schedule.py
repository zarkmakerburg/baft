#!/usr/bin/env python3
import argparse, json, random
from datetime import datetime, timezone

def main():
    ap=argparse.ArgumentParser(description="Create deterministic R-003 light-traffic schedule")
    ap.add_argument("--seed",type=int,required=True)
    ap.add_argument("--hours",type=float,default=24.0)
    ap.add_argument("--min-gap",type=float,default=20.0)
    ap.add_argument("--max-gap",type=float,default=180.0)
    ap.add_argument("--min-bytes",type=int,default=4096)
    ap.add_argument("--max-bytes",type=int,default=262144)
    ap.add_argument("--out",required=True)
    args=ap.parse_args()
    if args.min_gap <= 0 or args.max_gap < args.min_gap:
        raise SystemExit("invalid gap range")
    if args.min_bytes <= 0 or args.max_bytes < args.min_bytes:
        raise SystemExit("invalid byte range")
    rng=random.Random(args.seed)
    t=0.0
    events=[]
    horizon=args.hours*3600
    while t < horizon:
        t += rng.uniform(args.min_gap,args.max_gap)
        if t >= horizon:
            break
        events.append({"at_s":round(t,3),"bytes":rng.randint(args.min_bytes,args.max_bytes)})
    doc={
      "schema":"r003.traffic.schedule.v1",
      "created_utc":datetime.now(timezone.utc).isoformat(),
      "seed":args.seed,
      "hours":args.hours,
      "events":events
    }
    with open(args.out,"w",encoding="utf-8") as f:
        json.dump(doc,f,indent=2,sort_keys=True); f.write("\n")
    print(json.dumps({"events":len(events),"scheduled_bytes":sum(x["bytes"] for x in events)},sort_keys=True))
if __name__=="__main__":
    main()
