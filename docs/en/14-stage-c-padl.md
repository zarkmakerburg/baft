# 14 — Stage-C research hypothesis: PADL

**Pressure-Aged Deficit Leasing (PADL)** keeps DRR byte-deficit accounting but couples dequeue selection to replay-memory debt. Among deficit-eligible flows, lower replay pressure is preferred, while a maximum-skip aging bound prevents starvation.

The gap is BAFT-specific: plain DRR does not know how much reliability/replay memory a Flow retains; FQ-CoDel manages queue delay with AQM mechanisms that are not directly suitable for dropping reliable application-stream bytes; classical BackPressure/MaxWeight targets constrained queue stability with different delay trade-offs.

This is a research prototype. The current selector scans active flows, so its CPU cost can be O(n) per selection versus classic DRR's O(1) behavior. Stage E must measure that cost. No legal novelty/patentability claim is made.
