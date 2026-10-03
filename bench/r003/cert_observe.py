#!/usr/bin/env python3
import argparse, hashlib, json, socket, ssl
from datetime import datetime, timezone

def main():
    ap=argparse.ArgumentParser(description="R-003 TLS certificate observer")
    ap.add_argument("--host",required=True)
    ap.add_argument("--port",type=int,required=True)
    ap.add_argument("--sni",required=True)
    ap.add_argument("--out",required=True)
    args=ap.parse_args()

    ctx=ssl.create_default_context()
    ctx.check_hostname=False
    ctx.verify_mode=ssl.CERT_NONE
    with socket.create_connection((args.host,args.port),timeout=8) as raw:
        with ctx.wrap_socket(raw,server_hostname=args.sni) as s:
            der=s.getpeercert(binary_form=True)
            info=s.getpeercert()
            obj={
                "schema":"r003.cert.observation.v1",
                "ts_utc":datetime.now(timezone.utc).isoformat(),
                "sni":args.sni,
                "tls_version":s.version(),
                "alpn":s.selected_alpn_protocol(),
                "leaf_sha256":hashlib.sha256(der).hexdigest() if der else None,
                "subject":info.get("subject"),
                "issuer":info.get("issuer"),
                "notBefore":info.get("notBefore"),
                "notAfter":info.get("notAfter"),
                "subjectAltName":info.get("subjectAltName"),
            }
    with open(args.out,"w",encoding="utf-8") as f:
        json.dump(obj,f,indent=2,sort_keys=True)
        f.write("\n")
if __name__=="__main__":
    main()
