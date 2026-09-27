# Stage C foundation

The standalone Stage-C resource primitives are present:
- one global allocator with separate receive/replay pools (128 MiB each by default, 256 MiB total);
- 16 MiB default per-flow receive/replay caps;
- no borrowing between receive and replay pools;
- bounded control queue (256 messages or 1 MiB, whichever fills first);
- byte-based Deficit Round Robin primitive with caller-supplied per-flow quantum.

These primitives are unit-tested but are not yet wired into the Session data path, so Stage C is not marked complete.
