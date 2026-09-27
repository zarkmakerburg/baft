# 12 — Innovation methodology and idea evaluation

BAFT explicitly separates a safe baseline from research mechanisms. Innovation means a documented gap, a conceptual difference, and a falsifiable experiment—not ornamental complexity.

Before an important design decision, the project reviews known approaches, writes a Gap Statement, defines a distinct hypothesis where justified, fixes security/correctness/resource invariants before benchmarking, prototypes behind a clear boundary with rollback, and records only measured results in STATUS/TEST-RESULTS.

The 10x/100x rule is design pressure rather than a numeric claim. Minor tuning can still be useful, but it is not labeled primary innovation. No research mechanism may weaken TLS verification, authentication, bounded memory, Route authorization, rollback, or testability.

Claim levels are: idea, research hypothesis, supported result, and—only after serious prior-art/legal review—possible novelty or patentability.
