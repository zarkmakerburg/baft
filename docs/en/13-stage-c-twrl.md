# 13 — Stage-C research hypothesis: Tri-Watermark Receive Ledger (TWRL)

## Gap

HTTP/2 and QUIC use receiver-driven credit/limit flow control, and DRR addresses byte fairness. BAFT has an additional distinction: protocol acceptance of DATA is not the same event as delivery to the target socket. The previous implementation performed target writes in the Carrier read path, allowing a slow target to reduce Carrier liveness.

## Hypothesis

Track three monotonic watermarks:

- **A — Accepted:** contiguous bytes admitted into bounded BAFT receive memory; ACK basis.
- **D — Delivered:** bytes actually written to the target socket.
- **C — Credit:** maximum offset advertised through WINDOW.

Maintain:

```text
D ≤ A ≤ C
C - D ≤ R
```

where R is concrete reserved receive capacity.

Each Flow owns a fixed-capacity receive ring backed by the reservation. DATA enters the ring and advances A; a dedicated target writer advances D only after successful socket writes; WINDOW moves to `D + R`. FIN_ACK is delayed until all bytes through final_offset are delivered and target half-close succeeds.

This is currently a research hypothesis, not a legal novelty/patentability claim. Dedicated prior-art review is still required.


## Current measured result

The initial TWRL implementation at commit `66c4d06d...` passes unit/integration tests, the race detector, vet, and protocol fuzz smoke. The slow-receiver gate is green, and the 1 GiB COR-01 transfer still passes with the same SHA-256.

Current status: **engineering result supported in CI**. This is not yet a legal novelty/patentability claim or a public-network performance claim.
