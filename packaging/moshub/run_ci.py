#!/usr/bin/env python3
"""Run bounded MosHub checks and retain source-bound cloud results on failure."""
import json
import os
from pathlib import Path
import re
import subprocess
import time


def identity(branch, message, tree):
    match = re.fullmatch(r"mcpshka-snapshots/([0-9a-f]{40})", branch)
    sources = re.findall(r"^Source-Commit: ([0-9a-f]{40})$", message, re.M)
    trees = re.findall(r"^Source-Tree: ([0-9a-f]{40})$", message, re.M)
    if not match or sources != [match[1]] or trees != [tree]:
        raise ValueError("snapshot identity mismatch")
    return match[1]


def race_share(sites):
    """MosHub's LPT share of the race fan-out (packaging/ci/plan.json).

    GitVerse already ran the full go test gate and GitHub runs the other
    share, so MosHub repeats neither."""
    out = subprocess.run(["python3", "packaging/ci/shard.py", "--site", "moshub",
                          "--suite", "race", "--sites", sites],
                         capture_output=True, text=True, timeout=300, check=True)
    return out.stdout.split()


def run_checks(source_sha, tree, output, sites=None):
    started = time.monotonic()
    sites = sites or os.environ.get("SHARD_SITES") or "github,moshub"
    codes, pkgs = [], []
    try:
        pkgs = race_share(sites)
        if pkgs:
            codes.append(subprocess.run(["go", "test", "-race", "-count=1", "-p", "2", *pkgs],
                                        timeout=1500, check=False).returncode)
        else:
            codes.append(0)
    except subprocess.TimeoutExpired:
        codes.append(124)
    except (OSError, subprocess.CalledProcessError):
        codes.append(1)
    result = {"schema": "mcpshka.cloud-results/v1", "provider": "moshub",
              "source_sha": source_sha, "tree": tree,
              "state": "failed" if any(codes) else "passed",
              "planned_checks": 1, "observed_checks": len(codes),
              "suite": "race", "shard_sites": sites, "packages": pkgs,
              "exit_codes": codes, "seconds": time.monotonic() - started,
              "failed_tests": [],
              "trust": "UNTRUSTED", "deploy_authorized": False}
    (output / "summary.json").write_text(json.dumps(result, sort_keys=True) + "\n")
    return 1 if any(codes) else 0


def main():
    def git(*args):
        return subprocess.check_output(["git", *args], text=True, timeout=10).strip()
    tree = git("rev-parse", "HEAD^{tree}")
    sha = identity(os.environ.get("CI_COMMIT_BRANCH", ""), git("show", "-s", "--format=%B"), tree)
    if git("rev-list", "--parents", "-n", "1", "HEAD") != git("rev-parse", "HEAD"):
        raise ValueError("snapshot has parents")
    output = Path("moshub-results")
    output.mkdir(exist_ok=True)
    return run_checks(sha, tree, output)


if __name__ == "__main__":
    raise SystemExit(main())
