#!/usr/bin/env python3
import argparse, hashlib, json, socket, ssl, subprocess, time, urllib.request
from datetime import datetime, timezone

def utcnow():
    return datetime.now(timezone.utc).isoformat()

def ms(dt):
    return round(dt * 1000.0, 3)

def ping_once(host, timeout):
    start = time.monotonic()
    try:
        p = subprocess.run(
            ["ping", "-n", "-c", "1", "-W", str(max(1, int(timeout))), host],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
            timeout=timeout + 1,
        )
        return {"ok": p.returncode == 0, "ms": ms(time.monotonic() - start), "rc": p.returncode}
    except Exception as e:
        return {"ok": False, "ms": ms(time.monotonic() - start), "error": type(e).__name__}

def tcp_probe(host, port, timeout):
    start = time.monotonic()
    try:
        with socket.create_connection((host, port), timeout=timeout):
            return {"ok": True, "ms": ms(time.monotonic() - start)}
    except Exception as e:
        return {"ok": False, "ms": ms(time.monotonic() - start), "error": type(e).__name__, "detail": str(e)[:160]}

def tls_http_probe(ip, port, server_name, path, timeout):
    start = time.monotonic()
    try:
        ctx = ssl.create_default_context()
        ctx.check_hostname = False
        ctx.verify_mode = ssl.CERT_NONE
        with socket.create_connection((ip, port), timeout=timeout) as raw:
            with ctx.wrap_socket(raw, server_hostname=server_name) as s:
                req = (
                    "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n"
                    "User-Agent: BAFT-R003/1\r\n\r\n" % (path, server_name)
                ).encode()
                s.sendall(req)
                data = s.recv(4096)
                first = data.split(b"\r\n", 1)[0].decode("latin1", "replace") if data else ""
                cert = s.getpeercert(binary_form=True)
                return {
                    "ok": first.startswith("HTTP/"),
                    "ms": ms(time.monotonic() - start),
                    "status_line": first[:120],
                    "cert_sha256": hashlib.sha256(cert).hexdigest() if cert else None,
                    "tls_version": s.version(),
                    "alpn": s.selected_alpn_protocol(),
                }
    except Exception as e:
        return {"ok": False, "ms": ms(time.monotonic() - start), "error": type(e).__name__, "detail": str(e)[:160]}

def url_probe(url, timeout):
    start = time.monotonic()
    try:
        req = urllib.request.Request(url, headers={"User-Agent": "BAFT-R003/1"})
        ctx = ssl._create_unverified_context()
        with urllib.request.urlopen(req, timeout=timeout, context=ctx) as r:
            body = r.read(4096)
            return {"ok": 200 <= r.status < 500, "ms": ms(time.monotonic() - start), "status": r.status, "bytes": len(body)}
    except Exception as e:
        return {"ok": False, "ms": ms(time.monotonic() - start), "error": type(e).__name__, "detail": str(e)[:160]}

def main():
    ap = argparse.ArgumentParser(description="R-003 Iran-side reachability sampler")
    ap.add_argument("--study-id", required=True)
    ap.add_argument("--tunnel-id", required=True)
    ap.add_argument("--isp-id", required=True)
    ap.add_argument("--target-ip", required=True)
    ap.add_argument("--tunnel-port", required=True, type=int)
    ap.add_argument("--control-https-port", type=int, default=443)
    ap.add_argument("--control-sni")
    ap.add_argument("--control-path", default="/")
    ap.add_argument("--tunnel-sni")
    ap.add_argument("--app-url")
    ap.add_argument("--profile", choices=["light", "sustained", "idle"], default="idle")
    ap.add_argument("--interval", type=float, default=60.0)
    ap.add_argument("--timeout", type=float, default=5.0)
    ap.add_argument("--output", required=True)
    args = ap.parse_args()

    started = time.monotonic()
    next_tick = started
    with open(args.output, "a", encoding="utf-8", buffering=1) as out:
        while True:
            now = time.monotonic()
            if now < next_tick:
                time.sleep(next_tick - now)
            sample = {
                "schema": "r003.iran.sample.v1",
                "ts_utc": utcnow(),
                "elapsed_h": round((time.monotonic() - started) / 3600.0, 6),
                "study_id": args.study_id,
                "tunnel_id": args.tunnel_id,
                "isp_id": args.isp_id,
                "profile": args.profile,
                "checks": {},
            }
            sample["checks"]["icmp"] = ping_once(args.target_ip, args.timeout)
            sample["checks"]["tunnel_tcp"] = tcp_probe(args.target_ip, args.tunnel_port, args.timeout)
            sample["checks"]["control_tcp443"] = tcp_probe(args.target_ip, args.control_https_port, args.timeout)
            if args.control_sni:
                sample["checks"]["control_https"] = tls_http_probe(
                    args.target_ip, args.control_https_port, args.control_sni, args.control_path, args.timeout
                )
            if args.tunnel_sni:
                sample["checks"]["tunnel_https_cover"] = tls_http_probe(
                    args.target_ip, args.tunnel_port, args.tunnel_sni, "/", args.timeout
                )
            if args.app_url:
                sample["checks"]["tunnel_app"] = url_probe(args.app_url, args.timeout)
            out.write(json.dumps(sample, separators=(",", ":"), sort_keys=True) + "\n")
            next_tick += args.interval

if __name__ == "__main__":
    main()
