# 15D — ECRL design gate: prior art, threat model, formal invariants, and falsification

> **Status:** mandatory Stage-D design gate before further resume implementation  
> **Name:** Epoch-Fenced Correlated Resume Ledger (ECRL)  
> **Rule:** no new ECRL wire/session implementation proceeds until this document is the governing design contract.

## 1. Three-level claim status

| level | current status | meaning |
|---|---|---|
| **Research hypothesis** | **active** | ECRL is a falsifiable design hypothesis. |
| **Supported engineering result** | **not reached** | A prototype or unit test alone is insufficient. Real carrier replacement, zombie-carrier, lost-ACK, tombstone, and exact-byte gates must pass first. |
| **Patentability / legal novelty** | **unassessed** | This is an engineering prior-art review, not a patent search or legal opinion. |

### Explicit prior-art conclusion

**Epoch fencing itself is not an ECRL innovation.**  
**Resume and anti-replay themselves are not ECRL innovations.**

If ECRL has a defensible research distinction, it can only lie in the **atomic coupling of Carrier ownership with a per-Flow byte ledger, TWRL watermarks, replay frontier, FIN state, and tombstones**.

If that coupling does not preserve exact-once target delivery under fault injection, the ECRL hypothesis is rejected.

---

## 2. Mandatory prior-art review

| prior art | problem solved | core mechanism | overlap with ECRL | exact ECRL difference | novelty conclusion |
|---|---|---|---|---|---|
| **Raft term/epoch fencing** | prevent stale leaders from retaining authority after a newer term | monotonically increasing term; stale-term RPC rejection; at most one leader per term | ECRL also uses a monotonic generation and rejects an old Carrier after replacement commit | ECRL has **no quorum, leader election, consensus, or replicated log**. Its epoch fences ownership of one point-to-point Session Carrier. The ledger must additionally correlate per-Flow sender/receiver byte state to derive replay, FIN, and tombstone state. | fencing is prior art; only the coupling to a correlated byte-resume ledger is a possible distinction |
| **QUIC connection migration + Connection ID rotation** | preserve the same transport connection across address changes; rotate/retire connection IDs; suppress duplicate packets | Connection IDs, path validation, NEW/RETIRE_CONNECTION_ID, packet-number spaces | both systems must prevent an old path from corrupting current continuity | QUIC preserves **one transport connection** with QUIC-native state. ECRL runs above H2/TCP after the old Carrier can be dead, establishes a **new authenticated Carrier**, and reconstructs application-relay state including Flow offsets, replay, FIN, receive-ring state, and tombstones. | migration/retirement are prior art; application-layer reconstruction across independent Carriers is the possible distinction |
| **TLS 1.3 resumption / 0-RTT anti-replay** | resume cryptographic sessions and limit replay of early data | PSK/tickets, single-use tickets or ClientHello recording; application-specific 0-RTT safety | both deal with replay around re-establishment | TLS does not know BAFT Flow offsets, target-delivery watermarks, FIN state, or tombstones. ECRL runs **after** a replacement Carrier is authenticated and does not derive trust from the ledger. It protects application-byte continuity rather than replacing TLS anti-replay. | resumption/anti-replay are prior art; per-Flow byte-state reconciliation is the possible distinction |

### Raft boundary

Raft terms are logical clocks used to reject stale leadership. Raft also separately addresses duplicate client execution with client serial numbers, demonstrating that leader fencing alone is not the same thing as exactly-once application semantics.

For BAFT:

> **Epoch determines authority; the ledger determines byte continuity.**

If ECRL is only `current_epoch + 1` plus stale-Carrier rejection, it is not novel and it is incomplete.

### QUIC boundary

QUIC Connection IDs allow an existing connection to survive network-address changes. QUIC packet numbers provide duplicate suppression within that connection.

ECRL addresses a different failure boundary:

- the prior H2/TCP Carrier may be completely dead;
- the replacement Carrier has a new TLS/H2 transport context;
- the BAFT Session and its Flow state remain in the same process;
- replay buffers and receive rings need continuity;
- target-side application effects must not be repeated.

ECRL therefore must not recreate QUIC; it models **handoff between independent Carriers at the BAFT layer**.

### TLS 1.3 boundary

TLS 1.3 explicitly notes that TLS-layer 0-RTT anti-replay does not provide complete protection against multiple copies of application data. Application retry semantics still need application-layer protection.

For BAFT:

- mTLS remains the authentication source;
- ECRL must not bypass TLS verification;
- resume snapshots are not credentials;
- even if TLS resumption is enabled later, BAFT byte-ledger reconciliation remains a separate requirement.

---

## 3. ECRL threat model

### Assumptions

ECRL does not attempt to defend against a fully Byzantine authorized peer. Both Nodes are operator-controlled and TLS authentication is trusted, but network timing and failure behavior are adversarial.

The network can drop, delay, reorder, partition, restore, and race connections. An old authenticated Carrier can become writable again after being considered dead. Multiple reconnect attempts can race.

A Node can crash/restart, lose ACK/FIN_ACK frames before disconnect, or present buggy/stale state.

Cryptographic replay outside a TLS connection remains TLS's responsibility.

### What the existing Epoch already covers

The project glossary defines Epoch as:

> the Carrier generation of a Session for fencing.

That only answers:

> Which Carrier generation is current after a replacement is committed?

Epoch alone does **not** determine:

- the byte offset from which replay must begin;
- whether an ACK was lost after peer acceptance;
- how much accepted data is still undelivered to the target;
- whether a Flow was already closed and tombstoned;
- whether FIN/FIN_ACK state needs reconciliation;
- whether the peer restarted and lost in-memory state.

Those are ledger responsibilities.

### ECRL-specific failure/attack scenarios

**T1 — Zombie Carrier:** old epoch becomes active again after new-epoch commit. It must not mutate application state or deliver target bytes.

**T2 — Dual Candidate:** two replacement Carriers race for the same next epoch. At most one can commit and only the committed owner can carry application data.

**T3 — Lost ACK:** local sender knows `K`, peer accepted `A`, with `K < A <= S`. Replay must begin exactly at `A`, not at the stale local ACK frontier.

**T4 — Accepted but not delivered:** TWRL permits `D < A`. The interval `[D,A)` must come only from the retained receive ring; replay must start at `A`.

**T5 — Tombstone resurrection:** a closed Flow must not be reopened by a stale snapshot or zombie Carrier.

**T6 — Lost FIN / FIN_ACK:** terminal state must reconcile monotonically without tail loss, repeated side effects, or premature release.

**T7 — Peer restart:** a changed Boot ID means in-memory replay/ring/socket continuity is gone; resume must fail.

**T8 — Inconsistent authenticated snapshot:** claims such as `A>S` or `A<K` must produce STATE_MISMATCH and prevent commit.

---

## 4. Formal ECRL invariants

For the Session:

- `E` = current committed epoch;
- `Owner(E)` = the sole Carrier permitted to carry application frames.

For each Flow and direction:

- `S` = sender `tx_next`;
- `K` = sender-observed acceptance ACK;
- `F` = valid snapshot/tombstone floor;
- `A` = peer `rx_accepted`;
- `D` = peer `rx_delivered`;
- `C` = peer receive credit;
- `Ring` = accepted-but-not-delivered bytes;
- `Replay` = sender bytes required after handoff;
- `T(f)` = tombstone, if the Flow is terminal.

### Relationship of K to TWRL notation

#### Formal definition of K

For one Flow and one direction:

`K` = **the greatest cumulative ACK offset that the sender has actually received and recorded in its current in-memory snapshot for the same `session_id`, the same peer `boot_id`, and the same valid epoch.**

K is therefore:

- the sender's knowledge watermark about peer acceptance;
- derived from a valid cumulative `ACK(offset)`;
- not a target-delivery watermark;
- not a tombstone or terminal offset;
- not durable process-restart state;
- valid only while Session/Boot/Epoch continuity holds.

Because BAFT emits ACK only after contiguous bytes have been admitted into bounded receive state, K is a known lower bound for A:

```text
K <= A
```

ACK is emitted before target delivery, so K and D have no fixed ordering.

#### Unified K/D/A/S/C model

The fixed relations are:

```text
D <= A <= S <= C
K <= A
```

where:

- `D` = peer bytes delivered to the target;
- `A` = peer bytes accepted into bounded BAFT receive state;
- `S` = sender bytes already produced/sent into the BAFT stream (`tx_next`);
- `C` = peer absolute receive-credit limit;
- `K` = sender's last observed cumulative ACK.

K and D form a **partial order**, not a total order.

Single-axis representation:

```text
0 ------- [ K and D may appear in either order ] ------- A ------- S ------- C
            |                                |
            +-- valid case 1: K <= D         |
            +-- valid case 2: D <  K         |

Always:
    K <= A
    D <= A <= S <= C
```

Two valid linearizations are:

```text
case 1 — ACK lags target delivery:
0 -- K -- D -- A -- S -- C

case 2 — ACK leads target delivery:
0 -- D -- K -- A -- S -- C
```

Equality is also possible.

#### Is `K <= D <= A <= S <= C` always true?

**No.**

The invariant is:

```text
D <= A <= S <= C
K <= A
```

but `K <= D` is not guaranteed.

- With a slow target, peer acceptance and ACK can advance before delivery: `D < K <= A`.
- With a lost or delayed ACK, target delivery can advance while sender knowledge lags: `K < D <= A`.

Any implementation that treats `K<=D` as mandatory would reject valid TWRL states.

#### Re-evaluation of K_release and the [D,K) gap

The mandatory trace review found **no valid unchanged-Boot-ID trace in the current threat model where freeing sender replay through K causes data loss**.

When `D<K<=A`, the receiver owns `[D,A)` in its live in-memory ring, so `[D,K)` remains present after the sender releases its replay copy. With unchanged `boot_id`, the same Process/ring survives Carrier replacement. Losing that ring requires Process restart, which changes `boot_id` and causes current ECRL to reject resume.

Therefore the earlier claim that K_release closes a current in-memory `[D,K)` correctness gap is **withdrawn**.

The deterministic trace is `TestECRLKReleaseNotRequiredForCurrentInMemoryResume`.

### K_release is only a future durable-snapshot prerequisite

K_release is not part of the current ECRL wire contract. It is retained only for a future mode that may resume across receiver restart from durable state.

For that future mode:

```text
K_release <= D
S - K_release <= R_replay
Credit_eff = min(C, K_release + R_replay)
```

The deterministic slow-target model `TestECRLFutureDurableSlowTargetReplayBoundNoDeadlock` proves the reference model never exceeds `R_replay` and still makes progress.

PADL interaction in that future mode:

- DATA eligibility is first bounded by `next_data_end <= Credit_eff`;
- replay pressure becomes `S-K_release`;
- PADL selects only among eligible flows and cannot override the cap;
- delivery/control ACKs remain outside the PADL DATA queue, allowing target progress to reopen capacity and avoiding circular deadlock.

### F — valid snapshot/tombstone floor

For the current in-memory model:

```text
F <= D <= A <= S <= C
K <= A
```

No fixed order exists between F and K. In a future durable mode, `K_release<=D` is additionally required. For a fully delivered terminal tombstone, `F=D=A=S=final_offset`.

#### Consequence for ECRL

K must never substitute for D or A:

- K is what the sender **knows** the peer has accepted;
- A from a valid peer snapshot is the authoritative replay frontier;
- D is the exact-once target-delivery frontier;
- S is the end of sender-produced bytes;
- C is the receive-credit ceiling.

Replay therefore still begins at A, not at K or D.

### I0 — unique Carrier ownership

```text
AcceptApplicationFrame(c) => epoch(c) = E AND c = Owner(E)

|CommittedOwners(E)| = 1
```

A pre-commit candidate may exchange resume control only.

### I1 — correlated sender/receiver bounds

```text
F <= D <= A <= S <= C
K <= A
```

The first inequality prevents invented or rolled-back receive state; the second preserves TWRL.

### I2 — handoff conservation

At replacement commit:

```text
Ring   = [D, A)
Replay = [A, S)

Ring ∩ Replay = ∅
Ring ∪ Replay = [D, S)
```

This is the core ECRL invariant: the unfinished byte range is partitioned into two contiguous, non-overlapping sources.

### I3 — exact-once target prefix

```text
TargetDeliveredBefore = [0, D)

AfterResume:
  next_target_offset = D
  bytes < D are never delivered again
  [D, A) comes only from Ring
  [A, S) comes only from Replay/new DATA
```

Wire duplicates below `A` may be observed, but they must never be delivered to the target again.

### I4 — tombstone monotonicity

```text
T(f).epoch <= E AND TombstoneRetained(f)
  => NOT Reopen(f, E')
for every E' >= E during retention
```

### I5 — boot continuity

```text
ResumeCommit
  => peer.boot_id == expected_peer_boot_id
  AND session_id unchanged
```

### I6 — terminal monotonicity

```text
FIN_ACKED    => FIN_SENT
FIN_ACK_SENT => FIN_RECEIVED

terminal state may advance; it may never regress
```

### Combined safety statement

```text
Commit(E+1)
=>
UniqueOwner(E+1)
AND
for every active Flow/direction:
    K <= A
    D <= A <= S <= C
    Ring   = [D,A)
    Replay = [A,S)
AND
retained tombstone => no reopen
AND
same boot_id
```

If an implementation cannot preserve this statement, it does not qualify as ECRL core.

---

## 5. Falsification criteria

| id | injected fault | expected result | hypothesis rejection condition |
|---|---|---|---|
| **ECRL-F01** | zombie old Carrier sends DATA/ACK/WINDOW/FIN after new commit | no application-state or target-byte change | any stale frame changes current state |
| **ECRL-F02** | two candidates race for the same next epoch | one committed owner only | two effective owners or duplicate replay paths |
| **ECRL-F03** | ACK for `A` is lost while peer has already accepted `A` | replay starts exactly at `A` | replay frontier is below or above `A` |
| **ECRL-F04** | disconnect with `D<A` | `[D,A)` only from Ring and `[A,S)` only from replay | any gap, overlap, or target duplicate |
| **ECRL-F05** | stale resume snapshot attempts to revive a tombstoned Flow | no redial or reopen | any target redial/reopen/data delivery |
| **ECRL-F06** | FIN_ACK is lost across resume | terminal state reconciles without tail loss or repeated side effects | tail loss, duplicated terminal effect, or premature close |
| **ECRL-F07** | peer Boot ID changes | resume rejected | old Session continues |
| **ECRL-F08** | inconsistent `A>S` or `A<K` snapshot | STATE_MISMATCH; no commit | contradictory snapshot commits |
| **ECRL-F09** | randomized drop/reorder/duplicate/control races | invariants always hold | any invariant violation |
| **ECRL-F10** | random byte stream, repeated disconnect/resume at many offsets | exact target byte count and hash | **even one duplicate or missing target byte** |

### Final rejection rule

If any valid fault-injection execution causes **one duplicated or missing target byte**, or allows an old Carrier to mutate post-commit state, the current ECRL hypothesis is rejected.

Changing timeouts or buffers is not an acceptable way to relabel such a failure as success; the handoff invariant or architecture must be redesigned.

---

## 5A. One-to-one F01–F10 harness mapping

No F criterion is removed.

| criterion | deterministic test | exact coverage |
|---|---|---|
| F01 | `TestECRLF01ZombieCarrier` | stale Carrier cannot affect application state after commit |
| F02 | `TestECRLF02DualCandidate` | one effective owner for the next epoch |
| F03 | `TestECRLF03LostACK` | replay frontier is A, never K |
| F04 | `TestECRLF04AcceptedNotDeliveredPartition` + `TestECRLF04DltKReplayReleaseSafety` | no partition gap/overlap; [D,K) release safety |
| F05 | `TestECRLF05TombstoneResurrection` | OPEN on a retained tombstone is rejected |
| F06 | `TestECRLF06LostFIN` + `TestECRLF06LostFINACK` | Lost FIN and Lost FIN_ACK tested independently |
| F07 | `TestECRLF07PeerRestart` | Boot-ID change rejects resume |
| F08 | `TestECRLF08InconsistentSnapshot` | contradictory snapshots, including A>S, are rejected |
| F09 | `TestECRLF09DeterministicPropertySweep` | exhaustive small-space F/K/K_release/D/A/S/C invariant sweep |
| F10 | `TestECRLF10ExactByteStream` | exact byte count/hash; no duplicate or missing byte |

F06 retains one criterion because both cases test terminal monotonicity I6, but **no scenario is merged away**: Lost FIN and Lost FIN_ACK are separate tests.

## 5B. Mutation gate — mutants injected into the reference model

Mutations are activated inside the same test-only reference model through `ECRL_MUTANT`. The workflow then runs the **original F test** and requires a real `--- FAIL:` result before declaring the mutant killed.

| mutant | original F test expected to FAIL |
|---|---|
| `f01_accept_old_epoch` | F01 / `TestECRLF01ZombieCarrier` |
| `f02_accept_both_candidates` | F02 / `TestECRLF02DualCandidate` |
| `f03_replay_from_k` | F03 / `TestECRLF03LostACK` |
| `f04_overlap_ring_replay` | F04 / `TestECRLF04AcceptedNotDeliveredPartition` |
| `f05_accept_tombstone_open` | F05 / `TestECRLF05TombstoneResurrection` |
| `f06_lost_fin_never_closes` | F06 / `TestECRLF06LostFIN` |
| `f06_apply_fin_twice` | F06 / `TestECRLF06DuplicateFINIdempotence` |
| `f07_resume_after_boot_change` | F07 / `TestECRLF07PeerRestart` |
| `f08_accept_a_gt_s` | F08 / `TestECRLF08InconsistentSnapshot` |
| `f09_skip_k_le_a` | F09 / `TestECRLF09DeterministicPropertySweep` |
| `f10_replay_from_a_minus_1` | F10 / `TestECRLF10ExactByteStream` |
| `f10_replay_from_a_plus_1` | F10 / `TestECRLF10ExactByteStream` |

The previous release-to-K mutation is no longer a valid mutant for the **current in-memory threat model** after the [D,K) proof. It belongs only to a future durable-restart mode.

## 5C. Prior art for multi-stage acknowledgement/settlement

- **MQTT QoS 2:** separates receipt/ownership and completion through PUBREC/PUBREL/PUBCOMP and prevents duplicate onward delivery for the same in-flight identifier.
- **AMQP 1.0:** has unsettled delivery state, terminal outcomes, settlement, link recovery, and even a partial-message `received(section,offset)` resume state.
- **MPTCP DATA_ACK:** provides a cumulative acknowledgment in the connection-level data sequence space, above individual TCP subflow ACKs.
- **Kafka:** distinguishes producer acknowledgment/durability levels and supports idempotent producer writes.

Therefore a two-level ACK or K_release concept is not novel by itself. The only remaining BAFT research hypothesis is the composition of epoch-fenced Carrier handoff, TWRL D/A/C state, exact byte partitioning, FIN/tombstones, and bounded replay/PADL coupling. Supported-engineering-result status for the ECRL engine is still **not claimed**, and patentability remains unassessed.

Sources:
- https://docs.oasis-open.org/mqtt/mqtt/v5.0/mqtt-v5.0.html
- https://docs.oasis-open.org/amqp/core/v1.0/os/amqp-core-complete-v1.0-os.pdf
- https://www.rfc-editor.org/rfc/rfc8684
- https://kafka.apache.org/40/configuration/producer-configs/

## 6. Current novelty decision

### Not novel by itself

- monotonic epochs;
- stale-owner rejection;
- session resumption;
- connection migration;
- replay protection;
- sequence/offset deduplication;
- tombstones as a general concept.

### The current research hypothesis

The potentially distinctive composition is:

1. committed Carrier ownership by epoch;
2. correlation between sender `K/S` and peer `D/A/C`;
3. non-overlapping handoff partition:
   - `Ring=[D,A)`
   - `Replay=[A,S)`
4. tombstone monotonicity in the same resume decision;
5. FIN/half-close correlation;
6. Boot-ID continuity;
7. all of the above at an application relay above a newly authenticated H2/TCP Carrier, without custom cryptography.

This is **not proof of novelty or patentability**. It is a precise research hypothesis worthy of deeper prior-art search and falsification.

---

## 7. Primary sources

- Diego Ongaro, John Ousterhout — *In Search of an Understandable Consensus Algorithm*: https://raft.github.io/raft.pdf
- RFC 9000 — *QUIC: A UDP-Based Multiplexed and Secure Transport*: https://www.rfc-editor.org/rfc/rfc9000
- RFC 8446 — *The Transport Layer Security (TLS) Protocol Version 1.3*: https://www.rfc-editor.org/rfc/rfc8446

## 8. Implementation gate

After this document, implementation may proceed only in this order:

1. formal/property tests for I0–I6;
2. ECRL-F01 through F09;
3. real replacement-Carrier integration;
4. ECRL-F10 exact-byte-stream;
5. only then may claim status move from **Research hypothesis** to **Supported engineering result**.

