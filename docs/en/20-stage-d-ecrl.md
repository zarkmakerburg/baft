# 20 — ECRL: Epoch-Fenced Correlated Resume Ledger

**Status: Stage-D research hypothesis; not yet wired into the data path.**

ECRL separates Session state from Carrier ownership. A replacement Carrier first prepares exactly `current_epoch + 1`; the old Carrier remains authoritative until an atomic commit, after which the prior epoch is fenced.

Resume reconciliation correlates each sender's transmit state with the peer's authoritative receive watermark:

```text
sender.tx_acked <= peer.rx_accepted <= sender.tx_next
```

This allows a lost ACK to be recovered without inventing bytes: replay starts at the peer's authenticated `rx_accepted`. Contradictory state is rejected as STATE_MISMATCH. A changed peer boot ID is PEER_RESTARTED because BAFT does not promise resume after process restart.

The design is motivated by gaps between TLS resumption, HTTP/2 reconnect semantics, QUIC migration, and MPTCP's data-level sequence space. It introduces no custom cryptography; authentication remains TLS 1.3 + mTLS.

The current implementation is only a pure state model with unit tests. Wire integration, tombstone reconciliation, replay transfer, and end-to-end carrier replacement remain future gates. No legal novelty or patentability claim is made.
