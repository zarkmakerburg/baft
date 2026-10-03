#!/usr/bin/env python3
import argparse
import json
import math
import random
from pathlib import Path

def pct(xs, q):
    if not xs:
        return float("nan")
    ys=sorted(xs)
    pos=(len(ys)-1)*q
    lo=int(math.floor(pos)); hi=int(math.ceil(pos))
    if lo==hi: return ys[lo]
    w=pos-lo
    return ys[lo]*(1-w)+ys[hi]*w

def bootstrap_ci(xs, q, rounds=5000, seed=7007):
    rng=random.Random(seed + int(q*10000))
    n=len(xs)
    vals=[]
    for _ in range(rounds):
        sample=[xs[rng.randrange(n)] for __ in range(n)]
        vals.append(pct(sample,q))
    vals.sort()
    return pct(vals,0.025), pct(vals,0.975)

def wilson(successes,total,z=1.959963984540054):
    if total==0: return (float("nan"),float("nan"))
    p=successes/total
    den=1+z*z/total
    center=(p+z*z/(2*total))/den
    half=z*math.sqrt((p*(1-p)+z*z/(4*total))/total)/den
    return center-half,center+half

def main():
    ap=argparse.ArgumentParser()
    ap.add_argument("metrics_json")
    ap.add_argument("--json-out",default="b07-statistics.json")
    ap.add_argument("--md-out",default="b07-statistics.md")
    args=ap.parse_args()

    doc=json.loads(Path(args.metrics_json).read_text())
    rows=doc.get("metrics",{}).get("B07",[])
    if not rows:
        raise SystemExit("no B07 metrics found")

    samples=[]
    successes=0
    attempted=0
    target_reopens=0
    logical_survived=0
    for row in rows:
        vals=[float(x) for x in row.get("recovery_samples_ms",[])]
        samples.extend(vals)
        attempted += int(row.get("flows",0))
        successes += int(row.get("flows_survived",0))
        if row.get("logical_session_survived"): logical_survived += 1
        target_reopens += max(0,int(row.get("target_accepts_after",0))-int(row.get("target_accepts_before",0)))

    p50=pct(samples,0.50)
    p99=pct(samples,0.99)
    p50_ci=bootstrap_ci(samples,0.50)
    p99_ci=bootstrap_ci(samples,0.99)
    s_ci=wilson(successes,attempted)

    out={
      "cut_count":len(rows),
      "flows_per_cut": [int(r.get("flows",0)) for r in rows],
      "recovery_sample_count":len(samples),
      "flow_survival_successes":successes,
      "flow_survival_attempts":attempted,
      "flow_survival_percent":100*successes/attempted if attempted else None,
      "flow_survival_wilson_95_percent":[100*s_ci[0],100*s_ci[1]],
      "logical_session_survived_cuts":logical_survived,
      "target_tcp_reopens":target_reopens,
      "recovery_ms":{
        "p50":p50,
        "p50_bootstrap_95":[p50_ci[0],p50_ci[1]],
        "p99":p99,
        "p99_bootstrap_95":[p99_ci[0],p99_ci[1]],
        "min":min(samples),
        "max":max(samples)
      },
      "bootstrap_rounds":5000,
      "bootstrap_seed":7007,
      "note":"Aggregate p50/p99 are across all flow probes from repeated 64-flow cuts. Per-cut p99 remains resolution-limited at n=64."
    }
    Path(args.json_out).write_text(json.dumps(out,indent=2)+"\n")

    md=f"""# Stage E B07 Recovery Statistics

- Cuts: **{len(rows)}**
- Flow population per cut: **{min(out['flows_per_cut'])}..{max(out['flows_per_cut'])}**
- Total recovery samples: **{len(samples)}**
- Flow survival: **{successes}/{attempted} ({out['flow_survival_percent']:.4f}%)**
- Survival Wilson 95% CI: **{100*s_ci[0]:.4f}% .. {100*s_ci[1]:.4f}%**
- Logical-session survival: **{logical_survived}/{len(rows)} cuts**
- Target TCP reopens: **{target_reopens}**

| Metric | Estimate | Bootstrap 95% CI |
|---|---:|---:|
| Recovery p50 | {p50:.3f} ms | {p50_ci[0]:.3f} .. {p50_ci[1]:.3f} ms |
| Recovery p99 | {p99:.3f} ms | {p99_ci[0]:.3f} .. {p99_ci[1]:.3f} ms |

Range: **{min(samples):.3f} .. {max(samples):.3f} ms**

Confidence method: deterministic non-parametric bootstrap, 5000 resamples, seed 7007. Survival interval uses Wilson 95%.

Limitation: the production shard ceiling is 64 active flows, so a single-cut p99 is intrinsically coarse. The reported formal p99 is the aggregate distribution across repeated cuts on the same runner and payload probe profile.
"""
    Path(args.md_out).write_text(md)

if __name__=="__main__":
    main()
