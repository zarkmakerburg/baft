#!/usr/bin/env bash
# Release acceptance: ties the evidence of one release to one commit.
#
#   scripts/release/accept.sh <tag> [owner/repo]
#
# Needs: git, gh (authenticated), go, python3.
# Passes only when ALL hold for the exact commit the repository's CURRENT tag
# points at:
#   1. the tag, fetched fresh into a private ref, resolves to a commit that is
#      an ancestor of (or equal to) the freshly fetched main;
#   2. every required CI job succeeded on that commit, as produced by the
#      intended GitHub Actions workflow file (see "CI evidence" below);
#   3. the signed manifest of the published release names that same commit;
#   4. the published assets verify against the root key and revocation list
#      committed at that commit;
#   5. the offline bundle, when the release has one, is byte-identical to a
#      rebuild from the release assets.
# CI passing on a PR head is not acceptance: this checks the tag's own SHA.
#
# Tag binding: the tag is fetched with a forced update into refs/accept/ and
# the SHA is resolved only from that ref. A stale local tag of the same name is
# never consulted, and a failed fetch is a failure, not a fallback.
#
# CI evidence: check-run names alone can be produced by any installed GitHub
# App, so they are not trusted. Evidence is the Actions *workflow runs* for the
# exact SHA, identified by workflow file path (a path an App cannot forge). For
# each required workflow file the run with the highest run_number decides (so a
# later re-run supersedes an earlier failure, and a later failed run supersedes
# an earlier success); within it the latest attempt of the required job must
# have concluded success.
#
# Test hooks: ACCEPT_GH (gh command, default gh), ACCEPT_REMOTE (git remote,
# default origin).
set -uo pipefail

tag=${1:?usage: accept.sh <tag> [owner/repo]}
repo=${2:-zarkmakerburg/baft}
GH=${ACCEPT_GH:-gh}
remote=${ACCEPT_REMOTE:-origin}
# workflow file : required jobs ('?' = only when that workflow ran at all,
# because it is path-filtered)
ci_jobs="test release-dry-run e2e-binaries e2e-install e2e-install-release e2e-agent-enroll e2e-ssh-bootstrap e2e-launch1"
ci_optional="r3"   # in .github/workflows/r2-noise.yml, path-filtered

fails=0
row() { printf '%-6s %-28s %s\n' "$1" "$2" "$3"; [[ "$1" == FAIL ]] && fails=$((fails + 1)); }
work=$(mktemp -d)
cleanup() {
  rm -rf "$work"
  git update-ref -d "refs/accept/tags/$tag" 2>/dev/null
  git update-ref -d "refs/accept/main" 2>/dev/null
}
trap cleanup EXIT

# 1. tag and main, fetched fresh into private refs (forced, checked).
git update-ref -d "refs/accept/tags/$tag" 2>/dev/null
git update-ref -d "refs/accept/main" 2>/dev/null
if ! git fetch -q --no-tags "$remote" "+refs/tags/$tag:refs/accept/tags/$tag" "+refs/heads/main:refs/accept/main" 2>"$work/fetch.err"; then
  row FAIL "tag" "cannot fetch $tag and main from $remote: $(tail -n1 "$work/fetch.err")"
  echo; echo "REJECTED: $fails check(s) failed"; exit 1
fi
sha=$(git rev-parse --verify -q "refs/accept/tags/$tag^{commit}") || { row FAIL "tag" "$tag does not exist on $remote"; exit 1; }
row INFO "tag" "$tag -> $sha"

if git merge-base --is-ancestor "$sha" refs/accept/main; then row PASS "on main" "commit is on $remote/main"; else row FAIL "on main" "commit is NOT on $remote/main"; fi

# 2. CI evidence from workflow runs of the intended workflow files.
python3 - "$GH" "$repo" "$sha" "$ci_jobs" "$ci_optional" >"$work/ci.out" 2>"$work/ci.err" <<'PY'
import json, subprocess, sys
gh, repo, sha, jobs, optional = sys.argv[1:6]
def api(path):
    r = subprocess.run([gh, "api", path], capture_output=True, text=True)
    if r.returncode != 0:
        raise RuntimeError(r.stderr.strip() or "gh api failed")
    return json.loads(r.stdout)
def runs_for(path):
    d = api(f"repos/{repo}/actions/runs?head_sha={sha}&per_page=100")
    rs = [r for r in d.get("workflow_runs", []) if r.get("path") == path and r.get("head_sha") == sha]
    return max(rs, key=lambda r: (r.get("run_number", 0), r.get("run_attempt", 0)), default=None)
def job_row(run, name, workflow):
    if run.get("status") != "completed":
        return ("FAIL", name, f"{workflow} run #{run['run_number']} is {run.get('status')}")
    js = api(f"repos/{repo}/actions/runs/{run['id']}/jobs?filter=latest&per_page=100").get("jobs", [])
    cand = [j for j in js if j.get("name") == name]
    if not cand:
        return ("FAIL", name, f"no job '{name}' in {workflow} run #{run['run_number']}")
    j = max(cand, key=lambda j: j.get("id", 0))
    if j.get("status") == "completed" and j.get("conclusion") == "success":
        return ("PASS", name, f"success in {workflow} run #{run['run_number']} on {sha}")
    return ("FAIL", name, f"{j.get('status')}/{j.get('conclusion')} in {workflow} run #{run['run_number']}")
try:
    run = runs_for(".github/workflows/ci.yml")
    for name in jobs.split():
        if run is None:
            print("FAIL", f"ci: {name}", "no ci.yml workflow run for this commit", sep="\t")
        else:
            s, n, d = job_row(run, name, "ci.yml")
            print(s, f"ci: {n}", d, sep="\t")
    orun = runs_for(".github/workflows/r2-noise.yml")
    for name in optional.split():
        if orun is None:
            print("INFO", f"ci: {name}", "r2-noise.yml did not run on this commit (path-filtered)", sep="\t")
        else:
            s, n, d = job_row(orun, name, "r2-noise.yml")
            print(s, f"ci: {n}", d, sep="\t")
except Exception as e:
    print("FAIL", "ci", f"cannot read workflow runs: {e}", sep="\t")
PY
while IFS=$'\t' read -r st name detail; do [[ -n "$st" ]] && row "$st" "$name" "$detail"; done <"$work/ci.out"

# Published release assets (REST only: GraphQL is not available everywhere).
mkdir -p "$work/rel"
assets=$("$GH" api "repos/$repo/releases/tags/$tag" 2>/dev/null | python3 -c 'import json,sys
for a in json.load(sys.stdin).get("assets",[]): print(a["id"], a["name"], sep="\t")' 2>/dev/null)
if [[ -z "$assets" ]]; then
  row FAIL "release assets" "no published release $tag (draft or missing)"
  echo; echo "REJECTED: $fails check(s) failed"; exit 1
fi
while IFS=$'\t' read -r id name; do
  "$GH" api -H 'Accept: application/octet-stream' "repos/$repo/releases/assets/$id" > "$work/rel/$name" 2>/dev/null || row FAIL "asset $name" "download failed"
done <<<"$assets"
bundle=$(ls "$work"/rel/baft-offline-*.tar.gz 2>/dev/null | head -n1)
if [[ -n "$bundle" ]]; then
  python3 - "$GH" "$repo" "$sha" >"$work/off.out" <<'PY'
import json, subprocess, sys
gh, repo, sha = sys.argv[1:4]
r = subprocess.run([gh, "api", f"repos/{repo}/actions/runs?head_sha={sha}&per_page=100"], capture_output=True, text=True)
rs = [x for x in json.loads(r.stdout or "{}").get("workflow_runs", []) if x.get("path") == ".github/workflows/ci.yml"]
run = max(rs, key=lambda x: (x["run_number"], x.get("run_attempt", 0)), default=None)
if not run:
    print("FAIL\tci: e2e-install-offline\tno ci.yml run"); sys.exit()
js = json.loads(subprocess.run([gh, "api", f"repos/{repo}/actions/runs/{run['id']}/jobs?filter=latest&per_page=100"], capture_output=True, text=True).stdout or "{}").get("jobs", [])
c = [j for j in js if j.get("name") == "e2e-install-offline"]
ok = c and c[-1].get("status") == "completed" and c[-1].get("conclusion") == "success"
print(("PASS" if ok else "FAIL") + "\tci: e2e-install-offline\t" + (f"success in ci.yml run #{run['run_number']}" if ok else "missing or not successful"))
PY
  while IFS=$'\t' read -r st name detail; do row "$st" "$name" "$detail"; done <"$work/off.out"
fi

mkdir -p "$work/dist"
# License and third-party notices are published next to the signed files but
# are not part of the signed release.
for f in "$work"/rel/*; do
  case "$(basename "$f")" in LICENSE|COPYRIGHT|THIRD_PARTY_LICENSES.md) continue ;; esac
  [[ "$f" == "$bundle" || "$f" == "$bundle.sha256" ]] || cp "$f" "$work/dist/"
done

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
  # Releases cut after notices were added carry them in the bundle.
  notices=()
  if git cat-file -e "$sha:THIRD_PARTY_LICENSES.md" 2>/dev/null; then
    mkdir -p "$work/notices"
    for f in LICENSE COPYRIGHT THIRD_PARTY_LICENSES.md; do git show "$sha:$f" > "$work/notices/$f"; done
    notices=("$work/notices")
  fi
  if scripts/release/offline_bundle.sh "$work/dist" "$work/revocations.json" "$work/install.sh" "$work/rebuilt" "${notices[@]}" >/dev/null 2>&1 &&
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
