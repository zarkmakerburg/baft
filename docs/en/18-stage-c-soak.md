# 18 — Stage-C soak gate

Short correctness tests are not sufficient to expose every time-dependent race, leak, or starvation failure. Stage C therefore has a dedicated workflow that repeatedly executes the real H2+mTLS multi-Flow and slow-receiver integration paths.

The workflow runs both integration tests 25 times normally and 5 times under the race detector. It is single-flight per branch via `cancel-in-progress`.

This is a liveness/boundedness/race regression gate, not the Stage-E performance benchmark. Stage C is not declared complete until a relevant head passes this soak workflow.
