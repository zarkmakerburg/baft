# 23 — P1-A: signed release artifacts

Status: implementation proposed for owner approval. Launch-1 step P1-A from [22-launch-1-roadmap.md](22-launch-1-roadmap.md).

## Scope

- Reproducible builds of `baft`, `baft-pair` and `baft-bcc` for linux/amd64 and linux/arm64 (`scripts/release/build.sh`).
- A signed release: `SHA256SUMS`, a signed manifest (`manifest.json`) that includes build provenance, and the release key certificate (`release-key.cert.json`).
- Two-tier keys, as decided on 2026-10-01: an offline Ed25519 root key certifies a release signing key held by CI. Servers pin only the root public key.
- `baft-release` tool: `keygen`, `keyid`, `certify`, `revoke`, `sign`, `verify`.
- `release` workflow: on a `v*` tag it builds, signs, verifies against the pinned root and opens a draft GitHub release.
- `release-dry-run` CI job that rehearses the whole flow with throwaway keys on every push and PR.
- Branch protection: the rulesets in place since 2026-10-01 (PR required, `test` check required, no force-push or deletion on `main` and `release-v1-goldapp`; `v*` tags cannot be moved or deleted).

## Non-scope

- Installing from releases and fetching the revocation list on servers (P1-B, now in `install.sh`).
- Agent update verification (P1-D).
- Sigstore or GitHub artifact attestations. They can be added later on top of this; they are not the trust root.

## Formats

Every signed document is a [DSSE](https://github.com/secure-systems-lab/dsse) envelope. The signature covers the exact payload bytes, so verifiers never re-serialise JSON.

| File | Signed by | Content |
|---|---|---|
| `release-key.cert.json` | root | release key id and public key, `not_before`, `not_after` (at most 400 days) |
| `manifest.json` | release key | version, commit, `created_at`, signing key id, hash of `SHA256SUMS`, every artifact's name, OS, arch, size and SHA-256, provenance (builder, repository, ref, workflow, run id, Go version, build flags) |
| `SHA256SUMS` | covered by the manifest | `sha256sum -c` compatible |
| `release/keys/revocations.json` | root | `sequence`, `issued_at`, `expires_at` (at most 400 days), revoked release key ids |

Key id = first 16 bytes of SHA-256 over the raw public key, in hex.

## Verification rules

`baft-release verify` accepts a release only if all of these hold:

1. The certificate is signed by the pinned root and is for purpose `baft-release`.
2. A root-signed revocation list is given, has not expired, and its `sequence` is not lower than the one recorded in the trust state (when one is used). The release key is not in it.
3. The manifest is signed by the certified release key, and `signing_key_id` matches.
4. The manifest's `created_at` lies inside the certificate's validity window.
5. `SHA256SUMS` matches its hash in the manifest and lists exactly the manifest's artifacts.
6. Every file in the directory is a signed artifact or release metadata; every signed artifact is present with the signed size and hash.
7. With a trust state (`-state`): the release is not older than the accepted one (SemVer precedence) unless `-allow-downgrade` is given, and the accepted version is never accepted again from a different commit, even with `-allow-downgrade`.

Releases stay installable after their certificate expires, because rule 4 checks signing time, not install time. A leaked release key is handled by revocation, not expiry. That is why the revocation list is mandatory and expires: an attacker cannot strip it, and cannot replay an old list after its `expires_at`, or after a server has seen a newer `sequence`.

### Trust state (anti-rollback)

A server keeps a root-owned `/opt/baft/release-state.json` (`$BAFT_PREFIX/release-state.json`): the accepted version and commit and the highest revocation `sequence` seen. `install.sh` checks it before installing and updates it after the new binaries are in place (see [06-running-ir-ex.md](06-running-ir-ex.md)); `baft-release verify -state <file> [-update-state]` uses the same file format. A missing file means first install. The revocation sequence in the state never goes down, even after an explicit downgrade.

## Invariants

- No private key is ever written to the repository. `*.key` is git-ignored; CI receives the release key only as an environment secret.
- Nothing is published that did not verify against `release/keys/root.pub` in the same run.
- The release is created as a draft; the owner publishes it.
- A tag on a commit outside `main` / `release-v1-goldapp` does not produce a release.

## Owner setup (one time)

On an offline machine with the Go toolchain:

```
go build -o baft-release ./cmd/baft-release
./baft-release keygen -out root        # root.key stays offline (YubiKey-backed storage or encrypted USB), with a backup
./baft-release keygen -out release
./baft-release certify -root-key root.key -release-pub release.pub -valid-days 180 -out release-key.cert.json
```

Then:

Also sign the initial (empty) revocation list:

```
./baft-release revoke -root-key root.key -valid-days 180 -out revocations.json
```

1. Commit `root.pub` as `release/keys/root.pub` and `revocations.json` as `release/keys/revocations.json`, and put the same root key in `BAFT_PINNED_ROOT_PUB` in `install.sh` (one PR; `tests/docs` fails if the two differ).
2. In GitHub, create the environment `release`, restrict it to tags `v*`, and add the secrets `BAFT_RELEASE_SIGNING_KEY` (contents of `release.key`) and `BAFT_RELEASE_KEY_CERT` (contents of `release-key.cert.json`).
3. Delete `release.key` from the offline machine once the secret is stored.

## Offline installation bundle

For servers with no internet access each release also carries `baft-offline-<version>.tar.gz` (and `.sha256`). It contains `release/` (the signed release files for amd64 and arm64, including the agent), `revocations.json` and `install.sh`.

```bash
sha256sum -c baft-offline-v0.1.1.tar.gz.sha256
tar -xzf baft-offline-v0.1.1.tar.gz && cd baft-offline-v0.1.1
sudo bash install.sh --offline . --role ex --public-address HOST_OR_IP
sudo bash install.sh --offline . --agent-only --bcc-url ... --node-id ...
```

- `--offline` uses no network and no `apt`; `python3`, `openssl` and `sha256sum` must already be on the host (the installer stops and names the missing one).
- The archive itself is **not** a trust anchor. The installer verifies `release/` against the root key pinned in `install.sh` and the bundled revocation list, with the same downgrade and re-tag protection as an online install; a repacked or tampered bundle is refused before anything is installed. `release/` can also be checked on its own with `baft-release verify -dir release ...`.
- The revocation list expires; a bundle older than that is refused, so use a fresh one.
- As with `curl | bash`, trust in the `install.sh` you run comes from where you got it: compare it with the file at the release tag, or take the whole bundle from the official release.
- The archive is deterministic (sorted, fixed owner/mtime): `scripts/release/offline_bundle.sh` rebuilds the same bytes from the release assets, so anyone can compare. CI (`e2e-install-offline`) builds one, rejects a tampered copy and installs EX and IR from it with every download address pointed at a dead port.

## Rotation and revocation

- Rotation: generate a new release key, certify it with the root, replace both secrets. Servers need no change.
- Revocation: `baft-release revoke -root-key root.key -in release/keys/revocations.json -key-id <id> -out revocations.json`, commit it as `release/keys/revocations.json`, rotate. Always pass `-in` so the `sequence` grows; a fresh list restarts at 1 and servers that saw more refuse it.
- Refresh: before the list's `expires_at`, re-sign it unchanged with `revoke -in release/keys/revocations.json -out revocations.json` and commit it. An expired list stops releases and installs until it is refreshed.
- Root compromise needs re-pinning every server; that is why the root stays offline.

## Tests

- `go test ./internal/release`: round trip, tampered artifact, extra or missing file, tampered `SHA256SUMS`, tampered manifest, uncertified signing key, wrong root, signing outside validity, revocation (and forged revocation lists), missing, expired and replayed revocation lists, downgrade, re-tag of an accepted version, SemVer ordering, trust state round trip, payload-type confusion, input validation.
- `scripts/release/dry_run.sh` (CI job `release-dry-run`): two builds are byte-identical, and the CLI signs, verifies and rejects (for the expected reason) a wrong root, a missing revocation list, a downgrade, a replayed revocation list, a revoked key, a tampered artifact and an uncertified signing key; an explicit `-allow-downgrade` is accepted.

## Exit criteria

- CI green including `release-dry-run`.
- Owner completes the setup above, pushes a `v*` tag, and the draft release verifies with `baft-release verify` on a separate machine.

## Rollback

Delete the draft release, and the tag if the tag ruleset allows it. The code change is additive; reverting the PR removes the workflow and tool without touching runtime code.
