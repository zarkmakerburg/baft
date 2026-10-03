#!/usr/bin/env python3
import argparse, glob, json, math, statistics
from collections import defaultdict
from pathlib import Path

def pct(xs,q):
    ys=sorted(float(x) for x in xs)
    if not ys: return float("nan")
    pos=(len(ys)-1)*q
    lo=int(math.floor(pos)); hi=int(math.ceil(pos))
    if lo==hi: return ys[lo]
    w=pos-lo
    return ys[lo]*(1-w)+ys[hi]*w

def load(root):
    out=[]
    for p in glob.glob(str(Path(root)/"**/metrics.json"),recursive=True):
        try:d=json.loads(Path(p).read_text())
        except Exception:continue
        for key in ("B12","B13"):
            for row in d.get("metrics",{}).get(key,[]):
                row=dict(row); row["_scenario_key"]=key; row["_file"]=p; out.append(row)
    return out

def key(row):
    return (int(row.get("netem_rtt_ms",0)), float(row.get("netem_loss_pct",0) or 0), int(row.get("netem_rate_mbit",0)))

def main():
    ap=argparse.ArgumentParser()
    ap.add_argument("root")
    ap.add_argument("--json-out",required=True)
    ap.add_argument("--md-out",required=True)
    a=ap.parse_args()

    rows=load(a.root)
    groups=defaultdict(lambda:{"throughput":[],"ttfb":[],"recovery":[],"survived":0,"attempted":0,"cuts":0})
    for r in rows:
        k=key(r)
        g=groups[k]
        if r["_scenario_key"]=="B12":
            if "tx_mbps" in r:g["throughput"].append(float(r["tx_mbps"]))
            g["ttfb"].extend(float(x) for x in r.get("ttfb_samples_ms",[]))
        else:
            g["recovery"].extend(float(x) for x in r.get("recovery_samples_ms",[]))
            g["survived"] += int(r.get("flows_survived",0))
            g["attempted"] += int(r.get("flows",0))
            g["cuts"] += 1

    out=[]
    for k in sorted(groups):
        rtt,loss,rate=k; g=groups[k]
        if not g["throughput"] or not g["recovery"]: continue
        out.append({
          "rtt_ms":rtt,"loss_pct":loss,"rate_mbit":rate,
          "throughput_repeats":len(g["throughput"]),
          "throughput_mbps_median":statistics.median(g["throughput"]),
          "throughput_mbps_min":min(g["throughput"]),
          "throughput_mbps_max":max(g["throughput"]),
          "ttfb_samples":len(g["ttfb"]),
          "ttfb_ms_p50":pct(g["ttfb"],.50),
          "ttfb_ms_p99":pct(g["ttfb"],.99),
          "recovery_cuts":g["cuts"],
          "recovery_samples":len(g["recovery"]),
          "recovery_ms_p50":pct(g["recovery"],.50),
          "recovery_ms_p99":pct(g["recovery"],.99),
          "flow_survival_percent":100*g["survived"]/g["attempted"] if g["attempted"] else None,
          "flow_survived":g["survived"],"flow_attempted":g["attempted"]
        })
    if len(out)!=18:
        raise SystemExit(f"expected 18 WAN profiles, found {len(out)}")

    Path(a.json_out).write_text(json.dumps({"profiles":out},indent=2)+"\n")
    lines=[
      "# Stage E WAN netem matrix","",
      "Carrier-only tc netem; local application and target sockets are not intentionally impaired.","",
      "| RTT | Loss | Cap | Throughput median | TTFB p50 / p99 | Recovery p50 / p99 | Recovery samples | Survival |",
      "|---:|---:|---:|---:|---:|---:|---:|---:|"
    ]
    for x in out:
        lines.append(f"| {x['rtt_ms']} ms | {x['loss_pct']:.1f}% | {x['rate_mbit']} Mbit/s | {x['throughput_mbps_median']:.2f} Mbps | {x['ttfb_ms_p50']:.1f} / {x['ttfb_ms_p99']:.1f} ms | {x['recovery_ms_p50']:.1f} / {x['recovery_ms_p99']:.1f} ms | {x['recovery_samples']} | {x['flow_survived']}/{x['flow_attempted']} ({x['flow_survival_percent']:.2f}%) |")
    lines += ["",
      "Throughput: 3 repeats/profile, 8 flows × 1 MiB per flow.",
      "TTFB: pooled first-response samples from those throughput runs.",
      "Recovery: 5 cuts/profile, 32 existing flows/cut (160 flow samples/profile).",
      "Nominal RTT is implemented as half-RTT delay on each matched carrier direction. Loss and rate are tc netem parameters on the matched carrier port.",
      "p99 at 160 recovery samples/profile is usable as an exploratory estimate, but this WAN matrix is not a substitute for Internet-path measurements."
    ]
    Path(a.md_out).write_text("\n".join(lines)+"\n")

if __name__=="__main__":main()
