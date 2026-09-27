# BAFT/1 protocol notes

> فارسی: [baft1.md](baft1.md)  
> Full English guide: [../en/04-protocol-baft1.md](../en/04-protocol-baft1.md)

The wire header is exactly 24 bytes, big-endian: `frame_len u32`, `type u8`, `flags u8`, `reserved u16`, `stream_id u64`, and `offset u64`. Maximum payload is 65536 bytes and maximum total frame size is 65560 bytes. Baseline DATA generation uses 32768-byte chunks.

Executable vectors are stored in [golden-vectors.json](golden-vectors.json). The full guide documents Session/Flow state and distinguishes implemented behavior from Stage-D reserved recovery frames.
