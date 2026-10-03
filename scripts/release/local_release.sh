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

# Test hooks (upgrade/rollback e2e): BAFT_LOCAL_RELEASE_ROOT=<dir> reuses (or
# creates) one root key and revocation list across several releases;
# BAFT_LOCAL_RELEASE_BREAK=version|run signs a release whose baft binary does
# not run at all, or runs `version` but dies as a service.
out=${1:?usage: local_release.sh <out-dir> [version]}
version=${2:-v0.0.1-local}
mkdir -p "$out"
out=$(cd "$out" && pwd)
keys="$out/keys"
tool="$out/baft-release"

go build -o "$tool" ./cmd/baft-release
scripts/release/build.sh "$version" "$out/dist" >/dev/null
if [[ -n "${BAFT_LOCAL_RELEASE_ROOT:-}" && -f "$BAFT_LOCAL_RELEASE_ROOT/root.key" ]]; then
  mkdir -p "$keys"
  cp "$BAFT_LOCAL_RELEASE_ROOT/root.key" "$BAFT_LOCAL_RELEASE_ROOT/root.pub" "$keys/"
else
  "$tool" keygen -out "$keys/root" >/dev/null
  if [[ -n "${BAFT_LOCAL_RELEASE_ROOT:-}" ]]; then
    mkdir -p "$BAFT_LOCAL_RELEASE_ROOT"
    cp "$keys/root.key" "$keys/root.pub" "$BAFT_LOCAL_RELEASE_ROOT/"
  fi
fi
"$tool" keygen -out "$keys/release" >/dev/null
"$tool" certify -root-key "$keys/root.key" -release-pub "$keys/release.pub" -valid-days 30 -out "$out/cert.json" >/dev/null
if [[ -n "${BAFT_LOCAL_RELEASE_ROOT:-}" && -f "$BAFT_LOCAL_RELEASE_ROOT/revocations.json" ]]; then
  cp "$BAFT_LOCAL_RELEASE_ROOT/revocations.json" "$out/revocations.json"
else
  "$tool" revoke -root-key "$keys/root.key" -valid-days 30 -out "$out/revocations.json" >/dev/null
  if [[ -n "${BAFT_LOCAL_RELEASE_ROOT:-}" ]]; then cp "$out/revocations.json" "$BAFT_LOCAL_RELEASE_ROOT/"; fi
fi
case "${BAFT_LOCAL_RELEASE_BREAK:-}" in
  version) for f in "$out"/dist/baft-linux-*; do printf '#!/bin/sh\nexit 1\n' >"$f"; chmod 0755 "$f"; done ;;
  run) for f in "$out"/dist/baft-linux-*; do printf '#!/bin/sh\n[ "$1" = version ] && { echo broken; exit 0; }\nexit 1\n' >"$f"; chmod 0755 "$f"; done ;;
esac
BAFT_RELEASE_SIGNING_KEY=$(cat "$keys/release.key") \
  "$tool" sign -dir "$out/dist" -version "$version" -commit "$(git rev-parse HEAD)" \
  -cert "$out/cert.json" -root-pub "$keys/root.pub" >/dev/null
"$tool" verify -dir "$out/dist" -root-pub "$keys/root.pub" -revocations "$out/revocations.json"
cp "$keys/root.pub" "$out/root.pub"
rm -rf "$keys" "$tool" "$out/cert.json"
