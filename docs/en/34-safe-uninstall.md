# 34 — Safe uninstall

HQ A3. Uninstall is a BAFT lifecycle feature: `baft uninstall` and menu item 13. It is **local only**: there is no remote or fleet uninstall (HOLD), and the agent has no way to run it.

```
INSPECT -> BUILD REMOVAL PLAN -> SHOW EXACT CHANGES -> EXPLICIT CONFIRMATION
        -> BACKUP IF REQUIRED -> STOP SERVICES -> REMOVE -> VERIFY
```

## Commands

```
baft uninstall --preview            # the exact plan; changes nothing
baft uninstall --preview --json     # the same, for automation
baft uninstall                      # standard uninstall: BAFT services + agent, their binaries; KEEP DATA
baft uninstall --binaries           # BAFT binaries only
baft uninstall --agent              # the agent only
baft uninstall --bcc                # the BCC only
baft uninstall --services           # BAFT services + binaries
baft uninstall --full               # everything above
baft uninstall --resume | --restore # finish, or undo, an interrupted uninstall
```

On a terminal the command shows the plan and asks; in scripts it needs `--yes`, plus `--stop-active-tunnels` when a running tunnel would stop. Path flags (`--unit-dir`, `--bin-dir`, `--prefix`, `--config-dir`, `--state-dir`, `--agent-dir`, `--agent-state-dir`, `--bcc-state-file`, `--journal-dir`, `--backup-dir`) default to the installer's paths.

| Exit | Meaning |
|---|---|
| 0 | done and verified, a preview, or nothing to remove |
| 1 | failed; everything it changed was restored (the journal says what happened) |
| 2 | usage |
| 3 | blocked (ownership conflict, a tunnel change in progress, BCC recovering); nothing changed |
| 4 | confirmation needed or not given; nothing changed |
| 5 | an earlier uninstall did not finish: `--resume` or `--restore` first |

## Ownership law

Only artifacts whose BAFT ownership is **proven** are eligible. Proof is one of:

| Artifact | Proof |
|---|---|
| transport unit made by `install.sh` | byte-identical to what the installer writes for the parameters the unit names (a test runs the installer's own shell function and compares) |
| tunnel unit built by BCC | `# baft-managed: true` / `# baft-tunnel:` header and a `baft.managed.json` marker beside its config that names the same tunnel and matches the unit and config byte for byte |
| agent unit | byte-identical to the installer's agent unit |
| BCC unit | labelled `# baft-managed: true` and `# baft-component: bcc` (BAFT has no BCC installer; the operator labels a unit they want BAFT to own) |
| binary | its embedded Go build information (read from the file, never executed) names BAFT's module and the expected program |
| files | a name BAFT writes, at BAFT's own location, in BAFT's format (config that loads, Noise key, PEM files, marker, release state, pairing leftovers, the tunnel builder's records, the installer's rerun backups, BCC's state database, access file, job key, audit log, backups) |

| Classification | Uninstall |
|---|---|
| `BAFT_MANAGED` | eligible for removal |
| `DRIFTED` (BAFT wrote it, edited since) | held for manual review: not stopped, not removed |
| `DISCOVERED_UNMANAGED` | never touched |
| `OWNERSHIP_CONFLICT` | HOLD / manual review, and it **blocks the run** (exit 3) |
| `UNKNOWN` (unreadable, symlink, unexpected file) | never touched |

Anything still **used by something that stays** is kept: a binary a remaining unit runs (every unit file in the unit directories is scanned for the programs it runs), the config, keys and revocation list of a remaining tunnel, the release state of a remaining agent. A `baft*` unit that cannot be read keeps every BAFT binary. Directories are removed only when they end up empty; operator files (BCC's TLS certificate and key, admin token, install script) are never removed.

## Data: KEEP by default

| Class | Flag | What |
|---|---|---|
| BCC state | `--delete-bcc-state` | state database (and its JSON migration copy), access file, job-signing key |
| certificates | `--delete-certificates` | outer PKI, Noise keys, the IR's pinned EX CA, agent token and pinned keys, release trust state |
| backups | `--delete-backups` | installer rerun backups, config copies, the tunnel builder's PKI copies, BCC's encrypted backups |
| tunnel configs | `--delete-tunnel-configs` | configs, ownership markers, pairing leftovers, the tunnel builder's change records |
| audit history | `--delete-audit` | BCC audit log and anchor outbox, the agent's record of executed jobs |

Each class is chosen separately. With `--full` on a terminal each is asked separately and defaults to NO. Emergency backups are never removed by an uninstall.

## Active tunnels

A running tunnel is never stopped silently: the plan lists it with its impact (EX: stops accepting on its listen address; IR: local clients lose the tunnel), and the run needs explicit consent (`--stop-active-tunnels`, or a separate yes on a terminal). Its configuration is preserved unless tunnel configs are chosen.

## BCC safety

Before BCC state is deleted, BCC is stopped and an **emergency backup** is taken while holding BCC's own state lock (so BCC is provably not running and state and audit are one snapshot): every BCC file is copied into `/var/backups/baft/bcc-emergency-<time>/` (0700, files 0600), each copy is verified byte for byte, a `manifest.json` records sources and digests, and `baft-bcc verify-backup` opens a private copy of the database with BCC's own reader and verifies the audit hash chain. The location is shown. Only then is anything removed; if the backup fails, BCC is started again and nothing is removed. The owner can skip it only explicitly: `--no-backup`, or typing `NO BACKUP` on a terminal. To restore: stop BCC, copy each file back to the source path in the manifest (mode 0600), start BCC.

## Transactional, recoverable

Nothing is deleted directly. Each file is first checked against the digest the plan saw, then moved into a journaled quarantine (`/var/lib/baft-uninstall/run-<time>/`); services are stopped and disabled with their previous state recorded; binaries go last. Verification then proves that every removed path is gone, every kept artifact is byte-identical and every unit that stays and was running still runs. Only then is the quarantine purged (the commit) and BAFT directories that became empty removed. A failure before the commit restores everything (files back, units enabled and started as they were). If the process is killed, the next run refuses until `baft uninstall --restore` undoes it or `baft uninstall --resume` finishes it (if the binary was already moved, run the copy in the run's quarantine). The journal holds paths, digests and states only, never file contents, and stays as the record of what was removed.

## Reinstall

A standard uninstall keeps config, keys and release state, so `install.sh --yes` afterwards brings back **the same node**: the config validates with the release binary, pairing is skipped and the tunnel comes back without re-pairing. Because the release state is kept, an older release is still refused after uninstall and reinstall (anti-rollback holds). After a full uninstall with every class deleted, a reinstall is a fresh install and pairs anew. The service user is kept.

## Tests

- `internal/uninstall`: ownership (installer template match and one-byte mismatch, BCC marker consistent / drifted / every conflict form, labelled and unlabelled BCC, Go build information, symlinks), scopes, data defaults, in-use rules, active tunnels, plan-to-apply changes, failure injection at every step (files and service state restored), crash at every step then restore or resume, BCC backup (BCC stopped, lock held refused, unverified backup restored, `--no-backup`, pending BCC journal), parity of the unit templates with `install.sh`. Mutation-checked.
- `tests/uninstall`: a real BCC state goes through the uninstall's emergency backup and BCC's verifier; the restored files open in BCC; a tampered copy is refused.
- `cmd/baft`: the command (usage, preview, consent, conflict, interactive confirmations, pending run and restore) and menu item 13.
- CI `e2e-uninstall` (systemd, signed releases): install EX + IR, a labelled BCC, an agent and two operator units; conflict blocks; no consent refused; killed run restored, killed run resumed; user data preserved and reinstall without re-pairing; upgrade, uninstall, older release refused, current reinstalled; BCC state deleted only after a verified emergency backup; full uninstall removes only confirmed data; verify clean; fresh reinstall; the operator's units survive throughout.

## Limits

All BAFT services on the host are in scope together; installer prefixes other than the default are given with `--prefix` (one per run). BCC's own directories are not removed. A tunnel built by BCC comes back through BCC after a reinstall (its kept config and marker show as MISSING until rebuilt). `BAFT_UNINSTALL_FAIL_AT` / `BAFT_UNINSTALL_CRASH_AT` are test hooks.
