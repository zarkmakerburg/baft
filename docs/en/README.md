# BAFT Documentation Guide

[مستندات فارسی](../fa/README.md) | [English root README](../../README.en.md)

This documentation is written for readers with no prior BAFT context. Recommended order:

1. [01 — Overview, goals, and scope](01-overview.md)
2. [02 — Architecture, roles, and data flow](02-architecture.md)
3. [03 — Security, identity, and trust boundaries](03-security-model.md)
4. [04 — BAFT/1 protocol and state machine](04-protocol-baft1.md)
5. [05 — Configuration and Routes](05-configuration.md)
6. [06 — Building and running IR/EX](06-running-ir-ex.md)
7. [07 — Memory control, backpressure, and scheduling](07-resource-control.md)
8. [08 — Testing, CI, and acceptance gates](08-testing-and-ci.md)
9. [09 — Roadmap and Stages A–H](09-roadmap.md)
10. [10 — Repository layout and module responsibilities](10-repository-layout.md)
11. [11 — Glossary](11-glossary.md)
12. [12 — Innovation methodology](12-innovation-method.md)
13. [13 — Stage-C TWRL hypothesis](13-stage-c-twrl.md)

## Source hierarchy

When documents disagree, use this order:

1. content Blueprint 1.4 and Implementation Master Prompt 1.2;
2. accepted ADRs for implementation decisions;
3. schema, wire tests, and golden vectors for executable contracts;
4. STATUS and TEST-RESULTS for what has actually been built and measured;
5. these guides for explanation.

A feature described by the Blueprint but not recorded as implemented in STATUS is a **planned contract**, not a current capability.

14. [14 — Stage-C PADL hypothesis](14-stage-c-padl.md)

15. [15 — Conservation telemetry and multi-Shard budget](15-conservation-telemetry.md)

16. [16 — Privacy-bounded conservation metrics](16-conservation-metrics.md)

17. [17 — Single-flight correctness gates](17-correctness-gates.md)

18. [18 — Stage-C soak gate](18-stage-c-soak.md)
