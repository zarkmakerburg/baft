# 19 — Bounded terminal quarantine

Repeated Stage-C soak exposed intermittent multi-Flow unexpected EOF failures. The lifecycle race occurs when final target delivery overlaps FIN completion: one path can close the Flow while the target-delivery pump is still attempting to refresh receive credit.

The fix applies two invariants:

1. late credit intent after a Flow becomes terminal is absorbed and does not generate RESET;
2. actually closed Flow IDs are retained in a bounded 256-entry terminal quarantine. Only late ACK/WINDOW/FIN_ACK/RESET for those known IDs are absorbed; a truly unknown stream ID remains a protocol error.

This deliberately follows conservative closed/draining semantics rather than inventing novel terminal behavior in a safety-critical state transition. Stage D will later integrate retention, epochs, and tombstones into exact resume semantics.
