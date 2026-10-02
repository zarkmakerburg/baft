#!/usr/bin/env bash
# Release acceptance: ties the evidence of one release to one commit.
#
#   scripts/release/accept.sh <tag> [owner/repo]
#
# Needs: git (with the tag fetched), gh (authenticated), go, python3.
# Passes only when ALL hold for the exact commit the tag points at:
#   1. the tag is an ancestor-or-equal of origin/main (canonical branch);
#   2. every required CI check on that commit concluded success;
#   3. the signed manifest of the published release names that same commit;
#   4. the published assets verify against the pinned root key and the
#      revocation list committed at that commit (signature, hashes, no downgrade
#      state is used: this is an audit, not an install);
#   5. the offline bundle, when the release has one, is byte-identical to a
#      rebuild from the release assets.
# CI passing on a PR head is not acceptance: this checks the tag's own SHA.
set -uo pipefail

tag=${1:?usage: accept.sh <tag> [owner/repo]}
repo=${2:-zarkmakerburg/baft}
# r3 is path-filtered (no run on commits that do not touch its paths) and
# e2e-install-offline exists only for releases that carry an offline bundle;
# both are required only when they ran / when the bundle exists.
required=(test release-dry-run e2e-binaries e2e-install e2e-install-release e2e-agent-enroll e2e-ssh-bootstrap e2e-launch1)

fails=0
row() { printf '%-6s %-28s %s\n' "$1" "$2" "$3"; [[ "$1" == FAIL ]] && fails=$((fails + 1)); }
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

git fetch -q --no-tags origin main "refs/tags/$tag:refs/tags/$tag" 2>/dev/null
sha=$(git rev-parse --verify -q "refs/tags/$tag^{commit}") || { row FAIL "tag" "$tag does not exist"; exit 1; }
row INFO "tag" "$tag -> $sha"

if git merge-base --is-ancestor "$sha" origin/main; then row PASS "on main" "commit is on origin/main"; else row FAIL "on main" "commit is NOT on origin/main"; fi

mkdir -p "$work/rel"
# REST only (works where GraphQL is blocked): list the assets, then fetch each.
assets=$(gh api "repos/$repo/releases/tags/$tag" --jq '.assets[]|[.id,.name]|@tsv' 2>/dev/null)
if [[ -z "$assets" ]]; then
  row FAIL "release assets" "no published release $tag (draft or missing)"
else
  while IFS=$'\t' read -r id name; do
    gh api -H 'Accept: application/octet-stream' "repos/$repo/releases/assets/$id" > "$work/rel/$name" 2>/dev/null || row FAIL "asset $name" "download failed"
  done <<<"$assets"
fi
bundle=$(ls "$work"/rel/baft-offline-*.tar.gz 2>/dev/null | head -n1)

checks=$(gh api "repos/$repo/commits/$sha/check-runs?per_page=100" --jq '.check_runs[]|[.name,.status,.conclusion]|@tsv' 2>/dev/null)
[[ -n "$bundle" ]] && required+=(e2e-install-offline)
if grep -qP '^r3\t' <<<"$checks"; then required+=(r3); else row INFO "ci: r3" "no run on this commit (path-filtered)"; fi
for c in "${required[@]}"; do
  line=$(grep -P "^\Q$c\E\t" <<<"$checks" | head -n1)
  if [[ "$line" == *$'\tcompleted\tsuccess' ]]; then row PASS "ci: $c" "success on $sha"; else row FAIL "ci: $c" "${line:-no run on this commit}"; fi
done

if [[ -z "$assets" ]]; then
  echo; echo "REJECTED: $fails check(s) failed"; exit 1
fi
mkdir -p "$work/dist"
for f in "$work"/rel/*; do [[ "$f" == "$bundle" || "$f" == "$bundle.sha256" ]] || cp "$f" "$work/dist/"; done

mcommit=$(python3 -c 'import base64,json,sys;e=json.load(open(sys.argv[1]));print(json.loads(base64.b64decode(e["payload"]))["commit"])' "$work/dist/manifest.json" 2>/dev/null)
if [[ "$mcommit" == "$sha" ]]; then row PASS "manifest commit" "signed manifest names $sha"; else row FAIL "manifest commit" "manifest says '${mcommit:-?}', tag is $sha"; fi

git show "$sha:release/keys/root.pub" > "$work/root.pub" 2>/dev/null
git show "$sha:release/keys/revocations.json" > "$work/revocations.json" 2>/dev/null
if out=$(go run ./cmd/baft-release verify -dir "$work/dist" -root-pub "$work/root.pub" -revocations "$work/revocations.json" 2>&1); then
  row PASS "signature + hashes" "verified against the root key committed at $sha"
else
  row FAIL "signature + hashes" "$(tail -n1 <<<"$out")"
fi

if [[ -n "$bundle" ]]; then
  git show "$sha:install.sh" > "$work/install.sh"
  if scripts/release/offline_bundle.sh "$work/dist" "$work/revocations.json" "$work/install.sh" "$work/rebuilt" >/dev/null 2>&1 &&
     cmp -s "$bundle" "$work/rebuilt/$(basename "$bundle")"; then
    row PASS "offline bundle" "byte-identical to a rebuild from the assets"
  else
    row FAIL "offline bundle" "differs from a rebuild (or revocations.json in the release repo changed since)"
  fi
else
  row INFO "offline bundle" "this release has none"
fi

echo
if [[ $fails -eq 0 ]]; then echo "ACCEPTED: $tag @ $sha"; else echo "REJECTED: $fails check(s) failed"; exit 1; fi
