#!/usr/bin/env python3
import argparse, json, pathlib
from datetime import datetime

def parse(path):
    out=[]
    with open(path,encoding="utf-8") as f:
        for line in f:
            line=line.strip()
            if line:
                out.append(json.loads(line))
    return out

def check_ok(s, name):
    x=s.get("checks",{}).get(name)
    return None if x is None else bool(x.get("ok"))

def classify_window(s):
    tun=check_ok(s,"tunnel_app")
    tport=check_ok(s,"tunnel_tcp")
    c443=check_ok(s,"control_tcp443")
    https=check_ok(s,"control_https")
    icmp=check_ok(s,"icmp")
    if tun is False and tport is False and c443 is False and https is False:
        return "ip-wide" if icmp is False else "ip-wide-or-path"
    if tun is False and tport is False and (https is True or c443 is True):
        return "port-only"
    if tun is False and tport is True and https is True:
        return "protocol-reset-or-app-failure"
    return "unknown"

def consecutive_failure_start(samples, n):
    streak=0
    start=None
    healthy_seen=0
    for s in samples:
        app=check_ok(s,"tunnel_app")
        if app is True:
            healthy_seen += 1
            streak=0
            start=None
        elif app is False and healthy_seen >= 10:
            if streak == 0:
                start=s
            streak += 1
            if streak >= n:
                return start
        else:
            streak=0
            start=None
    return None

def full_block_start(samples, minutes, interval):
    need=max(1, int((minutes*60)/interval))
    streak=0
    start=None
    for s in samples:
        app=check_ok(s,"tunnel_app")
        if app is False:
            if streak == 0:
                start=s
            streak += 1
            if streak >= need:
                return start
        else:
            streak=0
            start=None
    return None

def main():
    ap=argparse.ArgumentParser()
    ap.add_argument("logs",nargs="+")
    ap.add_argument("--interval",type=int,default=60)
    ap.add_argument("--first-n",type=int,default=3)
    ap.add_argument("--full-block-minutes",type=int,default=30)
    ap.add_argument("--json-out")
    args=ap.parse_args()
    rows=[]
    for p in args.logs:
        samples=parse(p)
        if not samples:
            continue
        first=consecutive_failure_start(samples,args.first_n)
        full=full_block_start(samples,args.full_block_minutes,args.interval)
        pivot=full or first
        rows.append({
            "file":pathlib.Path(p).name,
            "study_id":samples[0].get("study_id"),
            "tunnel_id":samples[0].get("tunnel_id"),
            "isp_id":samples[0].get("isp_id"),
            "samples":len(samples),
            "first_disruption_h":None if not first else first.get("elapsed_h"),
            "full_block_h":None if not full else full.get("elapsed_h"),
            "block_type":"none" if pivot is None else classify_window(pivot),
        })
    print("tunnel\tisp\tfirst_disruption_h\tfull_block_h\tblock_type\tsamples")
    for r in rows:
        print("%s\t%s\t%s\t%s\t%s\t%d" % (
            r["tunnel_id"],r["isp_id"],r["first_disruption_h"],r["full_block_h"],r["block_type"],r["samples"]
        ))
    if args.json_out:
        with open(args.json_out,"w",encoding="utf-8") as f:
            json.dump(rows,f,indent=2,sort_keys=True)
            f.write("\n")

if __name__=="__main__":
    main()
