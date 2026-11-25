#!/usr/bin/env python3
"""Split Go packages between CI sites so no two sites repeat the same work.

Longest-processing-time-first (LPT) greedy assignment by measured package
weight and site speed: deterministic for the same inputs, so every site
computes the same plan independently and runs only its own share.
"""
import argparse
import json
from pathlib import Path
import statistics
import subprocess
import sys

PLAN = Path(__file__).with_name("plan.json")


def load(path=PLAN):
    return json.loads(Path(path).read_text())


def sites_for(plan, suite, active=None):
    names = sorted(n for n, s in plan["sites"].items() if suite in s["suites"])
    if active is not None:
        names = [n for n in names if n in active]
    if not names:
        raise ValueError("no active site runs suite " + suite)
    return names


def assign(packages, weights, speeds):
    """Return {site: [packages]}; packages without a weight get the median."""
    known = [weights[p] for p in packages if p in weights]
    default = statistics.median(known) if known else 1.0
    load_of = {s: 0.0 for s in speeds}
    out = {s: [] for s in speeds}
    for pkg in sorted(set(packages), key=lambda p: (-weights.get(p, default), p)):
        site = min(speeds, key=lambda s: ((load_of[s] + weights.get(pkg, default)) / speeds[s], s))
        out[site].append(pkg)
        load_of[site] += weights.get(pkg, default)
    return {s: sorted(v) for s, v in out.items()}


def shard(plan, suite, site, packages, active=None):
    names = sites_for(plan, suite, active)
    if site not in names:
        return []
    speeds = {n: float(plan["sites"][n].get("speed", 1.0)) for n in names}
    return assign(packages, plan.get("weights", {}), speeds)[site]


def go_packages():
    out = subprocess.check_output(["go", "list", "./..."], text=True, timeout=300)
    return [line for line in out.split() if line]


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--site", required=True)
    ap.add_argument("--suite", default="race")
    ap.add_argument("--sites", default="", help="comma-separated active sites; empty = all planned")
    ap.add_argument("--plan", default=str(PLAN))
    ap.add_argument("--explain", action="store_true", help="print the whole plan with loads")
    a = ap.parse_args(argv)
    plan = load(a.plan)
    active = [s for s in a.sites.split(",") if s] or None
    pkgs = go_packages()
    if a.explain:
        names = sites_for(plan, a.suite, active)
        speeds = {n: float(plan["sites"][n].get("speed", 1.0)) for n in names}
        w = plan.get("weights", {})
        for s, v in assign(pkgs, w, speeds).items():
            print(f"{s}: {len(v)} pkgs, ~{sum(w.get(p, 1.0) for p in v):.0f}s base", file=sys.stderr)
    print(" ".join("./" + p.split("/", 1)[1] if "/" in p else "." for p in shard(plan, a.suite, a.site, pkgs, active)))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
