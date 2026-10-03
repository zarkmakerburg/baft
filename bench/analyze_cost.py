#!/usr/bin/env python3
import argparse, glob, json, re, statistics
from pathlib import Path

def load_rows(root, scenario):
    rows=[]
    for p in glob.glob(str(Path(root)/"**/metrics.json"), recursive=True):
        try:
            d=json.loads(Path(p).read_text())
        except Exception:
            continue
        rows += d.get("metrics",{}).get(scenario,[])
    return rows

def med(rows,key):
    vals=[float(r[key]) for r in rows if key in r]
    return statistics.median(vals) if vals else None

def profile_percent(path, needle, column="flat"):
    if not path: return None
    text=Path(path).read_text(errors="replace")
    for line in text.splitlines():
        if needle in line:
            # pprof columns: flat flat% sum% cum cum%
            m=re.search(r"\s([0-9.]+)%\s+[0-9.]+%\s+\S+\s+([0-9.]+)%\s+",line)
            if m:
                return float(m.group(1) if column=="flat" else m.group(2))
    return None

def main():
    ap=argparse.ArgumentParser()
    ap.add_argument("root")
    ap.add_argument("--heap-top",required=True)
    ap.add_argument("--block-top",required=True)
    ap.add_argument("--json-out",required=True)
    ap.add_argument("--md-out",required=True)
    a=ap.parse_args()

    rows={s:load_rows(a.root,s) for s in ["B06","B08","B10","B11"]}
    for s in ["B06","B08","B10","B11"]:
        if not rows[s]: raise SystemExit(f"missing {s} metrics")

    e06,e08,e10=[med(rows[s],"elapsed_ms") for s in ["B06","B08","B10"]]
    t06,t08,t10=[med(rows[s],"tx_mbps") for s in ["B06","B08","B10"]]
    total_excess=e06-e08
    plain_excess=e10-e08
    h2tls=e06-e10
    plain_share=100*plain_excess/total_excess if total_excess>0 else None
    h2tls_share=100*h2tls/total_excess if total_excess>0 else None

    decode_alloc=profile_percent(a.heap_top,"internal/protocol.Decode","flat")
    replay_alloc=profile_percent(a.heap_top,"internal/session.(*flow).commitSend","flat")
    reserve_block=profile_percent(a.block_top,"internal/session.(*flow).reserveReadCapacity","cum")
    ring_block=profile_percent(a.block_top,"internal/session.(*receiveRing).Peek","cum")

    decode_bpd=med(rows["B11"],"bytes_allocated_per_decode")
    decode_apd=med(rows["B11"],"allocs_per_decode")

    out={
      "sample_repetitions":{"B06":len(rows["B06"]),"B08":len(rows["B08"]),"B10":len(rows["B10"]),"B11":len(rows["B11"])},
      "median":{"B06_tx_mbps":t06,"B08_tx_mbps":t08,"B10_tx_mbps":t10,
                "B06_elapsed_ms":e06,"B08_elapsed_ms":e08,"B10_elapsed_ms":e10},
      "elapsed_excess_decomposition":{
        "total_b06_minus_direct_ms":total_excess,
        "plain_baft_minus_direct_ms":plain_excess,
        "h2_tls_increment_b06_minus_b10_ms":h2tls,
        "plain_baft_share_of_excess_percent":plain_share,
        "h2_tls_share_of_excess_percent":h2tls_share
      },
      "allocation_profile_share_percent":{
        "protocol_decode_flat":decode_alloc,
        "replay_commitSend_flat":replay_alloc
      },
      "protocol_decode_micro":{
        "median_allocs_per_decode":decode_apd,
        "median_bytes_allocated_per_decode":decode_bpd
      },
      "blocking_profile_cumulative_percent":{
        "reserveReadCapacity":reserve_block,
        "receiveRing_Peek":ring_block
      },
      "warning":"Percentages use different denominators: elapsed excess, alloc_space, and blocking delay. They are intentionally not additive."
    }
    Path(a.json_out).write_text(json.dumps(out,indent=2)+"\n")

    md=f"""# Stage E Cost Decomposition

All B06/B08/B10/B11 measurements below were collected in one workflow job on one runner.

## Same-payload throughput

| Scenario | Repeats | Median TX | Median elapsed |
|---|---:|---:|---:|
| B08 direct TCP | {len(rows['B08'])} | {t08:.2f} Mbps | {e08:.3f} ms |
| B10 BAFT Session over raw TCP | {len(rows['B10'])} | {t10:.2f} Mbps | {e10:.3f} ms |
| B06 BAFT Session over H2/TLS | {len(rows['B06'])} | {t06:.2f} Mbps | {e06:.3f} ms |

For fixed payload volume, B06 excess elapsed time above direct TCP is **{total_excess:.3f} ms**.

- Session/framing/replay/flow-control over raw TCP (B10−B08): **{plain_excess:.3f} ms**, **{plain_share:.2f}%** of the B06 excess-time denominator.
- H2+TLS incremental layer (B06−B10): **{h2tls:.3f} ms**, **{h2tls_share:.2f}%** of the same excess-time denominator.

## Allocation factors

Fresh B06 alloc_space profile:

- `protocol.Decode`: **{decode_alloc:.2f}%** of alloc_space.
- replay ledger `flow.commitSend`: **{replay_alloc:.2f}%** of alloc_space.

B11 direct Decode micro-measurement (32 KiB DATA frame):

- allocations/decode median: **{decode_apd:.2f}**
- allocated bytes/decode median: **{decode_bpd:.2f} bytes**

## Flow-control / backpressure

Fresh B06 block profile cumulative delay:

- `flow.reserveReadCapacity`: **{reserve_block:.2f}%**
- `receiveRing.Peek`: **{ring_block:.2f}%**

Block-profile cumulative percentages overlap call stacks and are not CPU shares.

## Interpretation boundary

The elapsed-time shares, allocation shares, and block-delay shares have different denominators and must not be summed. B10 isolates the carrier-layer increment without modifying production code. B11 measures current Decode behavior but does not replace it. Replay-copy and backpressure evidence come from production-path profiles only; no test hook or production optimization was introduced.
"""
    Path(a.md_out).write_text(md)

if __name__=="__main__": main()
