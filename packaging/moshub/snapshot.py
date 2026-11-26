#!/usr/bin/env python3
"""Fail-closed, private-only MosHub Git snapshot publisher.

No production credentials are read during plan. Publish requires a verified
private MosHub API project and HTTPS with verified TLS.
The snapshot ref is immutable and never modifies remote main.
"""
import argparse
from contextlib import contextmanager
from datetime import datetime
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

REPO = Path(__file__).resolve().parents[2]
TOKEN = Path("/var/lib/mcpshka/moshub/api_token")
PROJECT = "lda6994/mcpshka-ci"
PROJECT_ID = 91385
LIMIT = 32768
PROJECT_RE = re.compile(r"[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)*\Z")
SHA_RE = re.compile(r"[0-9a-f]{40}\Z")


def fail(code):
    print(json.dumps({"schema": "mcpshka.moshub-snapshot/v1", "state": "HOLD",
                      "code": code, "published": False, "trust": "UNTRUSTED"},
                     sort_keys=True))
    return 2


def git(root, *args, env=None, data=None):
    cmd = ["git", "-C", str(root), *args]
    p = subprocess.run(cmd, input=data, stdout=subprocess.PIPE,
                       stderr=subprocess.DEVNULL, env=env, timeout=20,
                       check=False)
    if p.returncode or len(p.stdout) > LIMIT:
        raise RuntimeError("git_operation_unverified")
    return p.stdout.decode("utf-8", errors="strict").strip()


def source_identity(root, commit=""):
    if commit:
        # Any commit of the canonical object store (worktree branches too).
        try:
            kind = git(root, "cat-file", "-t", commit) if SHA_RE.fullmatch(commit) else ""
        except RuntimeError:
            kind = ""
        if kind != "commit":
            raise RuntimeError("source_commit_unknown")
        tree = git(root, "rev-parse", commit + "^{tree}")
        if not SHA_RE.fullmatch(tree):
            raise RuntimeError("invalid_git_source")
        return commit, tree
    if git(root, "status", "--porcelain", "--untracked-files=no"):
        raise RuntimeError("source_tracked_changes_present")
    sha = git(root, "rev-parse", "HEAD")
    tree = git(root, "rev-parse", "HEAD^{tree}")
    if not SHA_RE.fullmatch(sha) or not SHA_RE.fullmatch(tree):
        raise RuntimeError("invalid_git_source")
    return sha, tree


def remote(project):
    if not PROJECT_RE.fullmatch(project) or ".." in project.split("/"):
        raise RuntimeError("invalid_project_name")
    return "https://hub.mos.ru/" + project + ".git"


def private_file(path, maximum=8192):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        info = os.fstat(fd)
        if (not stat.S_ISREG(info.st_mode) or info.st_nlink != 1
                or info.st_uid != os.geteuid() or info.st_mode & 0o077
                or info.st_size < 1 or info.st_size > maximum):
            raise RuntimeError("credentials_not_isolated")
        content = os.read(fd, maximum + 1)
        if len(content) != info.st_size:
            raise RuntimeError("credential_read_incomplete")
        return content
    finally:
        os.close(fd)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *unused):
        raise RuntimeError("moshub_unexpected_redirect")


def api(path, missing_ok=False, first_page=False):
    # Fixed origin, no proxies or redirects; never expose response bodies/errors.
    token = private_file(TOKEN, 4096).strip()
    if not token or b"\n" in token or b"\r" in token:
        raise RuntimeError("credential_invalid")
    req = urllib.request.Request("https://hub.mos.ru/api/v4/projects/91385" + path,
                                 headers={"PRIVATE-TOKEN": token.decode("ascii")})
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    try:
        with opener.open(req, timeout=12) as response:
            if response.status != 200 or (response.headers.get("X-Next-Page") and not first_page):
                raise RuntimeError("moshub_api_incomplete")
            payload = response.read(LIMIT + 1)
    except urllib.error.HTTPError as exc:
        if missing_ok and exc.code == 404:
            return None
        raise RuntimeError("moshub_api_unavailable_or_unauthorized") from None
    except Exception:
        raise RuntimeError("moshub_api_unavailable_or_unauthorized") from None
    if len(payload) > LIMIT:
        raise RuntimeError("moshub_api_over_budget")
    try:
        return json.loads(payload)
    except Exception:
        raise RuntimeError("moshub_api_invalid") from None


def verify_private_project(project=PROJECT):
    obj = api("")
    if (not isinstance(obj, dict) or obj.get("id") != PROJECT_ID
            or obj.get("path_with_namespace") != project
            or obj.get("visibility") != "private"
            or obj.get("http_url_to_repo") != remote(project)
            or obj.get("archived") is not False or obj.get("jobs_enabled") is not True):
        raise RuntimeError("project_identity_or_privacy_unverified")
    return PROJECT_ID


def branch_commit(sha):
    name = "mcpshka-snapshots/" + sha
    obj = api("/repository/branches/" + urllib.parse.quote(name, safe=""), missing_ok=True)
    if obj is None:
        return None
    if (not isinstance(obj, dict) or obj.get("name") != name
            or not isinstance(obj.get("commit"), dict)
            or not SHA_RE.fullmatch(str(obj["commit"].get("id", "")))):
        raise RuntimeError("remote_ref_invalid")
    return obj["commit"]["id"]


@contextmanager
def https_environment():
    private_file(TOKEN, 4096)
    with tempfile.TemporaryDirectory(prefix="mcpshka-moshub-") as directory:
        os.chmod(directory, 0o700)
        askpass = Path(directory) / "askpass"
        askpass.write_text(
            "#!/usr/bin/python3\nimport sys\nfrom pathlib import Path\n"
            "prompt = sys.argv[1]\n"
            "if prompt.startswith(\"Username for 'https://hub.mos.ru\"):\n"
            "    print('oauth2')\n"
            "elif prompt.startswith(\"Password for 'https://oauth2@hub.mos.ru\"):\n"
            "    print(Path(" + repr(str(TOKEN)) + ").read_text().strip())\n"
            "else:\n    sys.exit(1)\n")
        askpass.chmod(0o700)
        env = {k: v for k, v in os.environ.items()
               if not k.startswith("GIT_") and k.lower() not in
               ("https_proxy", "http_proxy", "all_proxy")}
        env.update(GIT_ASKPASS=str(askpass), GIT_TERMINAL_PROMPT="0",
                   GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL="/dev/null",
                   GIT_CONFIG_COUNT="5", GIT_CONFIG_KEY_0="http.sslVerify",
                   GIT_CONFIG_VALUE_0="true", GIT_CONFIG_KEY_1="credential.helper",
                   GIT_CONFIG_VALUE_1="", GIT_CONFIG_KEY_2="http.followRedirects",
                   GIT_CONFIG_VALUE_2="false", GIT_CONFIG_KEY_3="http.proxy",
                   GIT_CONFIG_VALUE_3="", GIT_CONFIG_KEY_4="core.hooksPath",
                   GIT_CONFIG_VALUE_4="/dev/null")
        yield env


def snapshot_commit(sha, tree):
    dates = git(REPO, "show", "-s", "--format=%aI%n%cI", sha).splitlines()
    if len(dates) != 2:
        raise RuntimeError("git_dates_unverified")
    env = os.environ.copy()
    env.update(GIT_AUTHOR_NAME="MCPshka Snapshot", GIT_AUTHOR_EMAIL="snapshot@mcpshka.local",
               GIT_COMMITTER_NAME="MCPshka Snapshot", GIT_COMMITTER_EMAIL="snapshot@mcpshka.local",
               GIT_AUTHOR_DATE=dates[0], GIT_COMMITTER_DATE=dates[1])
    message = ("MCPshka MosHub snapshot\n\nSource-Commit: " + sha
               + "\nSource-Tree: " + tree + "\n").encode()
    snap = git(REPO, "commit-tree", tree, env=env, data=message)
    if (not SHA_RE.fullmatch(snap)
            or git(REPO, "rev-list", "--parents", "-n", "1", snap) != snap
            or git(REPO, "rev-parse", snap + "^{tree}") != tree):
        raise RuntimeError("snapshot_identity_invalid")
    return snap


def runners_online():
    """A snapshot pushed while every MosHub runner is offline never starts:
    the pipeline sits pending for hours, fails as stuck and mails the owner
    (12.11.2026). An unreadable runner list does not block the publish."""
    try:
        runners = api("/runners?per_page=100")
    except Exception:
        return True
    if not isinstance(runners, list) or not runners:
        # Shared runners are not listed for the project: judge by history.
        return not stuck_history()
    return any(isinstance(r, dict) and r.get("status") == "online" and r.get("active", True) is not False
               for r in runners)


STUCK_STREAK = 3
PROBE_AFTER = 24 * 3600


def stuck_history(now=None):
    """True when the newest STUCK_STREAK finished snapshot pipelines never
    started (every job stuck_or_timeout_failure): 12–24.11.2026 every
    pipeline was stuck, no shared runner took the appsechub tag. One probe
    publish a day still goes through, so a returning runner is noticed."""
    try:
        pipes = api("/pipelines?scope=finished&per_page=%d" % STUCK_STREAK, first_page=True)
        if not isinstance(pipes, list) or len(pipes) < STUCK_STREAK:
            return False
        for p in pipes:
            jobs = api("/pipelines/%d/jobs?per_page=20" % int(p["id"]), first_page=True)
            if not jobs or any(j.get("failure_reason") != "stuck_or_timeout_failure" for j in jobs):
                return False
        newest = datetime.fromisoformat(pipes[0]["created_at"].replace("Z", "+00:00")).timestamp()
    except Exception:
        return False
    return (now or time.time()) - newest < PROBE_AFTER


def publish(sha, tree):
    verify_private_project()
    snap = snapshot_commit(sha, tree)
    before = branch_commit(sha)
    if before is not None and before != snap:
        raise RuntimeError("remote_ref_conflict")
    if before is None and not runners_online():
        raise RuntimeError("no_online_runner")
    if before is None:
        ref = "refs/heads/mcpshka-snapshots/" + sha
        with https_environment() as env:
            # Empty expected lease refuses replacement, including concurrent creation.
            git(REPO, "push", "--force-with-lease=" + ref + ":", remote(PROJECT),
                snap + ":" + ref, env=env)
    if branch_commit(sha) != snap:
        raise RuntimeError("remote_verification_failed")
    return snap, "ALREADY_PRESENT" if before else "PUBLISHED"


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("mode", choices=["plan", "publish"])
    parser.add_argument("--commit", default="")
    args = parser.parse_args()
    try:
        sha, tree = source_identity(REPO, args.commit)
        ref = "refs/heads/mcpshka-snapshots/" + sha
        plan = {"schema": "mcpshka.moshub-snapshot/v1",
                "source_commit": sha, "source_tree": tree,
                "remote_ref": ref, "published": False, "trust": "UNTRUSTED"}
        if args.mode == "plan":
            missing = [] if TOKEN.is_file() else ["api_token"]
            plan.update(state="HOLD" if missing else "CONFIG_PRESENT_UNVERIFIED",
                        missing=missing,
                        next_step="verify private project via authenticated API before publish")
            print(json.dumps(plan, sort_keys=True))
            return 0
        snap, state = publish(sha, tree)
        plan.update(state=state, snapshot_commit=snap, published=True,
                    project=PROJECT, source_verified=True,
                    deploy_authorized=False)
        print(json.dumps(plan, sort_keys=True))
        return 0
    except Exception as exc:
        # Error code only; never leak URLs, credentials, filesystem or Git stderr.
        code = str(exc) if isinstance(exc, RuntimeError) and re.fullmatch(r"[a-z_]+", str(exc)) else "moshub_unverified"
        return fail(code)


if __name__ == "__main__":
    sys.exit(main())
