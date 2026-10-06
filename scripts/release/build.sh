#!/usr/bin/env bash
# Build the release binaries reproducibly into OUT_DIR.
#
#   scripts/release/build.sh <version> <out-dir>
#
# Artifacts are named <binary>-linux-<arch>. Builds are static (CGO off),
# path-trimmed and carry no build ID or VCS stamp, so the same commit and Go
# toolchain produce byte-identical files.
set -euo pipefail

version=${1:?usage: build.sh <version> <out-dir>}
out=${2:?usage: build.sh <version> <out-dir>}

binaries=(baft baft-pair baft-bcc baft-agent)
arches=(amd64 arm64)

mkdir -p "$out"
if [ -n "$(ls -A "$out")" ]; then
  echo "build.sh: $out is not empty" >&2
  exit 1
fi

# The BCC SSH bootstrap executes the installer shipped with this exact release.
# Release artifacts deliberately retain the existing <binary>-linux-<arch>
# allowlist, so the architecture-independent script is signed twice under
# explicit architecture names rather than widening the signer to arbitrary
# filenames.
[[ -f install.sh ]] || { echo "build.sh: install.sh is missing" >&2; exit 1; }

for arch in "${arches[@]}"; do
  install -m 0755 install.sh "$out/baft-install-linux-$arch"
  for bin in "${binaries[@]}"; do
    ldflags="-s -w -buildid="
    if [ "$bin" = baft ]; then
      ldflags="$ldflags -X main.version=${version#v}"
    fi
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
      go build -trimpath -buildvcs=false -ldflags="$ldflags" \
      -o "$out/$bin-linux-$arch" "./cmd/$bin"
    echo "$out/$bin-linux-$arch"
  done
done
