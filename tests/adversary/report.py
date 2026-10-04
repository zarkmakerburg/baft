#!/usr/bin/env python3
"""Compose one consolidated resilience + intrusion report from a soak run.

Usage:
  report.py ADV_RESULTS INTR_RESULTS PROVENANCE [--title TITLE] > report.md

Inputs are produced by run.sh (ADV_RESULTS = results.jsonl, PROVENANCE =
provenance.txt) and intrude.sh (INTR_RESULTS = results.jsonl). Missing inputs
degrade gracefully so the report step never fails the job.
"""
import json
import sys
import datetime


def load(path):
    rows = []
    try:
        with open(path) as f:
            for line in f:
                line = line.strip()
                if line:
                    rows.append(json.loads(line))
    except FileNotFoundError:
        pass
    except Exception as e:  # pragma: no cover - defensive
        print(f"<!-- failed to read {path}: {e} -->")
    return rows


def prov(path):
    try:
        with open(path) as f:
            return f.read().strip()
    except FileNotFoundError:
        return ""


def field(text, key):
    for tok in text.replace("\n", " ").split():
        if tok.startswith(key + "="):
            return tok[len(key) + 1:]
    return "?"


def main():
    args = [a for a in sys.argv[1:] if not a.startswith("--")]
    title = "BAFT Adversarial Resilience & Intrusion Report"
    if "--title" in sys.argv:
        title = sys.argv[sys.argv.index("--title") + 1]
    adv_path = args[0] if len(args) > 0 else "out/results.jsonl"
    intr_path = args[1] if len(args) > 1 else "intr/results.jsonl"
    prov_path = args[2] if len(args) > 2 else "out/provenance.txt"

    adv = load(adv_path)
    intr = load(intr_path)
    p = prov(prov_path)

    adv_fail = [r for r in adv if not r.get("pass", False)]
    intr_fail = [r for r in intr if not r.get("boundary_held", False)]
    ran_any = bool(adv or intr)
    verdict = "PASS" if ran_any and not adv_fail and not intr_fail else (
        "FAIL" if (adv_fail or intr_fail) else "NO DATA")
    badge = {"PASS": "✅ PASS", "FAIL": "❌ FAIL", "NO DATA": "⚠️ NO DATA"}[verdict]

    now = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%d %H:%M UTC")
    sha = field(p, "sha")
    profile = field(p, "profile")

    out = []
    w = out.append
    w(f"# {title}")
    w("")
    w(f"**Verdict: {badge}** · generated {now} · commit `{sha[:12]}` · profile `{profile}`")
    w("")
    w("This report is produced automatically by the periodic resilience tester "
      "(`tests/adversary/`). It launches a real BAFT EX/IR pair on loopback and, "
      "acting as a censor / DoS attacker and as an on-path intruder, tries to "
      "break or penetrate it with real traffic. All scenarios are loopback only "
      "and never target any third-party host.")
    w("")

    w("## 1. Destruction / DoS resilience")
    w("")
    if adv:
        w("| scenario | result | heal | EX rss MB | IR rss MB | EX fd | IR fd | note |")
        w("|---|---|---|---|---|---|---|---|")
        for r in adv:
            w("| {s} | {ok} | {h} | {er} | {ir} | {ef} | {irf} | {n} |".format(
                s=r.get("scenario", "?"),
                ok="✅" if r.get("pass") else "❌",
                h=r.get("heal", "?"),
                er=r.get("ex_rss_mb", "?"), ir=r.get("ir_rss_mb", "?"),
                ef=r.get("ex_fd", "?"), irf=r.get("ir_fd", "?"),
                n=r.get("reason", "") or "—"))
    else:
        w("_No destruction results recorded._")
    w("")

    w("## 2. Intrusion / penetration attempts")
    w("")
    if intr:
        w("| intrusion | boundary held | detail |")
        w("|---|---|---|")
        for r in intr:
            w("| {s} | {ok} | {d} |".format(
                s=r.get("scenario", "?"),
                ok="✅" if r.get("boundary_held") else "❌ BYPASS",
                d=r.get("detail", "") or "—"))
    else:
        w("_No intrusion results recorded._")
    w("")

    w("## 3. Findings")
    w("")
    if not ran_any:
        w("- ⚠️ The tester produced no data this run (see the job log).")
    elif not adv_fail and not intr_fail:
        w("- No findings: every destruction scenario survived within the "
          "resource bounds and every intrusion attempt was contained.")
    else:
        for r in adv_fail:
            w(f"- ❌ **{r.get('scenario')}**: {r.get('reason') or 'failed'}")
        for r in intr_fail:
            w(f"- ❌ **{r.get('scenario')} (intrusion bypass)**: {r.get('detail') or 'boundary not held'}")
    w("")

    w("## 4. Environment")
    w("")
    w("```")
    w(p if p else "(no provenance recorded)")
    w("```")
    w("")
    w("---")
    w("")
    w("Zero-day fuzzing of attacker-reachable parsers runs separately in the "
      "`fuzz` workflow; any crasher it finds is uploaded there and seeded back "
      "into the corpus as a regression.")
    w("")

    sys.stdout.write("\n".join(out))
    # Exit nonzero on a real failure so a caller can gate on it if desired,
    # while the workflow invokes this in an always() step.
    return 1 if (adv_fail or intr_fail) else 0


if __name__ == "__main__":
    sys.exit(main())
