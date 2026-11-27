#!/usr/bin/python3
"""Copy bounded bytes, then probe them in a disposable unprivileged unit."""
import os
import fcntl
import pwd
import re
import resource
import stat
import subprocess
import sys
import tempfile

ENV = {"PATH": "/usr/bin:/bin", "LANG": "C", "LC_ALL": "C"}
MAX_IMAGE = 128 << 20
PROBE_ROOT = "/usr/local/bin/.mcpshka-probe-images"
PROBE_IMAGE = "/run/mcpshka-probe-image"

def allowed_source(path):
    if path in ("/usr/local/bin/mcpshka", "/usr/local/bin/mcpshka.previous", "/root/repos/mcpshka/bin/mcpshka"):
        return True
    parent, name = os.path.split(path)
    return bool((parent == "/root/repos/mcpshka/bin" and re.fullmatch(r"mcpshka-[A-Za-z0-9._-]+\.candidate", name)) or
                (parent == "/usr/local/bin" and re.fullmatch(r"\.mcpshka\.(new|previous|restore|rollback)\.[A-Za-z0-9]+", name)))

def unit_args(unit, image, operation):
    if not re.fullmatch(r"mcpshka-probe-[a-f0-9]{16}\.service", unit) or operation not in ("version", "state-store-support"):
        raise ValueError("invalid probe unit or operation: HOLD")
    properties = ["DynamicUser=yes", "User=" + unit.removesuffix(".service"), "NoNewPrivileges=yes", "CapabilityBoundingSet=",
                  "PrivateNetwork=yes", "PrivateDevices=yes", "ProtectHome=yes", "ProtectSystem=strict",
                  "BindReadOnlyPaths=" + image + ":" + PROBE_IMAGE,
                  "ProtectProc=invisible", "ProcSubset=pid", "ProtectKernelTunables=yes",
                  "ProtectKernelModules=yes", "ProtectControlGroups=yes", "RestrictSUIDSGID=yes",
                  "RestrictNamespaces=yes", "RestrictAddressFamilies=AF_UNIX", "LockPersonality=yes",
                  "TemporaryFileSystem=/tmp:rw,size=16M /var/tmp:rw,size=16M",
                  "InaccessiblePaths=-/var/lib/mcpshka -/var/lib/mcpshka-executor -/run/mcpshka-broker",
                  "MemoryMax=128M", "CPUQuota=25%", "TasksMax=24", "RuntimeMaxSec=5s", "LimitFSIZE=4096",
                  "TimeoutStartSec=5s", "TimeoutStopSec=1s", "KillMode=control-group", "Restart=no", "UMask=0077"]
    return ["/usr/bin/systemd-run", "--quiet", "--wait", "--pipe", "--collect", "--service-type=exec",
            "--unit=" + unit] + ["--property=" + p for p in properties] + [
                "/usr/bin/env", "-i", "HOME=/nonexistent", "PATH=/usr/bin:/bin", "LANG=C", PROBE_IMAGE, operation]

def stage_image(source, directory):
    if os.path.realpath(source) != source:
        raise ValueError("probe source parent is aliased: HOLD")
    parent = os.open(os.path.dirname(source), os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        fd = os.open(os.path.basename(source), os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=parent)
    except BaseException:
        os.close(parent)
        raise
    name = None
    try:
        before = os.fstat(fd)
        parent_before = os.fstat(parent)
        if before.st_dev != parent_before.st_dev or mount_id(fd) != mount_id(parent):
            raise ValueError("probe source crosses mount boundary: HOLD")
        if not stat.S_ISREG(before.st_mode) or before.st_nlink != 1 or not 0 < before.st_size <= MAX_IMAGE:
            raise ValueError("unsafe probe image: HOLD")
        space = os.statvfs(directory)
        if space.f_bavail * space.f_frsize - before.st_size < max(3 << 30, space.f_blocks * space.f_frsize // 10):
            raise ValueError("probe disk reserve insufficient: HOLD")
        target, name = tempfile.mkstemp(prefix=".mcpshka.probe.", dir=directory)
        try:
            count = 0
            while True:
                chunk = os.read(fd, min(32768, before.st_size + 1 - count))
                if not chunk:
                    break
                count += len(chunk)
                if count > before.st_size:
                    raise ValueError("probe source grew: HOLD")
                view = memoryview(chunk)
                while view:
                    written = os.write(target, view)
                    if written <= 0:
                        raise OSError("short probe copy")
                    view = view[written:]
            after = os.fstat(fd)
            pin = lambda s: (s.st_dev, s.st_ino, s.st_size, s.st_mode, s.st_uid, s.st_nlink, s.st_mtime_ns, s.st_ctime_ns)
            public = os.stat(source, follow_symlinks=False)
            parent_after = os.stat(os.path.dirname(source), follow_symlinks=False)
            if count != before.st_size or pin(before) != pin(after) or pin(before) != pin(public) or (
                    parent_before.st_dev, parent_before.st_ino) != (parent_after.st_dev, parent_after.st_ino):
                raise ValueError("probe source changed: HOLD")
            os.fchmod(target, 0o555)
            os.fsync(target)
        finally:
            os.close(target)
        return name
    except BaseException:
        if name is not None:
            os.unlink(name)
        raise
    finally:
        os.close(fd)
        os.close(parent)

def mount_id(fd):
    with open("/proc/self/fdinfo/" + str(fd)) as f:
        raw = f.read(4097)
    if len(raw) > 4096:
        raise ValueError("probe mount identity unavailable: HOLD")
    ids = [line.split(":", 1)[1].strip() for line in raw.splitlines() if line.startswith("mnt_id:")]
    if len(ids) != 1 or not ids[0].isdigit():
        raise ValueError("probe mount identity unavailable: HOLD")
    return ids[0]

def output_limit():
    resource.setrlimit(resource.RLIMIT_FSIZE, (4096, 4096))

def stopped(unit):
    p = subprocess.run(["/usr/bin/systemctl", "show", "--property=LoadState", "--property=ActiveState",
                        "--property=ControlGroup", "--property=Job", "--", unit], capture_output=True, timeout=3, env=ENV)
    if p.returncode or len(p.stdout) > 4096:
        return False
    fields = dict(line.split("=", 1) for line in p.stdout.decode().splitlines())
    if len(fields) != 4 or fields.get("Job") not in ("", "0"):
        return False
    if fields.get("LoadState") not in ("loaded", "not-found") or fields.get("ActiveState") not in ("inactive", "failed"):
        return False
    group = fields.get("ControlGroup")
    if group not in ("", "/system.slice/" + unit):
        return False
    events = "/sys/fs/cgroup/system.slice/" + unit + "/cgroup.events"
    try:
        with open(events) as f:
            lines = f.read(4097).splitlines()
        return "populated 0" in lines and "populated 1" not in lines
    except FileNotFoundError:
        return True

def probe(source, operation):
    for directory in (os.path.dirname(PROBE_ROOT), PROBE_ROOT):
        if directory == PROBE_ROOT:
            try:
                os.mkdir(directory, 0o700)
            except FileExistsError:
                pass
        s = os.lstat(directory)
        private = directory == PROBE_ROOT
        if not stat.S_ISDIR(s.st_mode) or s.st_uid != 0 or s.st_mode & 0o022 or (private and stat.S_IMODE(s.st_mode) != 0o700) or os.path.realpath(directory) != directory:
            raise ValueError("probe private storage unverified: HOLD")
    fd = os.open(PROBE_ROOT + "/.lock", os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600)
    try:
        s = os.fstat(fd)
        if not stat.S_ISREG(s.st_mode) or s.st_uid != 0 or stat.S_IMODE(s.st_mode) != 0o600 or s.st_nlink != 1:
            raise ValueError("probe admission lock unverified: HOLD")
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        return run_probe(source, operation)
    finally:
        os.close(fd)

def run_probe(source, operation):
    image = stage_image(source, PROBE_ROOT)
    unit = "mcpshka-probe-" + os.urandom(8).hex() + ".service"
    fenced = False
    try:
        try:
            pwd.getpwnam(unit.removesuffix(".service"))
        except KeyError:
            pass
        else:
            fenced = True
            raise ValueError("probe identity already exists: HOLD")
        with tempfile.TemporaryFile(dir=PROBE_ROOT) as output:
            try:
                p = subprocess.run(unit_args(unit, image, operation), stdin=subprocess.DEVNULL, stdout=output,
                                   stderr=subprocess.DEVNULL, timeout=10, env=ENV, preexec_fn=output_limit)
            except subprocess.TimeoutExpired:
                # A lost launch reply never proves absence. Preserve the image
                # even if the best-effort stop observes no unit yet.
                subprocess.run(["/usr/bin/systemctl", "stop", "--", unit], timeout=3, env=ENV,
                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                raise
            else:
                if not stopped(unit):
                    subprocess.run(["/usr/bin/systemctl", "stop", "--", unit], timeout=3, env=ENV,
                                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                fenced = p.returncode == 0 and stopped(unit)
            if not fenced:
                raise ValueError("probe fencing unverified: HOLD")
            output.seek(0)
            body = output.read(4097)
            if p.returncode or len(body) > 256:
                raise ValueError("probe failed or exceeded output budget: HOLD")
            text = body.decode("ascii").strip()
            if (operation == "version" and not re.fullmatch(r"mcpshka [A-Za-z0-9][A-Za-z0-9._-]{0,63}", text)) or (
                    operation == "state-store-support" and text != "sqlite-v1"):
                raise ValueError("unexpected probe response: HOLD")
            return text
    finally:
        if fenced:
            os.unlink(image)

if __name__ == "__main__":
    try:
        if os.geteuid() != 0 or len(sys.argv) != 3 or not allowed_source(sys.argv[1]) or sys.argv[2] not in ("version", "state-store-support"):
            raise ValueError("fixed root probe scope required: HOLD")
        print(probe(sys.argv[1], sys.argv[2]))
    except (OSError, ValueError, subprocess.SubprocessError) as exc:
        print(str(exc), file=sys.stderr)
        sys.exit(1)
