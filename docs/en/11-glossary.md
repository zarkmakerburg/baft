# 11 — Glossary

| Term | BAFT meaning |
|---|---|
| Node | one BAFT process/agent acting as dialer or listener |
| IR | baseline Carrier initiator role |
| EX | baseline Carrier listener role |
| Carrier | authenticated bidirectional byte stream transporting BAFT frames |
| Shard | independent Carrier plus Session/scheduling unit |
| Session | in-memory state associated with one Shard |
| Flow | one bidirectional application TCP connection |
| Route | preconfigured authorized mapping to a local listener or fixed target |
| stream_id | Flow identifier within a Session |
| offset | directional byte position or type-specific state value |
| ACK | receiver's contiguous `next_expected`; BAFT acceptance |
| target written | bytes actually written to the target socket; distinct from ACK |
| WINDOW | absolute `max_offset` credit announcement |
| Replay | unacknowledged bytes retained for recovery semantics |
| Half-close | closing one TCP direction while keeping the opposite direction alive |
| Epoch | Carrier generation used for fencing |
| Snapshot | future resume-state image |
| Tombstone | short-lived state preventing ended Flows from being resurrected |
| Profile | versioned scheduling/resource configuration |
| Goodput | useful bytes delivered per unit time excluding protocol overhead |
| Backpressure | propagating bounded receiver capacity toward the source |
| DRR | Deficit Round Robin byte scheduler |
| mTLS | mutual TLS authentication |
| URI SAN | certificate identity URI used for Node identity |
| COR-01 | 1 GiB bidirectional end-to-end hash correctness gate |
