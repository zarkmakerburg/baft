#!/usr/bin/env python3
import json, os, subprocess, sys, tempfile

with tempfile.TemporaryDirectory() as d:
    p=os.path.join(d,"T9-ISP-A.ndjson")
    rows=[]
    # 10 healthy samples, then 30 failures -> first disruption at sample 10 and full block at same start.
    for i in range(40):
        ok=i<10
        rows.append({
          "schema":"r003.iran.sample.v1","study_id":"R003-test","tunnel_id":"T9","isp_id":"ISP-A",
          "elapsed_h":round(i/60,6),"profile":"idle",
          "checks":{
            "tunnel_app":{"ok":ok},
            "tunnel_tcp":{"ok":ok},
            "control_tcp443":{"ok":True},
            "control_https":{"ok":True},
            "icmp":{"ok":True}
          }
        })
    with open(p,"w",encoding="utf-8") as f:
        for r in rows:
            f.write(json.dumps(r)+"\n")
    jout=os.path.join(d,"summary.json")
    proc=subprocess.run([sys.executable, os.path.join(os.path.dirname(__file__),"summarize.py"), p, "--json-out", jout],
                        check=True,text=True,capture_output=True)
    got=json.load(open(jout,encoding="utf-8"))[0]
    assert got["block_type"]=="port-only", got
    assert abs(got["first_disruption_h"]-10/60)<0.001, got
    assert abs(got["full_block_h"]-10/60)<0.001, got
print("R003 summarizer synthetic test PASS")
