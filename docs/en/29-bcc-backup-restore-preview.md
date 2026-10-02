# 29 — BCC backups and restore preview

## Backups

BCC writes encrypted backups when `BAFT_BCC_BACKUP_KEY` (base64 of 32 bytes) is set: `--backup-dir` (default `./backups`), every `--backup-interval` (default 24 h), keeping 7 daily and 4 weekly files (`daily-<time>.baftbak`, `weekly-<time>.baftbak`, mode `0600`). A backup is AES-256-GCM over the whole BCC state plus the audit chain; the header (time, schema version, payload SHA-256, audit sequence and hash) is authenticated as additional data. The key comes only from the environment and is never written to a backup.

## Restore preview

```bash
export BAFT_BCC_BACKUP_KEY=...        # the same key
baft-bcc restore-preview --backup backups/daily-20261002T000000Z.baftbak --state-file bcc-state.json
baft-bcc restore-preview ... --json   # machine-readable
```

Read-only. It decrypts and verifies the backup (authentication, checksum, audit chain, anchor), then repeats the real restore's checks and its **anti-rollback merge** on copies and reports what a restore would leave:

- nodes, tunnels and jobs: counts now and after, and which identifiers would disappear, reappear or change;
- what the merge keeps from the live state instead of the backup: token rotations and revocations made after the backup, telemetry high-water marks (a restore cannot undo them);
- whether a real restore would be **refused** (the current audit log is invalid or does not extend the backup's anchor);
- warnings: nodes enrolled since the backup would disappear, tunnel records would disappear while their servers keep running, a tunnel in the backup was mid-change, a node present only in the backup would reappear, the backup is older than 7 days.

The preview lists identifiers and counts only: no token hash, pairing code or key. Exit code `0` = verified and not refused, `1` = not verified, would be refused, or could not be run, `2` = usage.

**BCC must be stopped.** State (SQLite) and audit log are two files that a running BCC changes at different moments, and another process cannot share BCC's in-process lock, so only a stopped BCC gives one coherent snapshot of both. This is enforced, not just documented: BCC holds an exclusive lock on `<state-file>.lock` for its whole life (the kernel drops it however BCC exits; a second BCC on the same state is refused), and `restore-preview` fails with "stop BCC before previewing from files" if it cannot take that lock. It also refuses while an interrupted write (`<state-file>-journal`) or restore (`.restore-journal.json`) is pending: start BCC once so it can recover, stop it, then preview. It reads into memory and a private temp directory and writes nothing in the live directory (apart from the empty `.lock` file BCC maintains).

Inside a running server the same logic is `Server.PreviewRestore`. It takes the same locks in the same order as the real restore (`backupMu`, then `mutationMu`) and verifies the current audit first, so state and audit come from one instant at which no mutation is in progress, the boundary at which `RestoreFromFile` decides. It is a point-in-time answer: BCC keeps changing afterwards. It is not exposed as a command or API yet.

## What is not here

The restore itself (`Server.RestoreFromFile`) is a transactional, fault-injection-tested library operation (stage, verify, journal, commit, roll back on failure), but it is **not yet exposed** as a command or API: this PR adds the preview only. A restore command that is safe against a running BCC needs its own design and approval.
