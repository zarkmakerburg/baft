#!/usr/bin/env python3
import argparse, json, time, urllib.request, ssl
from datetime import datetime, timezone

def now():
    return datetime.now(timezone.utc).isoformat()

def get_bytes(url, want, timeout):
    req=urllib.request.Request(url,headers={"User-Agent":"BAFT-R003-Traffic/1","X-R003-Bytes":str(want)})
    ctx=ssl._create_unverified_context()
    got=0
    start=time.monotonic()
    try:
        with urllib.request.urlopen(req,timeout=timeout,context=ctx) as r:
            while got < want:
                b=r.read(min(65536,want-got))
                if not b:
                    break
                got += len(b)
        return {"ok":got==want,"bytes":got,"elapsed_s":round(time.monotonic()-start,6)}
    except Exception as e:
        return {"ok":False,"bytes":got,"elapsed_s":round(time.monotonic()-start,6),
                "error":type(e).__name__,"detail":str(e)[:160]}

def emit(out,obj):
    out.write(json.dumps(obj,separators=(",",":"),sort_keys=True)+"\n")

def light(args,out):
    doc=json.load(open(args.schedule,encoding="utf-8"))
    started=time.monotonic()
    attempted=delivered=0
    for idx,event in enumerate(doc["events"]):
        target=started+float(event["at_s"])
        delay=target-time.monotonic()
        if delay>0: time.sleep(delay)
        want=int(event["bytes"])
        attempted += want
        r=get_bytes(args.url,want,args.timeout)
        delivered += int(r["bytes"])
        emit(out,{"schema":"r003.traffic.event.v1","ts_utc":now(),"profile":"light","event":idx,
                  "attempted_bytes":want,"delivered_bytes":r["bytes"],"ok":r["ok"],"detail":r})
    return attempted,delivered

def sustained(args,out):
    target_Bps=args.target_mbit_s*1_000_000/8.0
    started=time.monotonic()
    deadline=started+args.duration_minutes*60
    attempted=delivered=0
    seq=0
    object_bytes=args.object_bytes
    while time.monotonic() < deadline:
        want=object_bytes
        attempted += want
        r=get_bytes(args.url,want,args.timeout)
        delivered += int(r["bytes"])
        emit(out,{"schema":"r003.traffic.event.v1","ts_utc":now(),"profile":"sustained","event":seq,
                  "attempted_bytes":want,"delivered_bytes":r["bytes"],"ok":r["ok"],"detail":r})
        seq += 1
        expected_elapsed=delivered/target_Bps if target_Bps>0 else 0
        actual_elapsed=time.monotonic()-started
        if expected_elapsed>actual_elapsed:
            time.sleep(min(expected_elapsed-actual_elapsed,max(0,deadline-time.monotonic())))
    return attempted,delivered

def main():
    ap=argparse.ArgumentParser(description="Run identical R-003 application traffic through a tunnel")
    ap.add_argument("--profile",choices=["light","sustained"],required=True)
    ap.add_argument("--url",required=True,help="dedicated test object endpoint exposed through the tunnel")
    ap.add_argument("--schedule")
    ap.add_argument("--target-mbit-s",type=float,default=7.5)
    ap.add_argument("--duration-minutes",type=float,default=30.0)
    ap.add_argument("--object-bytes",type=int,default=4*1024*1024)
    ap.add_argument("--timeout",type=float,default=30.0)
    ap.add_argument("--output",required=True)
    args=ap.parse_args()
    if args.profile=="light" and not args.schedule:
        raise SystemExit("--schedule required for light profile")
    with open(args.output,"a",encoding="utf-8",buffering=1) as out:
        attempted,delivered=(light(args,out) if args.profile=="light" else sustained(args,out))
        emit(out,{"schema":"r003.traffic.summary.v1","ts_utc":now(),"profile":args.profile,
                  "attempted_bytes":attempted,"delivered_bytes":delivered,
                  "delivery_ratio":0 if attempted==0 else delivered/attempted})
if __name__=="__main__":
    main()
