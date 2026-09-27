# KNOWN LIMITATIONS

- The tree remains research software; Stage B completion does not mean production readiness.
- Stage-C resource primitives exist, but the current Session data path is not yet fully wired to the global receive/replay allocator and DRR scheduler.
- Receive credit is still the Stage-B sliding-window behavior until Stage-C integration is complete.
- Resume, replay across carrier replacement, epoch fencing and tombstones are not implemented yet.
- No relay, endpoint pool, H3 or Cloudflare Worker path is implemented yet.
- Long fuzz/soak campaigns and the formal 60 s × 5 benchmark campaign have not been completed.
- COR-01 passed on GitHub-hosted local networking; it is a correctness result, not a public-network goodput claim.
- No performance, stealth, censorship-resistance or novelty claim has been established.
- No real Iran↔EX pilot has been run.
