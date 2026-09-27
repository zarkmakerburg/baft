# 07 — Memory control, backpressure, and scheduling

Stage C exists to prevent a relay from turning slow receivers or many concurrent Flows into unbounded memory growth.

## Global allocator

The current design uses one data-memory allocator with separate receive and replay pools. The default example is 256 MiB total, split into 128 MiB receive and 128 MiB replay. Pools do not borrow from each other.

Per-Flow receive/replay caps are also enforced and must not exceed their corresponding pool.

## Receive reservation

WINDOW must be backed by real reserved receive capacity. The receiver reserves capacity before advertising a larger absolute `max_offset`.

## Replay reservation

Before reading new application bytes, the sender reserves replay capacity. Unacknowledged DATA remains accounted until ACK advancement releases fully acknowledged chunks.

## Backpressure

When credit or memory budget is exhausted, BAFT stops reading more application bytes rather than extending an unbounded queue. TCP backpressure therefore propagates toward the source.

## Byte-based DRR

Outbound DATA uses Deficit Round Robin measured in bytes. Each Flow accumulates quantum and can send an item when its deficit covers that item's byte size.

## Control versus DATA

ACK/WINDOW/FIN control traffic cannot be trapped behind bulk DATA. The current implementation uses a bounded control queue (256 messages or 1 MiB) with control priority plus a finite burst cap so DATA cannot be starved indefinitely. The current control-burst policy value is 32 and is an implementation tuning value, not a wire constant.

## Current Stage-C status

The allocator and scheduler are wired into the Session path and COR-01 still passes after this integration. A slow-receiver integration gate is still being stabilized; therefore Stage C is not declared complete.
