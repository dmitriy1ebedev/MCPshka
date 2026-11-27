#!/usr/bin/env python3
"""Read authenticated MosHub CI state, bound to a local source SHA and tree."""
import argparse
import json
import re
import sys
import urllib.error
import urllib.parse
import urllib.request

import importlib.util
from pathlib import Path

# forge_ci runs this with python3 -I, which leaves the script directory off
# sys.path; load the sibling by its fixed path instead of a plain import.
_spec = importlib.util.spec_from_file_location("moshub_snapshot", Path(__file__).resolve().with_name("snapshot.py"))
_snapshot = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_snapshot)
REPO, SHA_RE, api, git = _snapshot.REPO, _snapshot.SHA_RE, _snapshot.api, _snapshot.git
private_file, TOKEN, NoRedirect = _snapshot.private_file, _snapshot.TOKEN, _snapshot.NoRedirect
snapshot_commit, verify_private_project = _snapshot.snapshot_commit, _snapshot.verify_private_project

RUNNING = {"created", "waiting_for_resource", "preparing", "pending", "running", "scheduled"}
FAILED = {"failed", "canceled", "skipped", "manual"}
STATUSES = RUNNING | FAILED | {"success"}
JOB = "moshub-test-build"


def require(condition):
    if not condition:
        raise RuntimeError("moshub_ci_evidence_invalid")


def evaluate(source_sha, tree, snapshot_sha, pipelines, jobs):
    """Pure fixture-testable evaluation; unknown or incomplete success is rejected."""
    require(all(isinstance(s, str) and SHA_RE.fullmatch(s) for s in (source_sha, tree, snapshot_sha)))
    result = {"provider": "moshub", "source_sha": source_sha, "tree": tree,
              "state": "missing", "jobs": {}}
    require(isinstance(pipelines, list))
    if not pipelines:
        return result
    ref = "mcpshka-snapshots/" + source_sha
    ids = []
    for pipeline in pipelines:
        require(isinstance(pipeline, dict))
        require(pipeline.get("ref") == ref and pipeline.get("sha") == snapshot_sha)
        require(type(pipeline.get("id")) is int and pipeline["id"] > 0)
        require(pipeline.get("status") in STATUSES)
        ids.append(pipeline["id"])
    require(ids == sorted(set(ids), reverse=True))
    pipeline = pipelines[0]
    require(isinstance(jobs, list))
    for job in jobs:
        require(isinstance(job, dict) and job.get("name") == JOB)
        require(job["name"] not in result["jobs"] and job.get("status") in STATUSES)
        require(job.get("allow_failure") is False and job.get("ref") == ref)
        require(isinstance(job.get("commit"), dict) and job["commit"].get("id") == snapshot_sha)
        require(isinstance(job.get("pipeline"), dict) and job["pipeline"].get("id") == pipeline["id"])
        result["jobs"][job["name"]] = job["status"]
    status = pipeline["status"]
    if status == "success":
        require(result["jobs"] == {JOB: "success"})
        state = "passed"
    elif status in FAILED or any(s in FAILED for s in result["jobs"].values()):
        state = "failed"
    else:
        state = "running"
    result.update(state=state, pipeline_id=pipeline["id"])
    return result


def observe(source_sha, tree, snapshot_sha, request=api):
    ref = "mcpshka-snapshots/" + source_sha
    query = urllib.parse.urlencode({"ref": ref, "order_by": "id", "sort": "desc", "per_page": 100})
    pipelines = request("/pipelines?" + query)
    # Validate pipeline identity before using its ID to form another API request.
    if not pipelines:
        return evaluate(source_sha, tree, snapshot_sha, pipelines, [])
    require(isinstance(pipelines, list) and isinstance(pipelines[0], dict))
    latest = pipelines[0]
    require(type(latest.get("id")) is int and latest["id"] > 0)
    branch = request("/repository/branches/" + urllib.parse.quote(ref, safe=""))
    require(isinstance(branch, dict) and branch.get("name") == ref)
    require(isinstance(branch.get("commit"), dict) and branch["commit"].get("id") == snapshot_sha)
    jobs = request(f"/pipelines/{latest['id']}/jobs?per_page=100&include_retried=false")
    return evaluate(source_sha, tree, snapshot_sha, pipelines, jobs)


def retry(pipeline_id):
    """Retry the failed jobs of one pipeline (a flaky runner, not a code fix)."""
    require(type(pipeline_id) is int and pipeline_id > 0)
    token = private_file(TOKEN, 4096).strip()
    require(token and b"\n" not in token and b"\r" not in token)
    req = urllib.request.Request(f"https://hub.mos.ru/api/v4/projects/91385/pipelines/{pipeline_id}/retry",
                                 headers={"PRIVATE-TOKEN": token.decode("ascii")}, method="POST")
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    try:
        with opener.open(req, timeout=12) as response:
            require(response.status in (200, 201))
    except urllib.error.HTTPError:
        raise RuntimeError("moshub_retry_refused") from None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source_sha")
    parser.add_argument("--rerun", action="store_true", help="retry the snapshot pipeline if it failed")
    args = parser.parse_args()
    result = {"provider": "moshub", "source_sha": args.source_sha, "tree": None,
              "state": "failed", "jobs": {}}
    try:
        require(SHA_RE.fullmatch(args.source_sha) is not None)
        tree = git(REPO, "rev-parse", args.source_sha + "^{tree}")
        result["tree"] = tree
        snap = snapshot_commit(args.source_sha, tree)
        verify_private_project()
        result = observe(args.source_sha, tree, snap)
        if args.rerun:
            if result["state"] != "failed" or "pipeline_id" not in result:
                result["rerun"] = "not_needed"
                print(json.dumps(result, sort_keys=True))
                return 0
            retry(result["pipeline_id"])
            result.update(state="running", rerun="retried")
    except Exception:
        result["error"] = "moshub_ci_unverified"
    print(json.dumps(result, sort_keys=True))
    return 0 if result["state"] == "passed" or result.get("rerun") == "retried" else 1


if __name__ == "__main__":
    sys.exit(main())
