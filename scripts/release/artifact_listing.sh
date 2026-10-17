#!/usr/bin/env bash
# Print one SHA-256 over the sorted "<sha256> <size> <name>" lines of every
# file in DIR. Two builds of the same release print the same value exactly
# when every artifact is byte-identical.
#
#   scripts/release/artifact_listing.sh <dir>
set -euo pipefail
dir=${1:?usage: artifact_listing.sh <dir>}
cd "$dir"
for f in *; do
  [[ -f "$f" && ! -L "$f" ]] || { echo "artifact_listing: $f is not a regular file" >&2; exit 1; }
  printf '%s %s %s\n' "$(sha256sum "$f" | cut -d' ' -f1)" "$(wc -c <"$f" | tr -d ' ')" "$f"
done | LC_ALL=C sort | sha256sum | cut -d' ' -f1
