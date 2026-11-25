#!/usr/bin/env python3
"""Bounded cross-build matrix for MCPshka P0/CI preparation.

Builds Linux ARM64 and AMD64 sequentially with CGO disabled, verifies the ELF
machine type and emits compact JSON. By default all binaries live in a
temporary directory and are removed after verification.
"""

import argparse
import hashlib
import json
import os
import pathlib
import struct
import subprocess
import tempfile


ARCHES = {
    "arm64": 183,  # EM_AARCH64
    "amd64": 62,   # EM_X86_64
}


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        while True:
            chunk = f.read(1024 * 1024)
            if not chunk:
                break
            h.update(chunk)
    return h.hexdigest()


def elf_machine(path):
    with open(path, "rb") as f:
        header = f.read(20)
    if len(header) < 20 or header[:4] != b"\x7fELF":
        raise RuntimeError("%s is not an ELF binary" % path)
    if header[4] != 2:
        raise RuntimeError("%s is not ELF64" % path)
    endian = "<" if header[5] == 1 else ">" if header[5] == 2 else None
    if endian is None:
        raise RuntimeError("%s has invalid ELF byte order" % path)
    return struct.unpack(endian + "H", header[18:20])[0]


def build_one(root, out_dir, arch, timeout=180):
    target = pathlib.Path(out_dir) / ("mcpshka-linux-" + arch)
    env = os.environ.copy()
    env.update({
        "GOOS": "linux",
        "GOARCH": arch,
        "CGO_ENABLED": "0",
        "GOMAXPROCS": "2",
    })
    proc = subprocess.run(
        ["go", "build", "-trimpath", "-buildvcs=false", "-o", str(target), "./cmd/mcpshka"],
        cwd=str(root),
        env=env,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        universal_newlines=True,
        timeout=timeout,
    )
    if proc.returncode != 0:
        detail = (proc.stderr or proc.stdout or "build failed").strip()
        if len(detail) > 4096:
            detail = detail[-4096:]
        raise RuntimeError("%s build failed: %s" % (arch, detail))
    machine = elf_machine(str(target))
    expected = ARCHES[arch]
    if machine != expected:
        raise RuntimeError("%s ELF machine=%d expected=%d" % (arch, machine, expected))
    return {
        "arch": arch,
        "elf_machine": machine,
        "sha256": sha256_file(str(target)),
        "size_bytes": target.stat().st_size,
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", default=str(pathlib.Path(__file__).resolve().parents[2]))
    parser.add_argument("--output-dir", default="")
    parser.add_argument("--arch", choices=("all", "arm64", "amd64"), default="all")
    parser.add_argument("--timeout", type=int, default=180, help="Per-architecture build timeout, 1–780 seconds")
    args = parser.parse_args()
    if not 1 <= args.timeout <= 780:
        parser.error("--timeout must be between 1 and 780 seconds")

    root = pathlib.Path(args.root).resolve()
    if not (root / "go.mod").is_file():
        raise RuntimeError("go.mod not found under %s" % root)

    temp = None
    if args.output_dir:
        out_dir = pathlib.Path(args.output_dir).resolve()
        out_dir.mkdir(parents=True, exist_ok=True)
    else:
        temp = tempfile.TemporaryDirectory(prefix="mcpshka-build-matrix-")
        out_dir = pathlib.Path(temp.name)

    try:
        selected = ("arm64", "amd64") if args.arch == "all" else (args.arch,)
        builds = [build_one(root, out_dir, arch, args.timeout) for arch in selected]
        print(json.dumps({
            "schema": "mcpshka.build-matrix/v1",
            "ok": True,
            "goos": "linux",
            "builds": builds,
            "artifacts_retained": bool(args.output_dir),
        }, sort_keys=True))
    finally:
        if temp is not None:
            temp.cleanup()


if __name__ == "__main__":
    main()
