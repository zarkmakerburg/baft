# R-003 — Frozen Comparison Matrix

These fields must be filled before day 0 and then frozen. A row with materially different provider/region/host class is not directly comparable.

| ID | Exact implementation/version | Provider | ASN | Region | Host class | Foreign port | Domain/CDN | TLS/Noise | Config hash | Fresh IP confirmed |
|---|---|---|---|---|---|---:|---|---|---|---|
| T1 | GRE/IPIP exact variant | | | | | | n/a | n/a | | |
| T2 | reverse TCP exact tool/version | | | | | | n/a | off | | |
| T3 | WS exact tool/version | | | | | | | no TLS | | |
| T4 | WSS exact tool/version | | | | | | direct IP | self-signed | | |
| T5 | WSS exact tool/version | | | | | | domain + CDN | record edge mode | | |
| T6 | Rathole/FRP/Backhaul exact variant | | | | | | | record TLS/Noise | | |
| T7 | Xray/VLESS Reality exact version | | | | | | | Reality | | |
| T8 | Hashem + FRP exact versions | | | | | | | record FRP transport/TLS | | |
| T9 | BAFT Noise/H2/TLS1.3 shaping OFF | | | | | 8443 | record SNI | Noise on; shaping off | | |
| T10 | BAFT Noise/H2/TLS1.3 shaping ON | | | | | 8443 | record SNI | Noise on; shaping on | | |

### T8 protocol note

Hashem upstream describes a GRE Layer-3 tunnel paired with an FRP reverse relay. The study must record the installed FRP version plus `transport.protocol`, `transport.tls.enable`, and related TLS settings because those determine the actual observable FRP transport.

### IP-change follow-up

When a tunnel reaches the frozen full-block criterion, allocate a new dedicated IP of the same provider/ASN/region/host class and repeat only that tunnel with `generation=2`. Do not reuse another tunnel's IP. Compare generation-1 and generation-2 survival times descriptively.
