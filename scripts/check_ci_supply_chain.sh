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
for forbidden in (
    "BAFT_RELEASE_SIGNING_KEY",
    "BAFT_RELEASE_KEY_CERT",
    "secrets.",
    "environment: release",
    "contents: write",
    "gh release create",
    "go run ./cmd/baft-release",
):
    if forbidden in release:
        failures.append(f"release.yml: signing boundary violation: contains {forbidden!r}")
if "unsigned-release-candidate" not in release:
    failures.append("release.yml: unsigned candidate artifact is not enforced")
if "signing_boundary=external-offline" not in release:
    failures.append("release.yml: external/offline signing boundary marker is missing")
if "signed=false" not in release:
    failures.append("release.yml: candidate is not explicitly marked unsigned")

if failures:
    print("CI supply-chain policy violations:", file=sys.stderr)
    for failure in failures:
        print(" - " + failure, file=sys.stderr)
    sys.exit(1)
print(f"CI supply-chain policy: PASS ({len(workflows)} workflows); release secrets absent")
PY
