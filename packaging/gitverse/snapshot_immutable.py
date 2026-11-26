#!/usr/bin/env python3
"""Immutable GitVerse snapshot; never rewrites remote main or reads AI secrets."""
import argparse
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys

REPO = Path("/root/repos/mcpshka")
KEY = Path("/var/lib/mcpshka/gitverse/id_ed25519")
HOSTS = Path("/var/lib/mcpshka/gitverse/known_hosts")
REMOTE = "git@gitverse.ru:dmitriy_1ebedev/mcpshka-ci.git"
FINGERPRINT = "SHA256:IL4khwaSrT4uHCu9LTUW7dRJyMB5XkOJnFrP0nmdMXU"
MAX_OUTPUT = 8192


def hold(code, detail=""):
    out = {"schema": "mcpshka.gitverse-immutable/v1", "state": "HOLD",
           "reason_code": code, "published": False, "deploy_authorized": False}
    if detail:
        out["detail"] = detail
    print(json.dumps(out))
    return 2


class StepFailed(RuntimeError):
    """A failed git/ssh step: a reason code plus a sanitized stderr tail."""

    def __init__(self, code, detail=""):
        super().__init__(code)
        self.detail = detail


def classify(step, stderr):
    # The generic provider_operation_failed hid why a push failed twice in a
    # row (BUG-20261022-59); the class and the stderr tail now say it.
    text = stderr.decode("utf-8", "replace")
    tail = re.sub(r"[^\x20-\x7e]+", " ", text)[-300:].strip()
    tail = re.sub(r"\S+@[\w.-]+:", "<remote>:", tail)
    low = text.lower()
    if "permission denied" in low or "host key" in low:
        kind = "auth"
    elif any(m in low for m in ("could not resolve", "timed out", "connection refused", "connection reset", "connection closed", "broken pipe", "early eof", "remote end hung up")):
        kind = "network"
    elif any(m in low for m in ("rejected", "declined", "denied", "protected branch")):
        kind = "remote_rejected"
    else:
        kind = "failed"
    return StepFailed(step + "_" + kind, tail)


def run(args, env=None, data=None, allow_fail=False, timeout=20):
    step = args[3] if args[:1] == ["git"] and len(args) > 3 else os.path.basename(args[0])
    step = re.sub(r"[^a-z]+", "_", step.lower()).strip("_") or "step"
    try:
        proc = subprocess.run(args, cwd=REPO, input=data, stdout=subprocess.PIPE,
                              stderr=subprocess.PIPE, timeout=timeout, env=env, check=False)
    except subprocess.TimeoutExpired:
        raise StepFailed(step + "_timeout")
    if len(proc.stdout) > MAX_OUTPUT:
        raise StepFailed(step + "_output_limit")
    if proc.returncode != 0 and not allow_fail:
        raise classify(step, proc.stderr)
    return proc


def git(*args, env=None, data=None, timeout=20):
    return run(["git", "-C", str(REPO), *args], env=env, data=data, timeout=timeout).stdout.decode("utf-8", "strict").strip()


def source_identity(commit=""):
    if commit:
        # Any commit of the canonical object store (worktree branches too): a
        # commit is immutable, so only its existence and identity are checked.
        if not re.fullmatch(r"[a-f0-9]{40}", commit) or run(
                ["git", "-C", str(REPO), "cat-file", "-t", commit], allow_fail=True).stdout.strip() != b"commit":
            raise RuntimeError("source_commit_unknown")
        return commit, git("rev-parse", commit + "^{tree}")
    if git("symbolic-ref", "--quiet", "--short", "HEAD") != "main":
        raise RuntimeError("source_not_main")
    if git("status", "--porcelain", "--untracked-files=no"):
        raise RuntimeError("source_dirty")
    sha, tree = git("rev-parse", "HEAD"), git("rev-parse", "HEAD^{tree}")
    if not all(re.fullmatch(r"[a-f0-9]{40}", s) for s in (sha, tree)):
        raise RuntimeError("invalid_git_identity")
    return sha, tree


def private_file(path):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        st = os.fstat(fd)
        if not stat.S_ISREG(st.st_mode) or st.st_nlink != 1 or st.st_uid != os.geteuid() or st.st_mode & 0o077 or not 1 <= st.st_size <= 16384:
            raise RuntimeError("unsafe_credential_permissions")
        return os.read(fd, 16384)
    finally:
        os.close(fd)


def pin_matches(text):
    return any(FINGERPRINT in ln and "gitverse.ru (RSA)" in ln for ln in text.splitlines())


def ssh_env():
    private_file(KEY)
    private_file(HOSTS)
    check = run(["ssh-keygen", "-lf", str(HOSTS)]).stdout.decode("ascii")
    if not pin_matches(check):
        raise RuntimeError("official_host_fingerprint_mismatch")
    env = os.environ.copy()
    env["GIT_TERMINAL_PROMPT"] = "0"
    env["GIT_SSH_COMMAND"] = (
        "ssh -F /dev/null -i " + str(KEY) + " -o IdentitiesOnly=yes"
        + " -o BatchMode=yes -o UserKnownHostsFile=" + str(HOSTS)
        + " -o StrictHostKeyChecking=yes -o HostKeyAlgorithms=rsa-sha2-512,rsa-sha2-256"
        + " -o ConnectTimeout=8")
    return env


def snapshot_commit(sha, tree, env):
    stamp = git("show", "-s", "--format=%aI%n%cI", sha).splitlines()
    if len(stamp) != 2:
        raise RuntimeError("source_timestamps_unverified")
    env = env.copy()
    env.update(GIT_AUTHOR_NAME="MCPshka Snapshot", GIT_AUTHOR_EMAIL="snapshot@mcpshka.local",
               GIT_COMMITTER_NAME="MCPshka Snapshot", GIT_COMMITTER_EMAIL="snapshot@mcpshka.local",
               GIT_AUTHOR_DATE=stamp[0], GIT_COMMITTER_DATE=stamp[1])
    payload = ("MCPshka immutable snapshot\n\nSource-Commit: " + sha + "\nSource-Tree: " + tree + "\n").encode()
    snap = git("commit-tree", tree, env=env, data=payload)
    if not re.fullmatch(r"[a-f0-9]{40}", snap):
        raise RuntimeError("snapshot_sha_invalid")
    if len(git("rev-list", "--parents", "-n", "1", snap).split()) != 1:
        raise RuntimeError("snapshot_not_parentless")
    return snap, env


def remote_ref(ref, env):
    output = git("ls-remote", "--heads", REMOTE, ref, env=env)
    if not output:
        return None
    rows = output.splitlines()
    if len(rows) != 1 or len(rows[0].split()) != 2:
        raise RuntimeError("unexpected_remote_ref")
    sha, name = rows[0].split()
    if name != ref or not re.fullmatch(r"[a-f0-9]{40}", sha):
        raise RuntimeError("remote_ref_invalid")
    return sha


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("mode", choices=["plan", "publish"])
    parser.add_argument("--commit", default="")
    args = parser.parse_args()
    try:
        sha, tree = source_identity(args.commit)
        ref = "refs/heads/mcpshka-snapshots/" + sha
        result = {"schema": "mcpshka.gitverse-immutable/v1", "source_commit": sha,
                  "source_tree": tree, "remote_ref": ref, "published": False,
                  "deploy_authorized": False}
        if args.mode == "plan":
            result["state"] = "PLANNED_UNVERIFIED"
            print(json.dumps(result, sort_keys=True))
            return 0
        env = ssh_env()
        snap, env = snapshot_commit(sha, tree, env)
        existing = remote_ref(ref, env)
        if existing is not None and existing != snap:
            raise RuntimeError("immutable_ref_conflict")
        if existing is None:
            # Git refuses a simultaneous conflicting creation: never use -f.
            git("push", REMOTE, snap + ":" + ref, env=env, timeout=90)
        after = remote_ref(ref, env)
        if after != snap:
            raise RuntimeError("remote_readback_not_proven")
        result.update(state="ALREADY_PRESENT" if existing is not None else "PUBLISHED",
                      snapshot_commit=snap, published=True, remote_verified=True)
        print(json.dumps(result, sort_keys=True))
        return 0
    except Exception as e:
        code = str(e)
        if not re.fullmatch(r"[a-z_]{3,60}", code):
            code = "provider_unknown"
        return hold(code, getattr(e, "detail", ""))


if __name__ == "__main__":
    sys.exit(main())
