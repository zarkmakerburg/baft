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

- Installing from releases and fetching the revocation list on servers (P1-B).
- Agent update verification (P1-D).
- Sigstore or GitHub artifact attestations. They can be added later on top of this; they are not the trust root.

## Formats

Every signed document is a [DSSE](https://github.com/secure-systems-lab/dsse) envelope. The signature covers the exact payload bytes, so verifiers never re-serialise JSON.

| File | Signed by | Content |
|---|---|---|
| `release-key.cert.json` | root | release key id and public key, `not_before`, `not_after` (at most 400 days) |
| `manifest.json` | release key | version, commit, `created_at`, signing key id, hash of `SHA256SUMS`, every artifact's name, OS, arch, size and SHA-256, provenance (builder, repository, ref, workflow, run id, Go version, build flags) |
| `SHA256SUMS` | covered by the manifest | `sha256sum -c` compatible |
| revocations (published separately) | root | revoked release key ids |

Key id = first 16 bytes of SHA-256 over the raw public key, in hex.

## Verification rules

`baft-release verify` accepts a release only if all of these hold:

1. The certificate is signed by the pinned root and is for purpose `baft-release`.
2. The release key is not in the root-signed revocation list, when one is given.
3. The manifest is signed by the certified release key, and `signing_key_id` matches.
4. The manifest's `created_at` lies inside the certificate's validity window.
5. `SHA256SUMS` matches its hash in the manifest and lists exactly the manifest's artifacts.
6. Every file in the directory is a signed artifact or release metadata; every signed artifact is present with the signed size and hash.

Releases stay installable after their certificate expires, because rule 4 checks signing time, not install time. A leaked release key is handled by revocation, not expiry.

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

1. Commit `root.pub` as `release/keys/root.pub` (via PR).
2. In GitHub, create the environment `release`, restrict it to tags `v*`, and add the secrets `BAFT_RELEASE_SIGNING_KEY` (contents of `release.key`) and `BAFT_RELEASE_KEY_CERT` (contents of `release-key.cert.json`).
3. Delete `release.key` from the offline machine once the secret is stored.

## Rotation and revocation

- Rotation: generate a new release key, certify it with the root, replace both secrets. Servers need no change.
- Revocation: `baft-release revoke -root-key root.key -key-id <id> [-in revocations.json] -out revocations.json`, publish the list, rotate. P1-B makes servers fetch it.
- Root compromise needs re-pinning every server; that is why the root stays offline.

## Tests

- `go test ./internal/release`: round trip, tampered artifact, extra or missing file, tampered `SHA256SUMS`, tampered manifest, uncertified signing key, wrong root, signing outside validity, revocation (and forged revocation lists), payload-type confusion, input validation.
- `scripts/release/dry_run.sh` (CI job `release-dry-run`): two builds are byte-identical, and the CLI signs, verifies and rejects a wrong root, a revoked key, a tampered artifact and an uncertified signing key.

## Exit criteria

- CI green including `release-dry-run`.
- Owner completes the setup above, pushes a `v*` tag, and the draft release verifies with `baft-release verify` on a separate machine.

## Rollback

Delete the draft release, and the tag if the tag ruleset allows it. The code change is additive; reverting the PR removes the workflow and tool without touching runtime code.
