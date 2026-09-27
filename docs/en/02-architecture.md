# 02 — Architecture, roles, and data flow

## High-level view

```text
IR local TCP → Flow → Session/Shard → H2 + TLS 1.3 mTLS → Session/Shard → fixed EX Route → target TCP
```

The transport connection is initiated by IR in the baseline, but application traffic is bidirectional.

## Module boundaries

- **carrier:** authenticated byte stream, cancellation, transport metadata; no Route logic.
- **protocol:** bounded BAFT/1 encoding/decoding; no target dialing.
- **session:** handshake, frame state, Flow ownership, and future epoch/resume state.
- **routes:** pre-authorized Route lookup; never accepts arbitrary targets from wire frames.
- **identity:** TLS trust, identity extraction, authorization, and active revocation.
- **resources:** shared receive/replay reservations.
- **scheduler:** byte-based DATA scheduling without interpreting payload.
- **node:** turns validated configuration into real IR/EX runtime objects.

## Baseline Carrier

IR connects to EX over TCP, negotiates TLS 1.3 with mTLS and ALPN `h2`, then opens one long-lived full-duplex POST on `/baft/v1/carrier`. Each Shard owns an independent transport so four Shards can be demonstrated as four independent TCP connections.

Redirects, environment HTTP proxies, and HTTP/1 fallback are disabled.

## Session and Flow

A new Session orders `HELLO → HELLO_ACK → READY` before application frames. Each Flow is one bidirectional TCP connection with independent directional offsets, ACK/WINDOW state, and FIN/FIN_ACK half-close semantics.

## Writer ownership

Control and DATA use separate scheduling rules but ultimately serialize through a single Carrier writer. This prevents frame bytes from being interleaved by concurrent goroutines.

## Cancellation

Cancellation is part of the Carrier contract. Runtime peer revocation cancels already-established carriers instead of waiting for a reconnect.
