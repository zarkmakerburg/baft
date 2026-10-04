#!/usr/bin/env python3
"""Adversary driver for BAFT resilience tests.

Every scenario targets a BAFT node started on loopback by run.sh. Nothing here
reaches any third-party host. Scenarios model two adversary roles:

  - a censor / DPI box doing active probes against the carrier port;
  - an attacker flooding or exhausting the node (incl. DDoS-style load).

  attack.py SCENARIO --carrier PORT --entry PORT [options]  -> one JSON line

Scenarios:
  connflood   open/close many carrier connections fast (SYN/accept churn)
  garbage     many connections that dump random bytes (never a valid handshake)
  slowloris   many connections that trickle 1 byte at long intervals, holding slots
  probe       active probes to the carrier: HTTP GET, random, replayed prefix;
              records whether responses are distinguishable from each other
  manyflows   open many app connections through the IR entry at once
"""
import argparse, json, os, random, socket, ssl, sys, threading, time

def connect(port, timeout=5):
    s = socket.create_connection(("127.0.0.1", port), timeout=timeout)
    return s

def connflood(a):
    end = time.monotonic() + a.seconds
    n = [0]; errs = [0]
    def worker():
        while time.monotonic() < end:
            try:
                s = connect(a.carrier, 2); s.close(); n[0] += 1
            except OSError:
                errs[0] += 1
    run_workers(worker, a.workers)
    return {"opened": n[0], "errors": errs[0]}

def garbage(a):
    end = time.monotonic() + a.seconds
    n = [0]; errs = [0]
    def worker():
        while time.monotonic() < end:
            try:
                s = connect(a.carrier, 2)
                s.sendall(os.urandom(4096)); s.recv(256); s.close(); n[0] += 1
            except OSError:
                errs[0] += 1
    run_workers(worker, a.workers)
    return {"sent": n[0], "errors": errs[0]}

def slowloris(a):
    socks = []
    for _ in range(a.conns):
        try:
            s = connect(a.carrier, 3); s.sendall(b"G"); socks.append(s)
        except OSError:
            pass
    held = len(socks)
    end = time.monotonic() + a.seconds
    while time.monotonic() < end:
        for s in list(socks):
            try:
                s.sendall(os.urandom(1))
            except OSError:
                socks.remove(s)
        time.sleep(a.interval)
    for s in socks:
        s.close()
    return {"held": held, "survived": len(socks)}

def _probe_once(port, payload, use_tls):
    """Return a fingerprint of the response: (bytes_read, closed_by_peer)."""
    try:
        s = connect(port, 5)
        if use_tls:
            ctx = ssl._create_unverified_context()
            ctx.check_hostname = False
            s = ctx.wrap_socket(s, server_hostname="127.0.0.1")
        if payload:
            s.sendall(payload)
        s.settimeout(3)
        total = 0
        closed = False
        while True:
            try:
                b = s.recv(4096)
            except (socket.timeout, ssl.SSLWantReadError):
                break
            except OSError:
                closed = True; break
            if not b:
                closed = True; break
            total += len(b)
            if total > 1 << 20:
                break
        s.close()
        return {"ok": True, "bytes": total, "closed": closed}
    except (OSError, ssl.SSLError) as e:
        return {"ok": False, "err": type(e).__name__}

def probe(a):
    # On a TLS carrier these go through a TLS handshake; a plain carrier reads
    # raw bytes. Either way, an unauthenticated probe must not be told apart
    # from another by the response alone.
    probes = {
        "http_get": b"GET / HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n",
        "random_256": os.urandom(256),
        "empty": b"",
        "baft_hello_prefix": b"\x00\x00\x00\x18\x01" + os.urandom(19),
        "replayed_random": None,  # filled below: send random, then the same again
    }
    out = {}
    fixed = os.urandom(256)
    for name, payload in probes.items():
        if name == "replayed_random":
            r1 = _probe_once(a.carrier, fixed, a.tls)
            r2 = _probe_once(a.carrier, fixed, a.tls)
            out[name] = {"first": r1, "second": r2}
        else:
            out[name] = _probe_once(a.carrier, payload, a.tls)
    return out

def manyflows(a):
    socks = []
    ok = 0; refused = 0
    for _ in range(a.conns):
        try:
            s = connect(a.entry, 3); socks.append(s); ok += 1
        except OSError:
            refused += 1
    # hold them briefly, then release
    time.sleep(min(a.seconds, 5))
    for s in socks:
        s.close()
    return {"opened": ok, "refused": refused}

def run_workers(fn, n):
    ts = [threading.Thread(target=fn) for _ in range(n)]
    for t in ts: t.start()
    for t in ts: t.join()

SCEN = {"connflood": connflood, "garbage": garbage, "slowloris": slowloris,
        "probe": probe, "manyflows": manyflows}

def main():
    p = argparse.ArgumentParser()
    p.add_argument("scenario", choices=list(SCEN))
    p.add_argument("--carrier", type=int, default=0)
    p.add_argument("--entry", type=int, default=0)
    p.add_argument("--seconds", type=float, default=5)
    p.add_argument("--workers", type=int, default=16)
    p.add_argument("--conns", type=int, default=200)
    p.add_argument("--interval", type=float, default=2)
    p.add_argument("--tls", action="store_true")
    a = p.parse_args()
    res = SCEN[a.scenario](a)
    print(json.dumps({"scenario": a.scenario, "result": res}))

if __name__ == "__main__":
    main()
