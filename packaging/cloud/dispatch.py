#!/usr/bin/env python3
"""Fixed Codex Cloud transport. No local apply, agent execution, merge or deploy."""
import base64
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import selectors
import subprocess
import sys
import tempfile
import time
import urllib.parse
import urllib.request

ROOT = Path("/root/repos/mcpshka")
SNAPSHOT_HELPER = Path("/usr/local/libexec/mcpshka-cloud-snapshot.py")
CONFIG = Path("/var/lib/mcpshka/cloud-agents/config.json")
TOKEN = Path("/root/.config/gh-mcpshka.token")
REPOSITORY = "dmitriy1ebedev/mcpshka-ci"
REMOTE = "https://github.com/" + REPOSITORY + ".git"
CODEX = "/root/.local/bin/codex"
SHA = re.compile(r"[a-f0-9]{40}\Z")
IDENTITY = re.compile(r"[A-Za-z0-9][A-Za-z0-9._:-]{0,127}\Z")
MAX_OUTPUT = 131072


def snapshot_module():
    spec = importlib.util.spec_from_file_location("immutable", SNAPSHOT_HELPER)
    m = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(m)
    return m


def run(argv, *, env=None, input_text=None):
    with tempfile.TemporaryDirectory(prefix="mcpshka-cloud-") as workdir, subprocess.Popen(argv, cwd=workdir, env=env, stdin=subprocess.PIPE if input_text is not None else subprocess.DEVNULL, stdout=subprocess.PIPE,
                          stderr=subprocess.DEVNULL) as p:
        deadline = time.monotonic() + 12
        output = bytearray()
        try:
            if input_text is not None:
                p.stdin.write(input_text.encode())
                p.stdin.close()
            with selectors.DefaultSelector() as selector:
                selector.register(p.stdout, selectors.EVENT_READ)
                while True:
                    remaining = deadline - time.monotonic()
                    if remaining <= 0 or not selector.select(remaining):
                        raise RuntimeError("provider_command_timeout")
                    chunk = os.read(p.stdout.fileno(), min(8192, MAX_OUTPUT + 1 - len(output)))
                    if not chunk:
                        break
                    output.extend(chunk)
                    if len(output) > MAX_OUTPUT:
                        raise RuntimeError("provider_output_budget")
            if p.wait(timeout=max(0.01, deadline - time.monotonic())):
                raise RuntimeError("provider_command_failed")
            return output.decode("utf-8", "strict")
        except BaseException:
            try:
                p.kill()
            except ProcessLookupError:
                pass
            p.wait()
            raise


def settings(m):
    config = json.loads(m.private_file(CONFIG))
    environment = config.get("codex_environment", "")
    if set(config) != {"codex_environment", "repository"} or config["repository"] != REPOSITORY or not IDENTITY.fullmatch(environment):
        raise RuntimeError("environment_mapping_invalid")
    return environment


def git_environment(token):
    # Fixed HTTPS host; credentials live only in this root child's environment.
    env = dict(os.environ)
    env.update(GIT_TERMINAL_PROMPT="0", GIT_CONFIG_GLOBAL="/dev/null", GIT_CONFIG_SYSTEM="/dev/null",
               GIT_CONFIG_COUNT="3", GIT_CONFIG_KEY_0="http.https://github.com/.extraheader",
               GIT_CONFIG_VALUE_0="AUTHORIZATION: basic " + base64.b64encode(("x-access-token:" + token).encode()).decode(),
               GIT_CONFIG_KEY_1="core.hooksPath", GIT_CONFIG_VALUE_1="/dev/null",
               GIT_CONFIG_KEY_2="credential.helper", GIT_CONFIG_VALUE_2="")
    return env


def publish(m, commit, token):
    source, tree = m.source_identity(commit)
    snap, _ = m.snapshot_commit(source, tree, os.environ.copy())
    branch = "mcpshka-snapshots/" + source
    ref = "refs/heads/" + branch
    env = git_environment(token)
    def readback():
        rows = m.git("ls-remote", "--heads", REMOTE, ref, env=env).splitlines()
        if not rows:
            return None
        if len(rows) != 1 or rows[0].split() != [snap, ref]:
            raise RuntimeError("immutable_source_ref_conflict")
        return snap
    if readback() is None:
        # Create only: a competing different ref is rejected, never force-pushed.
        m.git("push", REMOTE, snap + ":" + ref, env=env, timeout=12)
    if readback() != snap:
        raise RuntimeError("source_readback_failed")
    return snap, tree, branch


def receipt(p, raw):
    return dict(id=p["id"], owner=p["owner"], provider=p["provider"], commit=p["commit"],
                request_sha256=hashlib.sha256(raw).hexdigest(), state="blocked",
                trust="UNTRUSTED", deploy_authorized=False)


def task_id(output):
    ids = set(re.findall(r"https://chatgpt\.com/codex/tasks/([A-Za-z0-9_-]+)", output))
    if len(ids) != 1:
        raise RuntimeError("task_identity_not_returned")
    return ids.pop()


def start(p, raw):
    r = receipt(p, raw)
    if p["provider"] == "claude_cloud":
        r["reason"] = "claude_snapshot_transport_not_admitted"
        return r
    m = snapshot_module()
    try:
        environment = settings(m)
    except FileNotFoundError:
        r["reason"] = "owner_must_configure_codex_environment"
        return r
    try:
        # This read authenticates the provider without copying credentials to service.
        page = json.loads(run([CODEX, "cloud", "list", "--env", environment, "--json", "--limit", "1"]))
        if not isinstance(page.get("tasks"), list):
            raise RuntimeError("provider_auth_unverified")
        token = m.private_file(TOKEN).decode().strip()
        snap, tree, branch = publish(m, p["commit"], token)
    except FileNotFoundError:
        r["reason"] = "owner_must_provision_provider_credentials"
        return r
    except Exception:
        r["reason"] = "provider_auth_or_snapshot_publish_failed"
        return r
    r.update(snapshot_commit=snap, source_tree=tree, state="unknown", reason="submission_outcome_unknown")
    prompt = ("MCPshka cloud request " + p["id"] + "\nPinned source commit: " + snap +
              "\nPinned tree: " + tree + "\nVerify HEAD and tree before any work; stop on mismatch.\n" +
              "Work only in this cloud environment. Do not contact the host or its MCP servers. " +
              "Do not write main, deploy, merge, or bypass CI. Return a PR in " + REPOSITORY +
              " from cloud-agents/" + p["id"] + " based on " + branch +
              ". Include this request ID and task link in the PR body.\nTask:\n" + p["prompt"] +
              "\nAcceptance:\n" + p["acceptance"])
    try:
        output = run([CODEX, "cloud", "exec", "--env", environment, "--branch", branch,
                      "--attempts", "1", "--", "-"], input_text=prompt)
        r.update(provider_task_id=task_id(output), state="submitted", reason="")
    except (RuntimeError, subprocess.TimeoutExpired, OSError):
        pass  # An ambiguous POST is never retried.
    return r


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise RuntimeError("provider_redirect_rejected")


def github(path, token):
    req = urllib.request.Request("https://api.github.com/repos/" + REPOSITORY + path,
                                 headers={"Authorization": "Bearer " + token,
                                          "Accept": "application/vnd.github+json"})
    with urllib.request.build_opener(NoRedirect).open(req, timeout=8) as response:
        raw = response.read(MAX_OUTPUT + 1)
        if len(raw) > MAX_OUTPUT:
            raise RuntimeError("provider_output_budget")
        return json.loads(raw)


def result_pr(r, rows):
    branch = "cloud-agents/" + r["id"]
    for pr in rows:
        head, base = pr.get("head", {}), pr.get("base", {})
        if (pr.get("state") == "open" and not pr.get("merged_at") and
            (head.get("repo") or {}).get("full_name") == REPOSITORY and
            (base.get("repo") or {}).get("full_name") == REPOSITORY and
            head.get("ref") == branch and base.get("ref") == "mcpshka-snapshots/" + r["commit"] and
            base.get("sha") == r["snapshot_commit"] and SHA.fullmatch(head.get("sha", "")) and
            r["provider_task_id"] in (pr.get("body") or "") and
            pr.get("html_url") == "https://github.com/" + REPOSITORY + "/pull/" + str(pr.get("number"))):
            return dict(result_branch=branch, result_commit=head["sha"], pull_request=pr["html_url"])
    return None


def poll(r):
    m = snapshot_module()
    environment = settings(m)
    # Recompute source identity; a service-supplied receipt cannot select another tree.
    source, tree = m.source_identity(r["commit"])
    snap, _ = m.snapshot_commit(source, tree, os.environ.copy())
    if snap != r["snapshot_commit"] or tree != r["source_tree"]:
        raise RuntimeError("source_identity_changed")
    cursor = None
    found = None
    for _ in range(3):
        argv = [CODEX, "cloud", "list", "--env", environment, "--json", "--limit", "20"]
        if cursor:
            argv += ["--cursor", cursor]
        page = json.loads(run(argv))
        tasks = page.get("tasks")
        if not isinstance(tasks, list) or len(tasks) > 20:
            raise RuntimeError("provider_page_invalid")
        for task in tasks:
            if task.get("id") == r["provider_task_id"]:
                if found or task.get("environment_id") != environment:
                    raise RuntimeError("provider_task_identity_conflict")
                found = task
        cursor = page.get("cursor")
        if found or not cursor:
            break
        if not isinstance(cursor, str) or len(cursor) > 1024:
            raise RuntimeError("provider_cursor_invalid")
    if not found:
        raise RuntimeError("task_not_in_bounded_page")
    state = found.get("status")
    # Official CLI TaskStatus uses pending/ready/applied/error, not the
    # attempt-level completed/failed vocabulary used by older observations.
    if state not in {"pending", "ready", "applied", "error"}:
        raise RuntimeError("provider_state_unknown")
    r["provider_state"] = state
    r["state"] = "running"
    if state == "error":
        r["state"] = "failed"
    elif state in {"ready", "applied"}:
        r.update(state="awaiting_pr", reason="create_same_repository_pr_in_cloud_ui")
        token = m.private_file(TOKEN).decode().strip()
        query = urllib.parse.urlencode({"state": "open", "head": REPOSITORY.split("/")[0] + ":cloud-agents/" + r["id"], "per_page": 20})
        result = result_pr(r, github("/pulls?" + query, token))
        if result:
            comparison = github("/compare/" + snap + "..." + result["result_commit"] + "?per_page=1", token)
            if (comparison.get("merge_base_commit", {}).get("sha") != snap or
                comparison.get("status") not in {"ahead", "identical"}):
                raise RuntimeError("result_not_descended_from_snapshot")
            r.update(result, state="result", reason="gitverse_ci_then_integration_ship_required")
    return r


def main():
    if len(sys.argv) == 3 and sys.argv[1] == "configure":
        if os.geteuid() != 0 or not IDENTITY.fullmatch(sys.argv[2]):
            return 2
        # Root-only provisioning command; no login, provider mutation or task launch.
        CONFIG.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        info = CONFIG.parent.lstat()
        if CONFIG.parent.is_symlink() or info.st_uid != 0 or info.st_mode & 0o077:
            return 2
        raw = json.dumps({"repository": REPOSITORY, "codex_environment": sys.argv[2]}).encode()
        fd = os.open(CONFIG, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        with os.fdopen(fd, "wb") as target:
            target.write(raw)
            target.flush()
            os.fsync(target.fileno())
        print("Codex environment mapping created; no task submitted")
        return 0
    raw = sys.stdin.buffer.read(12001)
    try:
        if len(raw) > 12000:
            raise RuntimeError("request_budget")
        p = json.loads(raw)
        if sys.argv[1:] == ["cloud-agent-start"]:
            out = start(p, raw)
        elif sys.argv[1:] == ["cloud-agent-poll"]:
            out = poll(p)
        else:
            raise RuntimeError("invalid_action")
        print(json.dumps(out, separators=(",", ":")))
        return 0
    except Exception:
        # Never expose CLI stderr, tokens, environment, prompts or remote URLs.
        return 2


if __name__ == "__main__":
    sys.exit(main())
