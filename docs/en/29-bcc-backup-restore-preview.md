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

Inside a running server the same logic is `Server.PreviewRestore`. It takes the same locks in the same order as the real restore (`backupMu`, then `mutationMu`) and verifies the current audit first, so state and audit come from one instant at which no mutation is in progress, the boundary at which `RestoreFromFile` decides. It is a point-in-time answer: BCC keeps changing afterwards. A5 exposes this read-only operation to authenticated administrators without exposing arbitrary filesystem paths.

## A5 — Admin restore preview

When encrypted backups are configured, BCC binds the admin API to the same `--backup-dir` and backup key used by the scheduled backup loop:

```text
GET  /api/backups
POST /api/backups/create            {}
POST /api/backups/restore-preview   {"filename":"daily-...baftbak"}
```

All three endpoints require BCC admin authentication. The list contains only regular `.baftbak` files. Preview accepts a **filename only** from that directory; path traversal, subdirectories and symlinks are rejected. The backup key is copied into server memory, is never returned, and the API cannot select an arbitrary host path. Preview attempts are recorded in the audit log as `backup.restore.preview` with only the filename and non-secret result metadata.

The create endpoint writes a new encrypted, server-named 0600 backup file in the configured directory and returns inventory metadata only; it never returns the key or backup payload. It records backup.create in the audit log. The preview endpoint cannot restore or mutate BCC state.

## A6 — Local restore CLI

The real restore remains deliberately **offline and local to the BCC host**:

```bash
export BAFT_BCC_BACKUP_KEY=...
baft-bcc restore --backup backups/daily-20261002T000000Z.baftbak --state-file bcc-state.json
# preview only; exits 4 because explicit confirmation is still required

baft-bcc restore --backup backups/daily-20261002T000000Z.baftbak --state-file bcc-state.json --yes
# repeats all safety checks and commits the transactional restore
```

The command requires the BCC state-file lock, so a running BCC makes it fail. It first runs the same restore preview and refuses an unauthenticated/corrupt backup, an invalid current audit chain, an anchor that the current audit does not extend, a legacy state that still needs migration, or a pending write/restore journal. Without `--yes` it never mutates state. With `--yes`, `RestoreFiles` reacquires the process lock and repeats backup/audit/anchor checks before calling the already fault-injection-tested transactional restore.

Exit codes: `0` committed, `1` safety/verification/restore failure, `2` usage or key configuration error, `4` verified preview but confirmation not given. `--json` reports both the preview and whether a restore was committed.

## What is not here

There is intentionally **no remote restore API** and no live in-process admin restore action. A5 creates and inspects backups but cannot apply a restore over the admin API; A6 requires local host access, a stopped BCC, the backup key, and explicit `--yes`.
