#!/usr/bin/env bash
# End-to-end release rehearsal with throwaway keys: build twice and compare
# (reproducibility), certify a release key with a root key, sign, verify,
# then check that tampering, an uncertified key, revocation, a missing or
# replayed revocation list and a downgrade are rejected.
set -euo pipefail

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
tool="$work/baft-release"
go build -o "$tool" ./cmd/baft-release

version=v0.0.2-dryrun
commit=$(git rev-parse HEAD)

scripts/release/build.sh "$version" "$work/a" >/dev/null
scripts/release/build.sh "$version" "$work/b" >/dev/null
if ! diff <(cd "$work/a" && sha256sum *) <(cd "$work/b" && sha256sum *); then
  echo "dry_run: builds are not reproducible" >&2
  exit 1
fi
echo "reproducible: $(ls "$work/a" | wc -l) artifacts byte-identical across two builds"

"$tool" keygen -out "$work/keys/root" >/dev/null
"$tool" keygen -out "$work/keys/release" >/dev/null
"$tool" certify -root-key "$work/keys/root.key" -release-pub "$work/keys/release.pub" \
  -valid-days 30 -out "$work/cert.json"
"$tool" revoke -root-key "$work/keys/root.key" -valid-days 30 -out "$work/rev1.json" >/dev/null

BAFT_RELEASE_SIGNING_KEY=$(cat "$work/keys/release.key") \
  "$tool" sign -dir "$work/a" -version "$version" -commit "$commit" \
  -cert "$work/cert.json" -root-pub "$work/keys/root.pub" -build-flag -trimpath
state="$work/state/release-state.json"
"$tool" verify -dir "$work/a" -root-pub "$work/keys/root.pub" -revocations "$work/rev1.json" \
  -state "$state" -update-state
(cd "$work/a" && sha256sum --quiet -c SHA256SUMS)

# expect_fail <what> <expected error text> <command...>
expect_fail() {
  local what=$1 want=$2 out; shift 2
  if out=$("$@" 2>&1); then
    echo "dry_run: $what was accepted" >&2
    exit 1
  fi
  if ! grep -qF -- "$want" <<<"$out"; then
    echo "dry_run: $what failed for the wrong reason: $out" >&2
    exit 1
  fi
  echo "rejected as expected: $what"
}

# Wrong pinned root.
"$tool" keygen -out "$work/keys/other" >/dev/null
expect_fail "wrong root key" "signed by key" "$tool" verify -dir "$work/a" -root-pub "$work/keys/other.pub" -revocations "$work/rev1.json"

# No revocation list.
expect_fail "missing revocation list" "-revocations is required" "$tool" verify -dir "$work/a" -root-pub "$work/keys/root.pub"

# Downgrade: an older signed release is refused once v0.0.2 was accepted,
# unless the operator explicitly allows it.
cp -r "$work/b" "$work/d"
BAFT_RELEASE_SIGNING_KEY=$(cat "$work/keys/release.key") \
  "$tool" sign -dir "$work/d" -version v0.0.1-dryrun -commit "$commit" \
  -cert "$work/cert.json" -root-pub "$work/keys/root.pub" >/dev/null
expect_fail "downgrade" "downgrade refused" "$tool" verify -dir "$work/d" -root-pub "$work/keys/root.pub" \
  -revocations "$work/rev1.json" -state "$state"
"$tool" verify -dir "$work/d" -root-pub "$work/keys/root.pub" -revocations "$work/rev1.json" \
  -state "$state" -allow-downgrade >/dev/null
echo "accepted as expected: explicit -allow-downgrade"

# A newer list (sequence 2) is accepted and recorded; replaying list 1 then fails.
"$tool" revoke -root-key "$work/keys/root.key" -in "$work/rev1.json" -valid-days 30 -out "$work/rev2.json" >/dev/null
"$tool" verify -dir "$work/a" -root-pub "$work/keys/root.pub" -revocations "$work/rev2.json" \
  -state "$state" -update-state >/dev/null
expect_fail "replayed revocation list" "is older than 2 already accepted" "$tool" verify -dir "$work/a" -root-pub "$work/keys/root.pub" \
  -revocations "$work/rev1.json" -state "$state"

# Revoked release key.
"$tool" revoke -root-key "$work/keys/root.key" -in "$work/rev2.json" \
  -key-id "$("$tool" keyid -pub "$work/keys/release.pub")" -out "$work/rev3.json" >/dev/null
expect_fail "revoked release key" "is revoked" "$tool" verify -dir "$work/a" -root-pub "$work/keys/root.pub" \
  -revocations "$work/rev3.json"

# Tampered artifact.
cp -r "$work/a" "$work/t"
printf 'x' >> "$work/t/baft-linux-amd64"
expect_fail "tampered artifact" "does not match the signed manifest" "$tool" verify -dir "$work/t" -root-pub "$work/keys/root.pub" -revocations "$work/rev2.json"

# Signing with a key the root never certified.
cp -r "$work/b" "$work/u"
expect_fail "uncertified signing key" "certificate is for key" "$tool" sign -dir "$work/u" -version "$version" -commit "$commit" \
  -cert "$work/cert.json" -root-pub "$work/keys/root.pub" -key "$work/keys/other.key"

echo "release dry run passed"
