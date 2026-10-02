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

The preview lists identifiers and counts only: no token hash, pairing code or key. It reads **copies** of the state file and audit log, so it never writes, migrates or recovers anything in the live directory and is safe next to a running BCC. Exit code `0` = verified and not refused, `1` = not verified or would be refused, `2` = usage. The same logic is available inside the server as `Server.PreviewRestore`.

## What is not here

The restore itself (`Server.RestoreFromFile`) is a transactional, fault-injection-tested library operation (stage, verify, journal, commit, roll back on failure), but it is **not yet exposed** as a command or API: this PR adds the preview only. A restore command that is safe against a running BCC needs its own design and approval.
