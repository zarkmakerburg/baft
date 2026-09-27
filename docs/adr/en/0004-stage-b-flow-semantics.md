# ADR-0004 — Stage-B Flow semantics

Status: accepted for Stage B; resource details are extended by Stage C. Date: 2026-09-27.

New Sessions complete HELLO/HELLO_ACK/two-sided READY before application frames. OPEN contains only Route ID and random nonce. DATA is offset-based per direction, duplicates are not target-written twice, ACK is distinct from target-written progress, FIN/FIN_ACK models half-close, and identical duplicate OPEN is idempotent.
