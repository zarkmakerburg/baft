#!/usr/bin/env bash
# End-to-end release rehearsal with throwaway keys: build twice and compare
# (reproducibility), certify a release key with a root key, sign, verify,
# then check that tampering, an uncertified key and revocation are rejected.
set -euo pipefail

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
tool="$work/baft-release"
go build -o "$tool" ./cmd/baft-release

version=v0.0.0-dryrun
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

BAFT_RELEASE_SIGNING_KEY=$(cat "$work/keys/release.key") \
  "$tool" sign -dir "$work/a" -version "$version" -commit "$commit" \
  -cert "$work/cert.json" -root-pub "$work/keys/root.pub" -build-flag -trimpath
"$tool" verify -dir "$work/a" -root-pub "$work/keys/root.pub"
(cd "$work/a" && sha256sum --quiet -c SHA256SUMS)

expect_fail() {
  local what=$1; shift
  if "$@" >/dev/null 2>&1; then
    echo "dry_run: $what was accepted" >&2
    exit 1
  fi
  echo "rejected as expected: $what"
}

# Wrong pinned root.
"$tool" keygen -out "$work/keys/other" >/dev/null
expect_fail "wrong root key" "$tool" verify -dir "$work/a" -root-pub "$work/keys/other.pub"

# Revoked release key.
"$tool" revoke -root-key "$work/keys/root.key" \
  -key-id "$("$tool" keyid -pub "$work/keys/release.pub")" -out "$work/revocations.json" >/dev/null
expect_fail "revoked release key" "$tool" verify -dir "$work/a" -root-pub "$work/keys/root.pub" \
  -revocations "$work/revocations.json"

# Tampered artifact.
cp -r "$work/a" "$work/t"
printf 'x' >> "$work/t/baft-linux-amd64"
expect_fail "tampered artifact" "$tool" verify -dir "$work/t" -root-pub "$work/keys/root.pub"

# Signing with a key the root never certified.
cp -r "$work/b" "$work/u"
expect_fail "uncertified signing key" "$tool" sign -dir "$work/u" -version "$version" -commit "$commit" \
  -cert "$work/cert.json" -root-pub "$work/keys/root.pub" -key "$work/keys/other.key"

echo "release dry run passed"
