# BAFT/1 protocol notes

Wire frame header is 24 bytes, big-endian:
- frame_len u32
- type u8
- flags u8 = 0
- reserved u16 = 0
- stream_id u64
- offset u64

Maximum total frame size: 65560 bytes. Maximum payload: 65536 bytes. Baseline DATA chunk target: 32768 bytes.

This document is a working implementation note; the Blueprint 1.4 remains normative.
