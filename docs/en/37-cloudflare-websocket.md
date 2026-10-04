# Running BAFT behind Cloudflare (WebSocket carrier)

The WebSocket carrier (`transport.primary: ws`) lets a BAFT carrier run behind
Cloudflare's proxy (orange cloud), hiding the EX origin IP and blending into
ordinary Cloudflare HTTPS. Cloudflare proxies WebSocket full-duplex streams
natively; it does not reliably forward BAFT's HTTP/2 streaming carrier, which is
why a WebSocket transport exists.

## Security model

Cloudflare terminates the outer TLS at its edge, so the carrier's outer mutual
TLS cannot reach the origin. The WebSocket carrier does not depend on it:
authentication, integrity and confidentiality come from the **inner Noise IK
handshake**, which is end to end. Cloudflare (and anyone on the path) sees only
WebSocket frames carrying Noise ciphertext. For this reason `transport.primary:
ws` **requires Noise** to be configured.

## Cloudflare setup

1. **DNS**: an `A`/`AAAA` record for your hostname pointing at the EX origin,
   with the proxy (orange cloud) **on**.
2. **SSL/TLS mode**: **Full (Strict)**, with a Cloudflare **Origin Certificate**
   installed on the EX listener (used as the EX server cert/key).
3. **Network**: WebSockets enabled (on by default on all plans). Use port 443.

## BAFT setup

Pair with the WebSocket transport:

```sh
# IR side
baft-pair ir-apply --code "$CODE" ... --transport ws --config-out /etc/baft/baft.yaml
# EX side
baft-pair ex-accept --reply "$REPLY" ... --transport ws \
  --cert-file /etc/baft/pki/origin.pem --cert-key-file /etc/baft/pki/origin.key \
  --config-out /etc/baft/baft.yaml
```

This sets `transport.primary: ws` in both configs. On the EX, use the Cloudflare
Origin Certificate as `--cert-file`/`--cert-key-file`. The IR's `peer.address`
is your Cloudflare-fronted hostname on port 443, and `peer.server_name` is that
hostname.

The EX listener then serves HTTP/1.1 + TLS and upgrades the BAFT carrier on a
WebSocket; any other request (including an unauthenticated probe) receives an
ordinary cover web page.

## Reality vs Cloudflare

Cloudflare's orange cloud and a Reality-style direct carrier are mutually
exclusive: Reality needs the TLS handshake to reach the origin untouched, which
Cloudflare's edge termination prevents. Pick one transport per carrier. BAFT is
moving toward registering more than one carrier and failing over between them
(see `reports/CARRIER-CAMOUFLAGE.md`).

## Notes

- The ws dial currently uses Go's TLS ClientHello. The uTLS browser-fingerprint
  dial (`internal/carrier/utlsdial`) will be wired under it to further blend the
  IR→Cloudflare TLS into browser traffic.
- Everything is unchanged for the default `transport.primary: h2`.
