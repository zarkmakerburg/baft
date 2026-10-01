#!/usr/bin/env bash
# Build a signed release with throwaway keys, laid out the way install.sh
# downloads one. For tests only: the keys are generated here and discarded.
#
#   scripts/release/local_release.sh <out-dir> [version]
#
# Writes <out-dir>/dist (the release assets), <out-dir>/root.pub and
# <out-dir>/revocations.json. Install from it with:
#   BAFT_RELEASE_URL=file://<out-dir>/dist BAFT_ROOT_PUB=$(cat <out-dir>/root.pub) \
#   BAFT_REVOCATIONS_URL=file://<out-dir>/revocations.json sudo -E bash install.sh ...
set -euo pipefail

out=${1:?usage: local_release.sh <out-dir> [version]}
version=${2:-v0.0.1-local}
mkdir -p "$out"
out=$(cd "$out" && pwd)
keys="$out/keys"
tool="$out/baft-release"

go build -o "$tool" ./cmd/baft-release
scripts/release/build.sh "$version" "$out/dist" >/dev/null
"$tool" keygen -out "$keys/root" >/dev/null
"$tool" keygen -out "$keys/release" >/dev/null
"$tool" certify -root-key "$keys/root.key" -release-pub "$keys/release.pub" -valid-days 30 -out "$out/cert.json" >/dev/null
"$tool" revoke -root-key "$keys/root.key" -valid-days 30 -out "$out/revocations.json" >/dev/null
BAFT_RELEASE_SIGNING_KEY=$(cat "$keys/release.key") \
  "$tool" sign -dir "$out/dist" -version "$version" -commit "$(git rev-parse HEAD)" \
  -cert "$out/cert.json" -root-pub "$keys/root.pub" >/dev/null
"$tool" verify -dir "$out/dist" -root-pub "$keys/root.pub" -revocations "$out/revocations.json"
cp "$keys/root.pub" "$out/root.pub"
rm -rf "$keys" "$tool" "$out/cert.json"
