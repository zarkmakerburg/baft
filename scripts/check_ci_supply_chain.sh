#!/usr/bin/env bash
set -euo pipefail

python3 - <<'PY'
from pathlib import Path
import re
import sys

workflows = sorted(list(Path(".github/workflows").glob("*.yml")) + list(Path(".github/workflows").glob("*.yaml")))
failures = []

uses_re = re.compile(r"\buses:\s*([^\s#]+)")
sha_re = re.compile(r"^[0-9a-f]{40}$")
for path in workflows:
    for lineno, line in enumerate(path.read_text().splitlines(), 1):
        m = uses_re.search(line)
        if not m:
            continue
        ref = m.group(1)
        if ref.startswith("./"):
            continue
        if "@" not in ref:
            failures.append(f"{path}:{lineno}: action reference has no immutable ref: {ref}")
            continue
        revision = ref.rsplit("@", 1)[1]
        if not sha_re.fullmatch(revision):
            failures.append(f"{path}:{lineno}: action reference is not pinned to a full commit SHA: {ref}")

for path in workflows:
    for lineno, line in enumerate(path.read_text().splitlines(), 1):
        if "@latest" in line:
            failures.append(f"{path}:{lineno}: mutable @latest tool reference: {line.strip()}")

release = Path(".github/workflows/release.yml").read_text()
if "go run ./cmd/baft-release" in release:
    failures.append("release.yml: release-tag source is still executed as the signer")
m = re.search(r"^\s*TRUSTED_SIGNER_COMMIT:\s*([0-9a-f]+)\s*$", release, re.M)
if not m or not sha_re.fullmatch(m.group(1)):
    failures.append("release.yml: TRUSTED_SIGNER_COMMIT is missing or not an immutable full SHA")
if "environment: release" not in release:
    failures.append("release.yml: protected release environment is not attached to signing")
if 'ref: ${{ env.TRUSTED_SIGNER_COMMIT }}' not in release:
    failures.append("release.yml: signing job does not checkout TRUSTED_SIGNER_COMMIT")
if '"$RUNNER_TEMP/baft-release" sign' not in release:
    failures.append("release.yml: signing step does not invoke the isolated trusted signer binary")

if failures:
    print("CI supply-chain policy violations:", file=sys.stderr)
    for failure in failures:
        print(" - " + failure, file=sys.stderr)
    sys.exit(1)
print(f"CI supply-chain policy: PASS ({len(workflows)} workflows)")
PY
