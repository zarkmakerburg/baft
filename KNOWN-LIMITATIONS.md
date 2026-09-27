# KNOWN LIMITATIONS

- The tree is research software and is not production-ready.
- Source is pinned to Go 1.27.1, but current sandbox verification is a compatibility smoke run on Go 1.23.2 only.
- YAML examples and JSON Schema exist; production YAML decoding is not yet wired because the intended pinned YAML dependency cannot be fetched in this sandbox.
- Stage-B flow control currently uses a per-flow sliding window without the Stage-C global allocator/reservation budget.
- Active certificate revocation on an already-established carrier is not implemented yet.
- RESET semantics and complete standardized error mapping are not complete.
- Resume, replay buffer, epoch fencing and tombstones are not implemented.
- No relay, endpoint pool, H3 or Cloudflare Worker path is implemented yet.
- No full benchmark, fuzz campaign or 1GiB correctness run has been completed.
- No performance, stealth, censorship-resistance or novelty claim has been established.
- No Iran↔EX pilot has been run.
