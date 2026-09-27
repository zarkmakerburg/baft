# ADR-0006: Bounded control scheduling

Status: accepted
Date: 2026-09-27

The BAFT Blueprint requires a per-Shard control queue capped at 256 messages or 1 MiB, control priority above DATA, and a rate cap that prevents DATA starvation.

Implementation:
- all outbound control frames use the bounded control queue once the Session sender is running;
- outbound DATA uses byte-based Deficit Round Robin;
- control is normally selected before DATA;
- when DATA is waiting, at most 32 consecutive control frames are emitted before one eligible DATA frame is forced.

The value 32 is an implementation policy, not a wire-protocol constant and not a value mandated by the Blueprint. It can be tuned only with tests/measurements; the 256-message and 1 MiB queue caps remain hard baseline limits.

Before the Session sender goroutine starts, direct control writes remain available only so isolated unit tests can exercise state handlers without constructing a full running Session. Production `Peer.Run` starts the sender before HELLO.
