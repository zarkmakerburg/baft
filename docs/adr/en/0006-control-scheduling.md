# ADR-0006 — Bounded control scheduling

Status: accepted. Date: 2026-09-27.

Control traffic must not be blocked behind bulk DATA, while DATA must not starve under unlimited control traffic. The sender uses a bounded control queue (256 messages or 1 MiB), byte-based DRR for DATA, normal control priority, and a finite control burst cap of 32 when DATA is waiting. The value 32 is an implementation tuning value, not a wire constant.
