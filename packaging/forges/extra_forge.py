#!/usr/bin/env python3
"""Immutable snapshots and CI observation on SourceCraft, GitFlic and GitHub.

Both sites run the CI configuration stored in the commit itself
(.sourcecraft/ci.yaml, gitflic-ci.yaml), so a snapshot is the source commit
pushed to refs/heads/mcpshka-snapshots/<sha>. A snapshot ref is never moved:
publish refuses a remote ref that points elsewhere. Tokens are read from
their service-owned files and never appear in arguments or output.

SourceCraft runs carry no commit or ref, so its CI writes report.json as a
cube artifact (commit, tree, per-check result); status trusts a run only
through that report. GitFlic pipelines name commit and ref directly.

GitHub goes through here too, as the mcpshka service user with its own API
token over HTTPS: pushing from root over SSH left root-owned objects in the
service-owned repository (BUG-20261103-10). Its workflow runs name head_sha.
"""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import urllib.error
import urllib.request

REPO = Path(__file__).resolve().parents[2]
SHA_RE = re.compile(r"[0-9a-f]{40}\Z")
LIMIT = 4 << 20

SITES = {
    "sourcecraft": {
        "token": Path("/var/lib/mcpshka/sourcecraft/token"),
        "git": "https://git.sourcecraft.dev/lda6994/mcpshka-ci.git",
        "api": "https://api.sourcecraft.tech/repos/lda6994/mcpshka-ci",
    },
    "gitflic": {
        "token": Path("/var/lib/mcpshka/gitflic/token"),
        "git": "https://gitflic.ru/project/lda6994/mcpshka.git",
        "api": "https://api.gitflic.ru/project/lda6994/mcpshka",
    },
    "github": {
        "token": Path("/var/lib/mcpshka/github/api_token"),
        # Private CI repository: the public "mcpshka" gets only the open-core
        # export (opencore_push.py), never a snapshot of the canonical tree.
        "git": "https://github.com/dmitriy1ebedev/mcpshka-ci.git",
        "api": "https://api.github.com/repos/dmitriy1ebedev/mcpshka-ci",
        "user": "x-access-token",
    },
}
# Checks each site's report must prove; matches its CI configuration.
# GitHub: every workflow the root CI evidence reads (cievidence.RequiredChecks).
REQUIRED = {"sourcecraft": ["amd64", "race"], "gitflic": ["contracts", "fuzz", "vet"], "github": ["cloud.yml", "admin-recovery.yml", "workbench-isolation.yml"]}


def emit(site, **fields):
    out = {"schema": "mcpshka.forge-ci/v1", "provider": site, "trust": "UNTRUSTED", "deploy_authorized": False}
    out.update(fields)
    print(json.dumps(out, sort_keys=True))


def hold(site, code):
    emit(site, state="HOLD", code=code)
    return 2


def token(site):
    path = SITES[site]["token"]
    st = path.stat()
    if st.st_uid != os.geteuid() or st.st_mode & 0o077:
        raise PermissionError("token_permissions")
    value = path.read_text().strip()
    if not value or len(value) > 512 or any(c.isspace() for c in value):
        raise ValueError("token_invalid")
    return value


def auth_header(site, value):
    if site in ("sourcecraft", "github"):
        return "Bearer " + value
    return "token " + value


def git_auth_header(site, value):
    if site == "sourcecraft":
        return "Authorization: Bearer " + value
    import base64
    user = SITES[site].get("user", "lda6994")
    return "Authorization: Basic " + base64.b64encode((user + ":" + value).encode()).decode()


def git(*args, header=None):
    cmd = ["git", "-C", str(REPO), "-c", "credential.helper="]
    if header:
        cmd += ["-c", "http.extraHeader=" + header]
    cmd += list(args)
    env = {"PATH": "/usr/bin:/bin", "GIT_TERMINAL_PROMPT": "0", "HOME": "/nonexistent", "LANG": "C"}
    return subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, env=env, timeout=300)


def remote_head(site, ref, header):
    p = git("ls-remote", SITES[site]["git"], ref, header=header)
    if p.returncode != 0:
        raise RuntimeError("ls_remote_failed")
    line = p.stdout.decode().strip()
    return line.split()[0] if line else ""


def private_repo(site):
    """Snapshots and mirrors carry the canonical tree: GitHub must say the
    target repository is private before anything is pushed there."""
    if site != "github":
        return True
    info = api_get("github", "", token("github"))
    return isinstance(info, dict) and info.get("private") is True


def publish(site, commit):
    if git("cat-file", "-e", commit + "^{commit}").returncode != 0:
        return hold(site, "unknown_commit")
    if not private_repo(site):
        return hold(site, "repository_not_private")
    header = git_auth_header(site, token(site))
    ref = "refs/heads/mcpshka-snapshots/" + commit
    before = remote_head(site, ref, header)
    if before == commit:
        emit(site, state="PUBLISHED", published=True, remote_ref=ref, already=True, source_commit=commit)
        return 0
    if before:
        return hold(site, "snapshot_ref_moved")
    if git("push", SITES[site]["git"], commit + ":" + ref, header=header).returncode != 0:
        return hold(site, "push_failed")
    if remote_head(site, ref, header) != commit:
        return hold(site, "remote_unverified")
    emit(site, state="PUBLISHED", published=True, remote_ref=ref, source_commit=commit)
    return 0


def mirror(site, commit):
    """Fast-forward the site's main: SourceCraft fires triggers only from a
    configuration present on its default branch."""
    if not private_repo(site):
        return hold(site, "repository_not_private")
    header = git_auth_header(site, token(site))
    before = remote_head(site, "refs/heads/main", header)
    if before == commit:
        emit(site, state="MIRRORED", remote_before=before, commit=commit)
        return 0
    if before and git("merge-base", "--is-ancestor", before, commit).returncode != 0:
        return hold(site, "not_fast_forward")
    if git("push", SITES[site]["git"], commit + ":refs/heads/main", header=header).returncode != 0:
        return hold(site, "push_failed")
    emit(site, state="MIRRORED", remote_before=before, commit=commit)
    return 0


def api_get(site, path, value, raw=False):
    headers = {"Authorization": auth_header(site, value), "Accept": "application/json"}
    if site == "github":
        headers.update({"Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28", "User-Agent": "mcpshka-forge-ci"})
    url = SITES[site]["api"] + path if path == "" or path.startswith("/") else path
    req = urllib.request.Request(url, headers=headers)
    with urllib.request.urlopen(req, timeout=30) as r:
        body = r.read(LIMIT + 1)
    if len(body) > LIMIT:
        raise ValueError("response_too_large")
    return body if raw else json.loads(body)


def open_pr(commit):
    """GitHub runs the gating workflows only for a pull request into main, so
    MCPshka opens one from the snapshot branch (owner 03.11.2026). GitHub
    marks it merged once main reaches the commit."""
    value = token("github")
    head = "mcpshka-snapshots/" + commit
    owner = SITES["github"]["api"].rstrip("/").split("/")[-2]
    found = api_get("github", "/pulls?state=all&head=" + owner + ":" + head, value)
    if isinstance(found, list) and found:
        emit("github", state="PR_OPEN", number=found[0].get("number"), already=True, commit=commit)
        return 0
    body = json.dumps({"title": "MCPshka snapshot " + commit[:12], "head": head, "base": "main", "draft": False,
                       "body": "CI for integration_ship of " + commit + ". GitHub marks it merged when main reaches this commit."}).encode()
    headers = {"Authorization": auth_header("github", value), "Accept": "application/vnd.github+json",
               "X-GitHub-Api-Version": "2022-11-28", "User-Agent": "mcpshka-forge-ci", "Content-Type": "application/json"}
    req = urllib.request.Request(SITES["github"]["api"] + "/pulls", data=body, headers=headers, method="POST")
    with urllib.request.urlopen(req, timeout=30) as r:
        pr = json.loads(r.read(LIMIT + 1))
    emit("github", state="PR_OPEN", number=pr.get("number"), commit=commit)
    return 0


def verdict(report, commit, tree, site):
    if not isinstance(report, dict) or report.get("commit") != commit or report.get("tree") != tree:
        return None
    checks = report.get("checks")
    if not isinstance(checks, dict):
        return None
    return all(checks.get(name) == "success" for name in REQUIRED[site]), checks


def tree_of(commit):
    p = git("rev-parse", commit + "^{tree}")
    return p.stdout.decode().strip() if p.returncode == 0 else ""


def status_sourcecraft(commit, value):
    tree = tree_of(commit)
    runs = api_get("sourcecraft", "/cicd/runs?page_size=20", value).get("runs", [])
    pending = False
    for run in runs:
        state = run.get("status")
        for wf in run.get("workflows", []):
            for task in wf.get("tasks", []):
                for cube in task.get("cubes", []):
                    for art in cube.get("artifacts", []):
                        if not str(art.get("local_path", "")).endswith("report.json") or art.get("status") != "success":
                            continue
                        try:
                            report = json.loads(api_get("sourcecraft", art["download_url"], value, raw=True))
                        except (urllib.error.URLError, ValueError, KeyError):
                            continue
                        v = verdict(report, commit, tree, "sourcecraft")
                        if v is None:
                            continue
                        ok, checks = v
                        result = "observed_pass" if ok and state == "success" else "observed_fail"
                        emit("sourcecraft", state=result, run=run.get("slug"), run_status=state, checks=checks, commit=commit, tree=tree)
                        return 0
        if state in ("created", "prepared", "processing", "awaiting_approval"):
            pending = True
    emit("sourcecraft", state="HOLD", code="pending" if pending else "no_report_for_commit", commit=commit)
    return 0


def status_gitflic(commit, value):
    ref = "mcpshka-snapshots/" + commit
    data = api_get("gitflic", "/cicd/pipeline?page=0&size=20", value)
    for p in data.get("_embedded", {}).get("restPipelineModelList", []):
        if p.get("commitId") != commit or p.get("ref") != ref:
            continue
        state = str(p.get("status", "")).upper()
        if state == "SUCCESS":
            result = "observed_pass"
        elif state in ("FAILED", "CANCELED", "WARNING", "ERROR"):
            result = "observed_fail"
        else:
            result = "HOLD"
        emit("gitflic", state=result, pipeline=p.get("localId"), pipeline_status=state, failure_reason=p.get("failureReason"), commit=commit)
        return 0
    emit("gitflic", state="HOLD", code="no_pipeline_for_commit", commit=commit)
    return 0


def status_github(commit, value):
    """Read the Actions runs of this exact commit. Each workflow is judged by
    its newest run: a dispatch cancelled by a newer one (concurrency) is not
    a failure. Any newest run that failed fails the commit; a pass needs
    every required workflow's newest run completed successfully."""
    runs = api_get("github", "/actions/runs?per_page=30&head_sha=" + commit, value).get("workflow_runs", [])
    runs = [r for r in runs if r.get("head_sha") == commit]
    if not runs:
        emit("github", state="HOLD", code="no_run_for_commit", commit=commit)
        return 0
    seen = {}
    for r in sorted(runs, key=lambda r: r.get("id") or 0, reverse=True):
        name = str(r.get("path", "")).rsplit("/", 1)[-1]
        seen.setdefault(name, []).append({"run": r.get("id"), "status": r.get("status"), "conclusion": r.get("conclusion"), "event": r.get("event")})
    newest = {name: xs[0] for name, xs in seen.items()}
    failed = [x for x in newest.values() if x["status"] == "completed" and x["conclusion"] not in ("success", "skipped", "neutral")]
    passed = all(w in newest and newest[w]["status"] == "completed" and newest[w]["conclusion"] == "success" for w in REQUIRED["github"])
    result = "observed_fail" if failed else "observed_pass" if passed else "HOLD"
    extra = {"code": "pending"} if result == "HOLD" else {}
    if failed:
        extra["diagnosis"] = github_rejection(failed[0]["run"], value)
    emit("github", state=result, runs=seen, commit=commit, **extra)
    return 0


def github_rejection(run, value):
    """S5.DIAGCI: jobs that failed with no step and no runner never ran — the
    account refused them (Actions minutes or billing of a private repo, 23.11)
    — which no new commit fixes; a job with steps failed in the code."""
    try:
        jobs = api_get("github", "/actions/runs/%s/jobs?per_page=50" % int(run), value).get("jobs", [])
    except Exception:
        jobs = []
    if jobs and all(not j.get("steps") and not j.get("runner_name") and j.get("conclusion") == "failure" for j in jobs):
        return {"code": "CI_ACCOUNT_BLOCKED", "reason": "GitHub rejected every job before a runner (0 steps): Actions minutes/billing of the account; rerunning or a new commit does not help", "jobs": len(jobs)}
    return {"code": "CI_TEST_FAILED", "reason": "jobs ran and failed: read their logs", "jobs": len(jobs)}


def main(argv=None):
    ap = argparse.ArgumentParser()
    ap.add_argument("site", choices=sorted(SITES))
    ap.add_argument("mode", choices=["plan", "publish", "mirror", "status", "pr"])
    ap.add_argument("--commit", required=True)
    a = ap.parse_args(argv)
    if not SHA_RE.match(a.commit):
        return hold(a.site, "invalid_commit")
    try:
        if a.mode == "plan":
            emit(a.site, state="PLANNED", remote_ref="refs/heads/mcpshka-snapshots/" + a.commit, required=REQUIRED[a.site])
            return 0
        if a.mode == "publish":
            return publish(a.site, a.commit)
        if a.mode == "mirror":
            return mirror(a.site, a.commit)
        if a.mode == "pr":
            return open_pr(a.commit) if a.site == "github" else hold(a.site, "pr_github_only")
        value = token(a.site)
        if a.site == "github":
            return status_github(a.commit, value)
        return status_sourcecraft(a.commit, value) if a.site == "sourcecraft" else status_gitflic(a.commit, value)
    except (OSError, ValueError, RuntimeError, urllib.error.URLError, subprocess.TimeoutExpired) as e:
        code = str(e) if isinstance(e, (ValueError, RuntimeError, PermissionError)) else type(e).__name__
        return hold(a.site, code[:80])


if __name__ == "__main__":
    sys.exit(main())
