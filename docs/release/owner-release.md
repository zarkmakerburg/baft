# Owner release flow

From v0.3.5 on, a release takes one command on the owner's phone or Mac. The
release signing key stays offline, and GitHub never receives it.

## Steps

1. Tag the approved `main` commit, e.g. `v0.3.5`.
2. Wait for `release.yml` to finish. It runs two jobs:
   - `reproduce` rebuilds the tag on a different runner image (ubuntu-22.04).
   - `candidate` builds the tag again and fails unless every artifact matches
     the independent rebuild byte for byte.

   If both pass, the job uploads the unsigned candidate and
   `candidate-digests.json`. Both are kept for 14 days.
3. On the device holding the key, run:

   ```
   BAFT_RELEASE_KEY_FILE=/path/release-20261012.key \
   BAFT_RELEASE_CERT=/path/release-20261012.cert.json \
   bash scripts/release/owner_release.sh v0.3.5
   ```

   The script needs `git`, `go`, `gh` (logged in), `tar` and `base64`. It does
   the following:
   - waits for step 2;
   - downloads only `candidate-digests.json`, which is a few KB;
   - builds `baft-release` from the tagged source;
   - shows what it will sign, and signs after you type `SIGN`;
   - after you type `PUBLISH`, starts `release-publish.yml` with the three
     signed metadata files.
4. `release-publish.yml` checks the following, and only then creates the
   GitHub release with the 18 usual assets:
   - the tag is on `main` and is not released yet;
   - it takes the candidate from the successful `release.yml` run for that
     tag;
   - the signature verifies with `baft-release verify` against
     `release/keys/root.pub` and `release/keys/revocations.json` from the
     tagged source;
   - the signed manifest names this tag and commit, and every artifact matches
     it.

   It also builds the offline bundle.
5. HQ runs `scripts/release/accept.sh <tag>` on the published release.

## What changed and why

| Before (up to v0.3.4) | Now |
|---|---|
| Candidate kept 1 day | Kept 14 days |
| Owner rebuilt all 10 binaries on the device to check provenance | Two separate CI builds on different runner images must match; the owner builds only the small signer |
| Owner downloaded every artifact, signed, built the offline bundle and uploaded 18 assets | Owner signs a digest list of a few KB; CI verifies the signature and publishes |

**The trust boundary is the same.** Only the owner's offline key can produce
a manifest that installers accept, and every server checks it again on
install. CI can publish only a release that already carries a valid owner
signature.

**What the owner gives up.** The owner no longer rebuilds the binaries on
their own device. The provenance check now rests on two independent GitHub
builds. If that matters for a release, the older
`scripts/release/mobile_sign_termux.sh` still performs the full local rebuild.

`scripts/check_ci_supply_chain.sh` enforces the following:
- `release.yml` still has no signing key, no `contents: write`, and no
  signing step.
- `release-publish.yml` is manual-only, has no secrets, never signs, and runs
  verification before `gh release create`.
