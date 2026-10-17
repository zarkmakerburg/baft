#!/usr/bin/env bash
# Sign and publish a BAFT release from the owner's phone (Termux) or Mac.
#
#   BAFT_RELEASE_KEY_FILE=/path/release.key BAFT_RELEASE_CERT=/path/release.cert.json \
#     bash owner_release.sh vX.Y.Z
#
# Needs git, go, gh (logged in), tar and base64. It never needs the release
# artifacts, never rebuilds them, and never sends the private key anywhere:
#   1. waits for release.yml on the tag, whose candidate must match an
#      independent rebuild on another runner byte for byte;
#   2. downloads only candidate-digests.json (a few KB);
#   3. builds the signer from the tagged source and signs those digests;
#   4. after a typed PUBLISH, starts release-publish.yml with the three signed
#      metadata files. That workflow verifies them against the pinned root key
#      and every candidate artifact before it creates the GitHub release.
# The tag itself is created beforehand, by the owner, on the commit approved
# for release.
set -euo pipefail
umask 077

version=${1:?usage: owner_release.sh vX.Y.Z}
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || { echo "invalid version: $version" >&2; exit 2; }
repo=${BAFT_REPO:-zarkmakerburg/BAFT}
key=${BAFT_RELEASE_KEY_FILE:?set BAFT_RELEASE_KEY_FILE to the release signing key}
cert=${BAFT_RELEASE_CERT:?set BAFT_RELEASE_CERT to the release key certificate}
for tool in git go gh tar base64; do
  command -v "$tool" >/dev/null || { echo "missing tool: $tool" >&2; exit 1; }
done
[[ -f "$key" && ! -L "$key" && -f "$cert" && ! -L "$cert" ]] || { echo "release key and certificate must be regular files" >&2; exit 1; }

say() { printf '\n== %s\n' "$*"; }
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

say "tag $version"
commit=$(gh api "repos/$repo/commits/$version" --jq .sha 2>/dev/null) || { echo "tag $version does not exist on GitHub; tag the approved main commit first" >&2; exit 1; }
[[ "$commit" =~ ^[0-9a-f]{40}$ ]] || { echo "could not resolve $version" >&2; exit 1; }
status=$(gh api "repos/$repo/compare/$commit...main" --jq .status)
[[ "$status" == ahead || "$status" == identical ]] || { echo "$version ($commit) is not on main" >&2; exit 1; }
echo "$version -> $commit"

say "waiting for release.yml (independent rebuild + candidate)"
run=
for _ in $(seq 1 60); do
  run=$(gh run list -R "$repo" --workflow release.yml --event push --limit 50 --json databaseId,headSha,headBranch \
    --jq "[.[] | select(.headSha==\"$commit\" and .headBranch==\"$version\")][0].databaseId // empty")
  [[ -n "$run" ]] && break
  sleep 10
done
[[ -n "$run" ]] || { echo "no release.yml run for $version" >&2; exit 1; }
gh run watch "$run" -R "$repo" --exit-status >/dev/null || { echo "release.yml run $run failed: gh run view $run -R $repo --log-failed" >&2; exit 1; }
gh run download "$run" -R "$repo" -n candidate-digests -D "$work/digests"
grep -Fxq "tag=$version" "$work/digests/CANDIDATE" && grep -Fxq "commit=$commit" "$work/digests/CANDIDATE" ||
  { echo "candidate is for another tag or commit" >&2; exit 1; }

say "building the signer from the tagged source"
git clone -q --depth 1 --branch "$version" "https://github.com/$repo.git" "$work/src"
[[ "$(git -C "$work/src" rev-parse HEAD)" == "$commit" ]] || { echo "clone is not at $commit" >&2; exit 1; }
go_version=$(awk '$1 == "go" { print $2; exit }' "$work/src/go.mod")
(cd "$work/src" && env GOFLAGS= GOWORK=off GOEXPERIMENT= GOTOOLCHAIN="go$go_version" go build -trimpath -o "$work/baft-release" ./cmd/baft-release)

say "signing"
sed -n 's/^ *"name": "\(.*\)",$/  \1/p' "$work/digests/candidate-digests.json"
printf '\nSign %s (%s) with %s? Type SIGN: ' "$version" "$commit" "$(basename "$cert")"
read -r answer
[[ "$answer" == SIGN ]] || { echo "not signed"; exit 0; }
mkdir "$work/signed"
"$work/baft-release" sign-digests -digests "$work/digests/candidate-digests.json" -out "$work/signed" \
  -version "$version" -commit "$commit" -cert "$cert" -key "$key" -root-pub "$work/src/release/keys/root.pub"
signature=$(tar -C "$work/signed" -czf - SHA256SUMS manifest.json release-key.cert.json | base64 | tr -d '\n')

printf '\nSigned. Type PUBLISH to publish %s on GitHub: ' "$version"
read -r answer
[[ "$answer" == PUBLISH ]] || { echo "not published"; exit 0; }
started=$(date -u +%Y-%m-%dT%H:%M:%SZ)
gh workflow run release-publish.yml -R "$repo" --ref main -f tag="$version" -f signature="$signature"
say "release-publish started"
pub=
for _ in $(seq 1 30); do
  sleep 5
  pub=$(gh run list -R "$repo" --workflow release-publish.yml --event workflow_dispatch --limit 10 --json databaseId,createdAt \
    --jq "[.[] | select(.createdAt >= \"$started\")][0].databaseId // empty")
  [[ -n "$pub" ]] && break
done
[[ -n "$pub" ]] || { echo "could not find the release-publish run; check the Actions tab" >&2; exit 1; }
gh run watch "$pub" -R "$repo" --exit-status >/dev/null || { echo "release-publish failed: gh run view $pub -R $repo --log-failed" >&2; exit 1; }
say "published: https://github.com/$repo/releases/tag/$version"
