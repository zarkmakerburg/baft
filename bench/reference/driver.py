#!/usr/bin/env python3
import argparse, hashlib, json, socket, statistics, threading, time

def recvn(s,n):
    out=bytearray()
    while len(out)<n:
        b=s.recv(n-len(out))
        if not b: raise RuntimeError("unexpected EOF")
        out.extend(b)
    return bytes(out)

def pct(xs,q):
    ys=sorted(xs)
    if not ys:return None
    pos=(len(ys)-1)*q
    lo=int(pos); hi=min(len(ys)-1,lo+1); w=pos-lo
    return ys[lo]*(1-w)+ys[hi]*w

def one_repeat(host,port,flows,nbytes,timeout):
    ready=threading.Barrier(flows+1)
    go=threading.Event()
    errors=[]
    ttfb=[]
    lock=threading.Lock()
    payloads=[bytes(((j*17+i*29)%251 for j in range(nbytes))) for i in range(flows)]
    def worker(i):
        try:
            with socket.create_connection((host,port),timeout=timeout) as s:
                s.settimeout(timeout)
                pre=bytes((0x42,i&0xff,0x45))
                ts=time.perf_counter()
                s.sendall(pre)
                if recvn(s,len(pre))!=pre: raise RuntimeError("warmup mismatch")
                with lock:ttfb.append((time.perf_counter()-ts)*1000)
                ready.wait()
                go.wait()
                p=payloads[i]
                want=hashlib.sha256(p).digest()
                send_err=[]
                def writer():
                    try:
                        s.sendall(p)
                        s.shutdown(socket.SHUT_WR)
                    except Exception as e: send_err.append(repr(e))
                tw=threading.Thread(target=writer,daemon=True);tw.start()
                h=hashlib.sha256();got=0
                while True:
                    b=s.recv(65536)
                    if not b:break
                    got+=len(b);h.update(b)
                tw.join()
                if send_err: raise RuntimeError(send_err[0])
                if got!=len(p) or h.digest()!=want: raise RuntimeError(f"payload mismatch got={got}")
        except Exception as e:
            with lock:errors.append(f"flow={i} {e!r}")
    threads=[threading.Thread(target=worker,args=(i,),daemon=True) for i in range(flows)]
    for x in threads:x.start()
    ready.wait()
    start=time.perf_counter();go.set()
    for x in threads:x.join()
    elapsed=time.perf_counter()-start
    if errors:raise RuntimeError("; ".join(errors))
    tx=flows*nbytes
    return {
      "flows":flows,"bytes_per_flow":nbytes,"tx_bytes":tx,"rx_bytes":tx,
      "elapsed_ms":elapsed*1000,
      "tx_mbps":tx*8/elapsed/1_000_000,
      "rx_mbps":tx*8/elapsed/1_000_000,
      "ttfb_samples_ms":sorted(ttfb),
      "ttfb_ms_p50":pct(ttfb,.50),"ttfb_ms_p99":pct(ttfb,.99)
    }

def main():
    ap=argparse.ArgumentParser()
    ap.add_argument("--host",default="127.0.0.1")
    ap.add_argument("--port",type=int,required=True)
    ap.add_argument("--flows",type=int,default=8)
    ap.add_argument("--bytes-per-flow",type=int,default=1048576)
    ap.add_argument("--repeats",type=int,default=3)
    ap.add_argument("--timeout",type=float,default=60)
    ap.add_argument("--label",required=True)
    ap.add_argument("--out",required=True)
    a=ap.parse_args()
    rows=[]
    for i in range(a.repeats):
        r=one_repeat(a.host,a.port,a.flows,a.bytes_per_flow,a.timeout)
        r.update({"label":a.label,"repeat":i+1})
        rows.append(r)
        print(json.dumps(r),flush=True)
    doc={"label":a.label,"rows":rows,
         "median_tx_mbps":statistics.median(r["tx_mbps"] for r in rows),
         "ttfb_samples_ms":[x for r in rows for x in r["ttfb_samples_ms"]]}
    with open(a.out,"w") as f:json.dump(doc,f,indent=2);f.write("\n")

if __name__=="__main__":main()
