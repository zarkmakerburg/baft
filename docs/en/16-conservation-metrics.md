# 16 — Privacy-bounded conservation metrics

BAFT deliberately uses the standard Prometheus text exposition format instead of inventing another metrics wire format. Innovation is applied to metric semantics: the endpoint exposes aggregate conservation relationships rather than peer identities, Route names, target addresses, or per-stream labels.

Core gauges include active Flows, receive/replay/total reserved bytes, accepted backlog (A-D), credit exposure (C-D), replay outstanding (txNext-txAcked), and current TWRL invariant violations.

The metrics listener is constrained to loopback by configuration validation. Aggregate metrics trade local diagnostic precision for privacy and bounded cardinality; detailed per-Flow diagnostics belong in the future Unix-socket admin/doctor path.
