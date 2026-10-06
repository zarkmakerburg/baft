#!/usr/bin/env bash
# Pack a signed release into one file for servers with no internet access.
#
#   scripts/release/offline_bundle.sh <release-dist-dir> <revocations.json> <install.sh> <out-dir> [notices-dir]
#
# notices-dir, when given, must hold LICENSE, COPYRIGHT and
# THIRD_PARTY_LICENSES.md; they are copied to the top of the archive so the
# license and third-party notices ship with the binaries.
#
# Writes <out-dir>/baft-offline-<version>.tar.gz and a .sha256 next to it. The
# archive holds release/ (the signed release files, both architectures and the
# agent; verifiable on their own with baft-release verify), the revocation list
# and install.sh. Nothing in it is
# trusted because of the archive: `install.sh --offline <dir>` verifies the
# release against the root key pinned in install.sh exactly like an online
# install, so a tampered or repacked bundle is refused.
#
# The archive is deterministic (sorted, fixed owner and mtime) so anyone can
# rebuild it from the release assets and compare.
set -euo pipefail

dist=${1:?usage: offline_bundle.sh <dist-dir> <revocations.json> <install.sh> <out-dir>}
rev=${2:?revocations.json is required}
inst=${3:?install.sh is required}
out=${4:?out-dir is required}
notices=${5:-}

for f in manifest.json release-key.cert.json SHA256SUMS; do
  [[ -s "$dist/$f" ]] || { echo "offline_bundle: $dist/$f is missing; sign the release first" >&2; exit 1; }
done
[[ -s "$rev" && -s "$inst" ]] || { echo "offline_bundle: revocations.json or install.sh is missing" >&2; exit 1; }
for arch in amd64 arm64; do
  signed_inst="$dist/baft-install-linux-$arch"
  [[ -s "$signed_inst" ]] || { echo "offline_bundle: signed $signed_inst is missing" >&2; exit 1; }
done
(cd "$dist" && sha256sum --quiet -c SHA256SUMS) || { echo "offline_bundle: release files do not match SHA256SUMS" >&2; exit 1; }
for arch in amd64 arm64; do
  cmp -s -- "$inst" "$dist/baft-install-linux-$arch" || { echo "offline_bundle: top-level install.sh differs from the signed $arch installer" >&2; exit 1; }
done

# The name only; the signature is checked by install.sh, not here. The cmp
# above proves the launcher copied into the bundle is the signed installer
# generation listed in dist/SHA256SUMS for both supported architectures.
version=$(python3 -c 'import base64,json,sys;e=json.load(open(sys.argv[1]));print(json.loads(base64.b64decode(e["payload"]))["version"])' "$dist/manifest.json")
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || { echo "offline_bundle: unexpected version '$version'" >&2; exit 1; }

name="baft-offline-$version"
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
mkdir -p "$stage/$name/release" "$out"
cp "$dist"/* "$stage/$name/release/"
cp "$rev" "$stage/$name/revocations.json"
cp "$inst" "$stage/$name/install.sh"
if [[ -n "$notices" ]]; then
  for f in LICENSE COPYRIGHT THIRD_PARTY_LICENSES.md; do
    [[ -s "$notices/$f" ]] || { echo "offline_bundle: $notices/$f is missing" >&2; exit 1; }
    cp "$notices/$f" "$stage/$name/$f"
  done
fi
cat > "$stage/$name/README.txt" <<TXT
BAFT $version offline bundle

On a server with no internet access (Debian/Ubuntu with python3 and openssl):

  tar -xzf $name.tar.gz
  cd $name
  sudo bash install.sh --offline . --role ex --public-address HOST_OR_IP
  # or:  sudo bash install.sh --offline . --agent-only --bcc-url ... --node-id ...

The release is verified against the root key pinned in install.sh and the
bundled revocation list (which expires; use a fresh bundle after that).
Compare the archive with the .sha256 published next to it before use.
TXT

# Fixed modes, so the archive does not depend on the umask or on whether the
# inputs carry the executable bit (release assets downloaded from GitHub and
# files read with git show do not).
find "$stage" -type d -exec chmod 0755 {} +
find "$stage" -type f -exec chmod 0644 {} +
chmod 0755 "$stage/$name/install.sh" "$stage/$name"/release/baft-*

out=$(cd "$out" && pwd)
epoch=${SOURCE_DATE_EPOCH:-0}
(cd "$stage" && tar --sort=name --owner=0 --group=0 --numeric-owner --mtime="@$epoch" -cf - "$name") | gzip -n -9 > "$out/$name.tar.gz"
(cd "$out" && sha256sum "$name.tar.gz" > "$name.tar.gz.sha256")
echo "$out/$name.tar.gz"
