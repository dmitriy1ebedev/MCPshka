#!/usr/bin/env python3
"""Stable SHA-256 of committed release source, independent of remote commit ancestry."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import tarfile
import threading
import time

SOURCE_PATHS = ("cmd/", "internal/", "packaging/", "skills/", ".github/workflows/", ".gitverse/workflows/")
SOURCE_FILES = {"go.mod", "go.sum", "Makefile"}
MAX_FILE = 16 << 20
CHUNK_SIZE = 64 << 10  # fixed-size hashing buffer; avoid allocating whole artifacts
MAX_TOTAL = 128 << 20
MAX_FILES = 20000
ARCHIVE_TIMEOUT = 20

class InvalidSource(ValueError):
    pass


def hash_stream(stream, size):
    """Hash exactly size bytes with constant-memory reads; reject short sources."""
    if not 0 <= size <= MAX_FILE:
        raise InvalidSource("source entry exceeds budget")
    digest = hashlib.sha256()
    while size:
        chunk = stream.read(min(CHUNK_SIZE, size))
        if not chunk:
            raise InvalidSource("truncated source entry")
        digest.update(chunk)
        size -= len(chunk)
    return digest.digest()


def source_digest(repo="."):
    proc = subprocess.Popen(["git", "-C", str(repo), "archive", "--format=tar", "HEAD"],
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    deadline = time.monotonic() + ARCHIVE_TIMEOUT
    timer = threading.Timer(ARCHIVE_TIMEOUT, proc.kill)
    timer.daemon = True
    timer.start()
    files = []
    total = 0
    completed_stream = False
    try:
        with tarfile.open(fileobj=proc.stdout, mode="r|") as archive:
            for member in archive:
                path = member.name
                if path not in SOURCE_FILES and not path.startswith(SOURCE_PATHS):
                    continue
                if path.startswith("/") or ".." in Path(path).parts:
                    raise InvalidSource("unsafe source path")
                if member.isdir():
                    continue
                if not member.isfile() or member.size > MAX_FILE:
                    raise InvalidSource("nonregular or oversized source entry")
                total += member.size
                if total > MAX_TOTAL or len(files) >= MAX_FILES:
                    raise InvalidSource("source budget exceeded")
                with archive.extractfile(member) as stream:
                    digest = hash_stream(stream, member.size)
                files.append((path, digest))
        completed_stream = True
    except (tarfile.TarError, OSError) as e:
        raise InvalidSource("invalid git archive") from e
    finally:
        # A rejected archive must not leave a git child running on H96.
        proc.stdout.close()
        if not completed_stream and proc.poll() is None:
            proc.kill()
        try:
            proc.wait(timeout=max(0.001, deadline - time.monotonic()))
        except subprocess.TimeoutExpired as e:
            proc.kill()
            proc.wait()
            raise InvalidSource("git archive timeout") from e
        finally:
            timer.cancel()
    error = proc.stderr.read(2048)
    proc.stderr.close()
    if proc.returncode != 0:
        raise InvalidSource("git archive failure: " + error.decode("utf-8", "replace")[:120])
    if not files or not any(name == "go.mod" for name, _ in files):
        raise InvalidSource("empty or invalid release source")
    files.sort()
    result = hashlib.sha256(b"mcpshka-release-source/v1\0")
    for name, digest in files:
        result.update(name.encode("utf-8") + b"\0" + digest)
    return {"schema": "mcpshka.source-identity/v1",
            "source_digest": result.hexdigest(), "file_count": len(files),
            "total_bytes": total}

def main():
    p = argparse.ArgumentParser()
    p.add_argument("--repo", default=".")
    a = p.parse_args()
    print(json.dumps(source_digest(a.repo), sort_keys=True))

if __name__ == "__main__":
    main()
