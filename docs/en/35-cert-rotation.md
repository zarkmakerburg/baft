# 35 — Transactional certificate rotation

HQ A4 / Stage F. Rotates the outer TLS certificate of a tunnel BCC built: the EX's local CA and server certificate, and the IR's pinned trust in it. **A failed new certificate never costs the working old one**, and at no point is the IR unable to verify the certificate the EX serves. There is no single-step rotation.

```
PREPARE -> DISTRIBUTE -> VERIFY -> ACTIVATE -> CONFIRM [-> HOLD] -> RETIRE_OLD
```

| Step | Node | What happens | Proof BCC requires |
|---|---|---|---|
| PREPARE | EX | `baft-pair pki` generates a new CA + server certificate for the same name into `pki.next-<rotation>`; nothing live changes | the plan: new CA and leaf (public), digests that match |
| DISTRIBUTE | IR | the pinned CA file becomes **old + new**, the service restarts; on failure the old file is restored | IR trusts old and new; the EX's current (old) certificate still verifies |
| VERIFY | IR, EX | IR: the new leaf verifies against its live trust and was issued by the new CA, the old one is still served and accepted. EX: the staged set is intact, valid for the name, key matches; the live set is unchanged | evidence per node |
| ACTIVATE | EX | the whole `pki` directory is swapped with the staged one in one atomic step (`renameat2 RENAME_EXCHANGE`), the service restarts and must serve the new leaf; on any failure the old set is swapped back and the service restarted on it | EX serves exactly the new leaf |
| CONFIRM | IR | over the real path the EX serves the new leaf, the live trust accepts it, the tunnel route accepts connections | IR sees the new certificate |
| HOLD | — | optional `hold_seconds` before retiring (cancel still possible) | — |
| RETIRE_OLD | IR, then EX | IR trusts the new CA only (only while it sees the new leaf served; bundle restored on failure); the EX destroys the old key material | IR trusts exactly the new CA; EX retired at the new epoch |

The trust window is old + new on the IR from DISTRIBUTE until RETIRE; the EX serves exactly one certificate at every instant. Every state the files can be in after a crash or reboot is bootable and mutually trusted:

| IR trusts | EX serves | when |
|---|---|---|
| old | old | before DISTRIBUTE; after a full rollback |
| old + new | old | DISTRIBUTE .. ACTIVATE; during rollback |
| old + new | new | ACTIVATE .. RETIRE |
| new | new | after RETIRE |

The two unsafe pairs are excluded by order, and the node re-checks each rule itself: the EX activates only after the IR proved the new leaf verifies; the EX rolls back **before** the IR, and the IR refuses to drop the new CA unless it sees the EX serving a certificate the old trust accepts (or BCC states the activation job never left its queue and the EX is not seen serving the new one); the IR retires the old CA only while it sees the new leaf served, and once RETIRE started neither side rolls back.

## Transaction, epoch, idempotency, recovery

- **Transaction ID** `rot-<hex>`; **epoch** = the tunnel's certificate epoch + 1 (`cert_epoch` on the tunnel). A node refuses an epoch that is not newer than its own (`/var/lib/baft/rotations/epoch`).
- One change at a time: a rotation and a tunnel change exclude each other on the same nodes, both in BCC and on the node; a second rotation is refused while one runs or ended in `rollback_failed` / `retire_failed`.
- Every node step is **idempotent** and **resumes** an interrupted one (records in `/var/lib/baft/rotations/<id>/rot.json`); job IDs are single-use, so a retry is a new signed job for the same rotation.
- **Crash / restart recovery:** the node's files are always in one of the four states above; re-running the step or the rollback completes or undoes it. BCC's state is in SQLite (`cert_rotations`, migration 5) and resumes after a BCC restart.
- **Ownership:** only files BAFT installed for the live tunnel are touched. The live config must still be byte-identical to what the tunnel builder installed and must point at BAFT's certificate files; a live certificate set or CA file changed outside BAFT makes the step refuse and change nothing. Only Noise tunnels built by BCC rotate.
- **Rollback:** any failure, timeout (20 minutes per step) or cancel before RETIRE rolls back EX, then IR, and only nodes whose jobs left the queue. If the EX rollback fails, the IR is **not** rolled back (old + new accepts whatever the EX serves) and the rotation ends `rollback_failed`.
- **Bounded overlap:** `overlap_hours` (default 24, 1–168) from the start. ACTIVATE is refused after it; HOLD ends at it; a failing RETIRE step is retried up to 5 times within it and then ends `retire_failed` — the new certificate is in use and the old one is still trusted, never a lockout.
- **Secrets:** only public certificates and digests travel in jobs; new private keys never leave the EX and never reach BCC.

## API (admin, audited)

```
POST /api/tunnels/rotate-cert      {"tunnel_id": "...", "hold_seconds": 0, "overlap_hours": 24}   -> 202 rotation
GET  /api/cert-rotations[?id=rot-…]
POST /api/cert-rotations/cancel    {"id": "rot-…", "reason": "..."}   (refused once RETIRE started)
```

Audit: `cert.rotation.start`, `cert.rotation.cancel` (admin), `cert.rotation.activated`, `cert.rotation.complete`, `cert.rotation.rolled_back`, `cert.rotation.rollback_failed`, `cert.rotation.retire_failed` (BCC). The rotation keeps per-node evidence for every step (the node's JSON report and what, if anything, did not match). The dashboard has a "Rotate cert" button on active tunnels. A restore preview warns about rotations in progress in the backup and rotations completed since it.

Agent actions (signed, allowlisted, exact parameters): `tunnel_cert_prepare_ex`, `tunnel_cert_trust_ir`, `tunnel_cert_verify_ex`, `tunnel_cert_verify_ir`, `tunnel_cert_activate_ex`, `tunnel_cert_confirm_ir`, `tunnel_cert_retire_ir`, `tunnel_cert_retire_ex`, `tunnel_cert_rollback`.

## Files

| Path | What |
|---|---|
| `/etc/baft/pki` | the live set; swapped atomically at ACTIVATE |
| `/etc/baft/pki.next-<id>` | the staged new set (before ACTIVATE) |
| `/etc/baft/pki.prev-<id>` | the previous set, kept for rollback until RETIRE |
| `/var/lib/baft/tunnels/<tunnel>/peer-ca.pem` | the IR's pinned trust (old, old + new, new) |
| `/var/lib/baft/rotations/` | records, epoch, active pointer, the IR's backup of its previous trust |

`baft uninstall` recognises all of them by format (class `certificates`, kept by default) and keeps anything else found there.

## Tests

`internal/tunnelnode/rotate_test.go` runs the real `baft-pair` with a pretend systemd whose "service" loads the certificate files on restart and serves them over real TLS; after every step and every injected fault it asserts that the running IR accepts the running EX **and** that the files both would boot from agree. Fault points (HQ): after PREPARE, after DISTRIBUTE, after VERIFY, during ACTIVATE (before/after the swap, before the restart, a service that does not start), after the first node activated, before CONFIRM, after CONFIRM, before RETIRE — each followed by a reboot, resume or rollback. `internal/agent/rotation_flow_test.go` runs BCC and two real agents: the full rotation with evidence and audit, a disconnect (step timeout) at every step, a crash during ACTIVATE, a BCC restart mid-rotation, cancel and its EX-then-IR order, bounds and admin-only endpoints.
