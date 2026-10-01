# 04 — BAFT/1 protocol and state machine

BAFT/1 is a binary framing protocol used only inside an authenticated encrypted Carrier. TLS provides confidentiality and integrity; BAFT does not add a custom security checksum.

## Frame format

All binary integers are big-endian.

| Offset | Size | Field | Meaning |
|---:|---:|---|---|
| 0 | 4 | `frame_len` | total frame length including header |
| 4 | 1 | `type` | frame type |
| 5 | 1 | `flags` | must be zero in v1 |
| 6 | 2 | `reserved` | must be zero in v1 |
| 8 | 8 | `stream_id` | zero for Session control, non-zero for Flow |
| 16 | 8 | `offset` | type-specific byte/state offset |
| 24 | variable | `payload` | maximum 65536 bytes |

Header size is 24 bytes; total frame size is 24..65560. The parser validates bounds and type before payload allocation, detects offset overflow, and performs incremental reads without relying on TCP or HTTP/2 frame boundaries.

## Frame types

`HELLO`, `HELLO_ACK`, `OPEN`, `OPEN_OK`, `OPEN_ERR`, `DATA`, `ACK`, `WINDOW`, `FIN`, `FIN_ACK`, `RESET`, `RESUME_STATE`, `RESUME_DONE`, `READY`, `PING`, `PONG`, `GOAWAY`, `PROFILE_PROPOSE`, `PROFILE_ACCEPT`, `PROFILE_COMMIT`, and `PADDING` are reserved by BAFT/1.

Unknown types/flags, non-zero reserved bits, or invalid stream placement are protocol errors.

## Session startup

New Sessions use:

```text
HELLO → HELLO_ACK → two-sided READY → application frames
```

HELLO metadata never overrides the authenticated certificate identity.

## OPEN

OPEN carries a Route ID plus a random open nonce, never an arbitrary destination address. Duplicate OPEN with identical identity must be idempotent and must not redial the target.

## DATA / ACK / WINDOW

Each Flow direction has its own byte-offset space.

- DATA offset is the first byte position in that payload.
- ACK reports the next contiguous byte expected by the receiver.
- ACK means BAFT acceptance, not final target application processing.
- WINDOW is an absolute maximum acceptable offset, not a delta.
- duplicate or overlapping DATA is never written twice to the target.
- gaps and flow-control violations are rejected.

## FIN

FIN carries the final offset for one direction. The receiver verifies it matches accepted data, half-closes the destination socket when supported, and replies with FIN_ACK.

## RESET / fixed errors

Wire-visible errors use a fixed code vocabulary such as `AUTH_FAILED`, `FLOW_CONTROL_ERROR`, `ROUTE_DENIED`, `TARGET_UNREACHABLE`, `RESOURCE_EXHAUSTED`, `STALE_EPOCH`, and `PROTOCOL_ERROR`. Raw OS/file-path errors are not sent to the peer.

A Flow that ends before the peer's FIN was passed to the local socket (RESET in either direction, a failed OPEN, revocation, or the end of the Session without recovery) is closed abortively: the local socket gets `SO_LINGER=0`, so the application sees a connection reset instead of a clean EOF that would look like a complete stream. A Flow that ended through FIN keeps the graceful close.

## Resume frames

RESUME frame types are reserved by the protocol, but complete resume semantics belong to Stage D. Parsing a reserved type is not equivalent to implementing recovery.
