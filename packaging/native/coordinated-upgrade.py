#!/usr/bin/python3 -I
"""One-shot, independently root-authorized paired MCPshka and broker upgrade.

No generic root shell, service arguments, network mutations or auto-unlock.
Version+SHA-pinned images and current hashes, durable intent, backups, rollback.
"""
import argparse
import fcntl
import hashlib
import json
import os
import pwd
from pathlib import Path
import re
import stat
import subprocess
import sys
import tempfile
import time
import traceback
import urllib.request

BIN = Path("/root/repos/mcpshka/bin")
PLAN = BIN / "mcpshka-coordinated-release.json"
TARGETS = (Path("/usr/local/bin/mcpshka"), Path("/usr/local/libexec/mcpshka-broker"))
SERVICES = ("mcpshka-broker.service", "mcpshka.service", "mcpshka-tunnel.service")
# Release-owned drop-in only: never replace the owner's broker.json or
# other systemd overrides. Existing bytes are journalled for exact rollback.
BROKER_RESOURCES = Path("/etc/systemd/system/mcpshka-broker.service.d/90-mcpshka-release-resources.conf")
BROKER_RESOURCE_TEXT = b"[Service]\nCPUQuota=100%\nCPUWeight=400\nIOWeight=400\nTasksMax=96\n"
BROKER_RESOURCE_SHOW = {"CPUQuotaPerSecUSec": "1s", "CPUWeight": "400", "IOWeight": "400", "TasksMax": "96"}

ROOT = Path("/var/lib/mcpshka-executor")
AUDIT = ROOT / "coordinated-upgrades"
LOCK = Path("/run/mcpshka-self-deploy.lock")
HELPER_TARGET = Path("/usr/local/libexec/mcpshka-coordinated-upgrade")
GATE_TARGET = Path("/usr/local/libexec/mcpshka-self-deploy")
GUARDED_TARGET = Path("/usr/local/libexec/mcpshka-self-deploy-guarded")
GATE_SOURCE = Path("/root/repos/mcpshka/packaging/native/release-gate.py")
GUARDED_SOURCE = Path("/root/repos/mcpshka/packaging/native/self-deploy.sh")
FIELDS = {"schema", "operation_id", "version", "current_version", "ci_run", "source_tree",
          "controller_sha256", "broker_sha256", "current_controller_sha256",
          "current_broker_sha256", "controller_candidate", "broker_candidate", "helper_sha256",
          "gate_sha256", "guarded_helper_sha256", "current_gate_sha256", "current_guarded_sha256"}

def sha(v):
    return hashlib.sha256(v).hexdigest()

def parse(v):
    def unique(pairs):
        d = {}
        for k, x in pairs:
            if k in d:
                raise ValueError("duplicate field: HOLD")
            d[k] = x
        return d
    return json.loads(v, object_pairs_hook=unique)

def read(p, owner=None, limit=128 << 20):
    fd = os.open(p, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        a = os.fstat(fd)
        if not stat.S_ISREG(a.st_mode) or a.st_nlink != 1 or not 0 < a.st_size <= limit or a.st_mode & 0o002 or (owner is not None and (a.st_uid != owner or a.st_mode & 0o022)):
            raise ValueError("unsafe binary/manifest: HOLD")
        b = os.read(fd, limit + 1)
        c = os.fstat(fd)
        if len(b) != a.st_size or (a.st_dev, a.st_ino, a.st.mtime_ns if False else a.st_mtime_ns, a.st_ctime_ns) != (c.st_dev, c.st_ino, c.st_mtime_ns, c.st_ctime_ns):
            raise ValueError("changed input: HOLD")
        return b
    finally:
        os.close(fd)

def validate(p):
    if not isinstance(p, dict) or set(p) != FIELDS or p["schema"] != "mcpshka.coordinated-upgrade/v1":
        raise ValueError("invalid fixed plan")
    if not re.fullmatch("[a-f0-9]{32}", p["operation_id"]) or not re.fullmatch(r"0\.[0-9]{1,3}\.[0-9]{1,6}", p["version"]) or not re.fullmatch(r"0\.[0-9]{1,3}\.[0-9]{1,6}", p["current_version"]) or p["version"] == p["current_version"]:
        raise ValueError("invalid operation/version")
    if type(p["ci_run"]) is not int or p["ci_run"] < 0 or not re.fullmatch("[a-f0-9]{40}", p["source_tree"]):
        raise ValueError("unverified source")
    for name in ("controller_sha256", "broker_sha256", "current_controller_sha256", "current_broker_sha256"):
        if not isinstance(p[name], str) or not re.fullmatch("[a-f0-9]{64}", p[name]):
            raise ValueError("invalid binary pin")
    for name in ("helper_sha256","gate_sha256","guarded_helper_sha256","current_gate_sha256","current_guarded_sha256"):
        if not isinstance(p[name], str) or not re.fullmatch("[a-f0-9]{64}", p[name]):
            raise ValueError("untrusted root helper pin")
    for kind in ("controller", "broker"):
        name = "mcpshka-" + ("broker-" if kind == "broker" else "") + p["version"] + "." + p[kind + "_sha256"][:12] + ".candidate"
        if p[kind + "_candidate"] != str(BIN / name):
            raise ValueError("unpinned image path")
    return p

def root_dir(p):
    st = p.lstat()
    if not stat.S_ISDIR(st.st_mode) or st.st_uid != 0 or stat.S_IMODE(st.st_mode) != 0o700 or p.resolve() != p:
        raise ValueError("unsafe root-owned state: HOLD")

def prepare_heavy_queue():
    # Shared scheduling state is not privileged executor authority. setgid
    # keeps root workers and the controller on the same kernel-lock queue.
    parent = os.open("/var/lib/mcpshka", os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        try:
            os.mkdir("heavy-queue", 0o2770, dir_fd=parent)
        except FileExistsError:
            pass
        fd = os.open("heavy-queue", os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=parent)
        try:
            st = os.fstat(fd)
            # Resolve the service account by name: chown to gid 994 (the uid
            # number) handed the queue to an unrelated group, so root agents'
            # tickets were unreadable and every job of theirs was killed
            # (BUG-20261024-93).
            account = pwd.getpwnam("mcpshka")
            if st.st_uid not in (0, account.pw_uid) or st.st_mode & 0o007:
                raise ValueError("unsafe heavy queue directory: HOLD")
            os.fchown(fd, account.pw_uid, account.pw_gid)
            os.fchmod(fd, 0o2770)
        finally:
            os.close(fd)
    finally:
        os.close(parent)


def schedule_cleanup():
    # A failed maintenance schedule never rolls back a healthy release;
    # the broker's independent periodic sweep is the fallback.
    try:
        subprocess.run(("/usr/bin/systemd-run", "--quiet", "--collect",
            "--unit=mcpshka-host-cleanup", "--on-active=30s",
            "--property=RuntimeMaxSec=90s", "--property=MemoryMax=256M",
            "--property=CPUQuota=25%", "--property=Nice=19",
            "--property=IOSchedulingClass=idle", "/usr/local/libexec/mcpshka-broker",
            "host-cleanup-worker"), stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL, timeout=5, check=False,
            env={"PATH":"/usr/bin:/bin", "LANG":"C"})
    except (OSError, subprocess.TimeoutExpired):
        pass

def sync_dir(p):
    fd = os.open(p, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)

def create(p, b):
    fd = os.open(p, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd,"wb") as f:
        f.write(b)
        f.flush()
        os.fchmod(f.fileno(),0o600)
        os.fsync(f.fileno())
    sync_dir(p.parent)

def record(p, state, **details):
    when = str(time.time_ns())
    create(p / (when + "-" + state + ".json"), json.dumps({"schema":"mcpshka.paired-upgrade-audit/v1","state":state,"at_ns":when, **details},sort_keys=True).encode())

def command(*args):
    proc = subprocess.run(args, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30,
                          env={"PATH":"/usr/bin:/bin","LANG":"C","LC_ALL":"C"})
    if proc.returncode or len(proc.stdout)>4096:
        raise RuntimeError("fixed operation failed: " + repr(args) + " exit=" + str(proc.returncode) +
                           " stderr=" + proc.stderr[:2048].decode("utf-8", "replace") +
                           " stdout=" + proc.stdout[:1024].decode("utf-8", "replace"))
    if proc.stderr:
        print(proc.stderr[:4096].decode("utf-8", "replace"), file=sys.stderr)
    return proc.stdout

def active(unit):
    try:
        command("/usr/bin/systemctl","is-active","--quiet",unit)
        return True
    except RuntimeError:
        return False

def health(version):
    for i in range(35):
        try:
            if not all(active(x) for x in SERVICES):
                raise ValueError("critical service stopped")
            opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
            with opener.open("http://127.0.0.1:8765/healthz",timeout=2) as f:
                h=parse(f.read(2048))
            with opener.open("http://127.0.0.1:8768/readyz",timeout=2) as f:
                ready=f.read(32)
            if h.get("version")==version and h.get("status")=="ok" and ready.strip()==b"ready":
                return
        except (OSError,ValueError,RuntimeError,subprocess.TimeoutExpired):
            time.sleep(1)
    raise RuntimeError("controller/tunnel health timed out")

def rollback_reference():
    return TARGETS[0].with_name(TARGETS[0].name + ".previous")

def verify_installed(plan):
    # Use the same fixed production verifier as project_verify. Run the exact
    # installed controller as its service account, under the rollback lock.
    # No MCPSHKA_PRIVILEGED_BROKER: the broker admits only the MainPID of
    # mcpshka.service, so a runuser child is always reset (MCP-275). As the
    # unprivileged service user the verifier probes binary versions directly.
    proc = subprocess.run(
        ("/usr/sbin/runuser", "--user", "mcpshka", "--",
         str(TARGETS[0]), "verify-production"),
        stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        timeout=100, check=False,
        env={"PATH":"/usr/bin:/bin", "LANG":"C", "LC_ALL":"C"})
    if len(proc.stdout) > 16384:
        raise RuntimeError("installed production verifier output exceeds limit")
    if proc.returncode:
        raise RuntimeError("installed production verifier failed (exit " + str(proc.returncode) +
                           ")" + failed_checks(proc.stdout))
    report = parse(proc.stdout)
    if (not isinstance(report, dict) or report.get("ok") is not True or
            report.get("installed_version") != plan["version"] or
            report.get("rollback_version") != plan["current_version"]):
        raise RuntimeError("installed production verifier did not prove the installed and rollback versions")
    checks = report.get("checks")
    if not isinstance(checks, list) or not checks:
        raise ValueError("installed verifier has no check results")
    return [check["name"] for check in checks if check["ok"] is True]

def failed_checks(stdout):
    # Name failing checks in the journal so a rollback is never silent.
    try:
        checks = parse(stdout).get("checks") or []
        bad = ["%s: %s" % (str(c.get("name"))[:40], str(c.get("detail"))[:160])
               for c in checks if isinstance(c, dict) and c.get("ok") is not True]
    except Exception:
        return ""
    return ("; failed: " + " | ".join(bad[:6])) if bad else ""

def replace(target, data):
    fd, tmp = tempfile.mkstemp(prefix=".mcpshka-pair-",dir=target.parent)
    try:
        with os.fdopen(fd,"wb") as f:
            f.write(data)
            f.flush()
            os.fchmod(f.fileno(),0o755)
            os.fsync(f.fileno())
        os.replace(tmp,target)
        sync_dir(target.parent)
    finally:
        if os.path.exists(tmp):
            os.unlink(tmp)

def broker_resource_parent():
    parent = BROKER_RESOURCES.parent
    base = parent.parent
    for directory in (base,):
        st = os.lstat(directory)
        if not stat.S_ISDIR(st.st_mode) or st.st_uid != 0 or st.st_mode & 0o022:
            raise ValueError("unsafe broker systemd directory: HOLD")
    try:
        st = os.lstat(parent)
    except FileNotFoundError:
        return False
    if not stat.S_ISDIR(st.st_mode) or st.st_uid != 0 or st.st_mode & 0o022:
        raise ValueError("unsafe broker resource drop-in directory: HOLD")
    return True


def backup_broker_resources(work):
    broker_resource_parent()
    try:
        old = read(BROKER_RESOURCES, owner=0, limit=4096)
    except FileNotFoundError:
        create(work / "broker.resources.absent", b"absent")
    else:
        create(work / "broker.resources.previous", old)


def replace_broker_resources(data):
    if not broker_resource_parent():
        os.mkdir(BROKER_RESOURCES.parent, 0o755)
        if not broker_resource_parent():
            raise ValueError("failed to prepare broker unit drop-in: HOLD")
    fd, tmp = tempfile.mkstemp(prefix=".mcpshka-resources-", dir=BROKER_RESOURCES.parent)
    try:
        with os.fdopen(fd, "wb") as f:
            f.write(data)
            f.flush()
            os.fchmod(f.fileno(), 0o644)
            os.fsync(f.fileno())
        os.replace(tmp, BROKER_RESOURCES)
        sync_dir(BROKER_RESOURCES.parent)
    finally:
        if os.path.exists(tmp):
            os.unlink(tmp)


def set_broker_resources():
    # Never write the source unit, the privileged broker's JSON config, or
    # any other operator drop-in. PID1 reload happens while services are fenced.
    replace_broker_resources(BROKER_RESOURCE_TEXT)
    command("/usr/bin/systemctl", "daemon-reload")


def restore_broker_resources(work):
    was_absent = (work / "broker.resources.absent").is_file()
    old = None if was_absent else read(work / "broker.resources.previous", owner=0, limit=4096)
    try:
        current = read(BROKER_RESOURCES, owner=0, limit=4096)
    except FileNotFoundError:
        current = None
    if current not in (BROKER_RESOURCE_TEXT, old):
        raise ValueError("foreign broker resource change: HOLD")
    if old is None and current is not None:
        os.unlink(BROKER_RESOURCES)
        sync_dir(BROKER_RESOURCES.parent)
    elif old is not None and current != old:
        replace_broker_resources(old)
    command("/usr/bin/systemctl", "daemon-reload")


def broker_resources_healthy():
    out = command("/usr/bin/systemctl", "show", "--property=" + ",".join(BROKER_RESOURCE_SHOW), SERVICES[0])
    lines = out.decode("ascii", "strict").splitlines()
    observed = {}
    for line in lines:
        key, separator, value = line.partition("=")
        if not separator or key in observed:
            return False
        observed[key] = value
    return observed == BROKER_RESOURCE_SHOW


def validate_images(p):
    images=tuple(read(Path(p[k+"_candidate"])) for k in ("controller","broker"))
    old=tuple(read(t,owner=0) for t in TARGETS)
    expected=(p["controller_sha256"],p["broker_sha256"],p["current_controller_sha256"],p["current_broker_sha256"])
    if tuple(map(sha,images+old))!=expected:
        raise ValueError("current or candidate SHA changed: HOLD")
    return images,old

def safe_deploy_lock(st, uid=0):
    return stat.S_ISREG(st.st_mode) and st.st_uid == uid and st.st_nlink == 1 and not st.st_mode & 0o022

def running_broker_hash():
    # PID comes from PID1. The procfs executable link is inspected explicitly;
    # ordinary pinned file inputs still require O_NOFOLLOW.
    pid = int(command("/usr/bin/systemctl", "show", "--value", "--property=MainPID", SERVICES[0]).strip())
    if pid <= 0:
        raise ValueError("root broker process unavailable")
    proc = Path("/proc") / str(pid) / "exe"
    if not os.readlink(proc).startswith(str(TARGETS[1])):
        raise ValueError("broker process image differs from target")
    fd = os.open(proc, os.O_RDONLY | os.O_CLOEXEC)
    try:
        st = os.fstat(fd)
        if not stat.S_ISREG(st.st_mode) or st.st_size > 128 << 20:
            raise ValueError("broker process image invalid")
        chunks=[]
        while True:
            chunk=os.read(fd,65536)
            if not chunk: break
            chunks.append(chunk)
            if sum(map(len,chunks)) > 128 << 20:
                raise ValueError("broker image too large")
        return sha(b"".join(chunks))
    finally:
        os.close(fd)

def upgrade(check_only):
    if os.geteuid()!=0:
        raise ValueError("independent human root authorization required")
    p=validate(parse(read(PLAN,limit=16384)))
    if not re.fullmatch(r"0\.[0-9]{1,3}\.[0-9]{1,6}", p["version"]) or not str(PLAN).endswith("mcpshka-coordinated-release-" + p["version"] + ".json"):
        raise ValueError("manifest version differs from invocation: HOLD")
    if sha(read(Path(__file__))) != p["helper_sha256"]:
        raise ValueError("coordinated root updater digest differs from approved plan")
    root_dir(ROOT)
    fs=os.statvfs(ROOT)
    if fs.f_bavail*fs.f_frsize < max(3<<30,fs.f_blocks*fs.f_frsize//10)+(512<<20):
        raise ValueError("insufficient rollback disk reserve: HOLD")
    fd=os.open(LOCK,os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600)
    task_fd=None
    try:
        st=os.fstat(fd)
        if not safe_deploy_lock(st):
            raise ValueError("unsafe deploy lock")
        fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
        task_fd=os.open(ROOT/"ha-automations.lock",os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600)
        if not safe_deploy_lock(os.fstat(task_fd)):
            raise ValueError("untrusted privileged task exclusion lock: HOLD")
        fcntl.flock(task_fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
        images,old=validate_images(p)
        gate_candidate=read(GATE_SOURCE)
        guarded_candidate=read(GUARDED_SOURCE)
        previous_gate=read(GATE_TARGET,owner=0)
        previous_guarded=read(GUARDED_TARGET,owner=0)
        if (sha(gate_candidate),sha(guarded_candidate),sha(previous_gate),sha(previous_guarded)) != (
            p["gate_sha256"],p["guarded_helper_sha256"],p["current_gate_sha256"],p["current_guarded_sha256"]):
            raise ValueError("release authority helper/source hash mismatch: HOLD")
        if not all(active(x) for x in SERVICES):
            raise ValueError("critical service not running")
        if check_only:
            return {"state":"READY","operation_id":p["operation_id"],"version":p["version"]}
        AUDIT.mkdir(mode=0o700,exist_ok=True)
        root_dir(AUDIT)
        work=AUDIT/p["operation_id"]
        work.mkdir(mode=0o700) # replay or UNKNOWN must fail
        sync_dir(AUDIT)
        record(work,"PREPARED")
        create(work/"controller.previous",old[0])
        create(work/"broker.previous",old[1])
        try:
            prior_reference = read(rollback_reference(), owner=0)
        except FileNotFoundError:
            pass
        else:
            create(work/"controller.prior-rollback", prior_reference)
        create(work/"gate.previous",previous_gate)
        create(work/"guarded.previous",previous_guarded)
        backup_broker_resources(work)
        record(work,"BACKED_UP")
        if validate_images(p)[0]!=images:
            raise ValueError("candidate changed since backup")
        # One-time physical-root bootstrap: seal the exact reviewed helper before
        # service transition. Future broker admin sessions execute only this copy.
        helper_target=HELPER_TARGET
        try:
            if sha(read(helper_target,owner=0)) != p["helper_sha256"]:
                raise ValueError("installed root helper differs: HOLD")
        except FileNotFoundError:
            replace(helper_target,read(Path(__file__)))
            if sha(read(helper_target,owner=0)) != p["helper_sha256"]:
                raise ValueError("root helper installation failed: HOLD")
        prepare_heavy_queue()
        record(work,"HELPER_SEALED")
        swapped=False
        home_priority_started=False
        try:
            for s in reversed(SERVICES):
                command("/usr/bin/systemctl","stop",s)
            if any(active(s) for s in SERVICES):
                raise RuntimeError("old writer still live")
            record(work,"FENCED")
            record(work,"SWAPPING")
            swapped=True
            replace(GATE_TARGET,gate_candidate)
            replace(GUARDED_TARGET,guarded_candidate)
            replace(TARGETS[1],images[1])
            replace(TARGETS[0],images[0])
            set_broker_resources()
            if sha(read(TARGETS[1],owner=0)) != p["broker_sha256"]:
                raise ValueError("foreign broker before priority installation: HOLD")
            home_priority_started=True
            command(str(TARGETS[1]), "home-priority", "--startup")
            for s in SERVICES:
                command("/usr/bin/systemctl","start",s)
            health(p["version"])
            if running_broker_hash() != p["broker_sha256"]:
                raise RuntimeError("running broker executable hash mismatch")
            if not broker_resources_healthy():
                raise RuntimeError("running broker resource limits differ from release")
            if (sha(read(TARGETS[1],owner=0))!=p["broker_sha256"] or
                    sha(read(TARGETS[0],owner=0))!=p["controller_sha256"] or
                    sha(read(GATE_TARGET,owner=0))!=p["gate_sha256"] or
                    sha(read(GUARDED_TARGET,owner=0))!=p["guarded_helper_sha256"]):
                raise RuntimeError("installed controller/broker/gate/helper checksum mismatch")
            replace(rollback_reference(), old[0])
            checks = verify_installed(p)
            record(work,"SUCCEEDED", smoke_checks=checks)
            schedule_cleanup()
            return {"state":"SUCCEEDED","version":p["version"],"operation_id":p["operation_id"]}
        except BaseException as exc:
            failure = failure_details(exc)
            try:
                record(work, "APPLY_FAILED", **failure)
            finally:
                errors = rollback(work, p, swapped, home_priority_started)
            record(work, "ROLLBACK_FAILED" if errors else "ROLLED_BACK",
                   **failure, rollback_errors=errors)
            if errors:
                raise RuntimeError("rollback UNKNOWN; keep journal and inspect root host: " +
                                   failure["error"] + "; " + str(errors)) from exc
            raise RuntimeError("paired upgrade failed; rollback verified: " + failure["error"]) from exc
    finally:
        if task_fd is not None: os.close(task_fd)
        os.close(fd)



def failure_details(exc):
    return {"error": (type(exc).__name__ + ": " + str(exc))[:2048],
            "traceback": "".join(traceback.format_exception(type(exc), exc, exc.__traceback__))[-2048:]}


def rollback(work, plan, swapped, home_priority_started):
    errors = []
    def attempt(step, fn):
        try:
            fn()
            return True
        except BaseException as exc:
            errors.append({"step": step, **failure_details(exc)})
            return False

    def restore(target, backup, current, previous):
        if sha(read(target, owner=0)) not in (current, previous):
            raise ValueError("foreign installed image/helper: HOLD")
        saved = read(work / backup, owner=0)
        if sha(saved) != previous:
            raise ValueError("untrusted rollback backup: HOLD")
        replace(target, saved)

    def restore_reference():
        saved_path = work/"controller.prior-rollback"
        try:
            saved = read(saved_path, owner=0)
        except FileNotFoundError:
            return
        current = read(rollback_reference(), owner=0)
        if sha(current) != plan["current_controller_sha256"] and current != saved:
            raise ValueError("rollback reference changed independently: HOLD")
        if current != saved:
            replace(rollback_reference(), saved)

    def priority():
        if sha(read(TARGETS[1], owner=0)) not in (plan["broker_sha256"], plan["current_broker_sha256"]):
            raise ValueError("foreign broker before priority rollback: HOLD")
        command(str(TARGETS[1]), "home-priority", "--rollback")

    def fence():
        if any(active(service) for service in SERVICES):
            raise RuntimeError("writer still live: HOLD")

    try:
        for service in reversed(SERVICES):
            attempt("stop " + service, lambda service=service: command("/usr/bin/systemctl", "stop", service))
        fenced = attempt("fence", fence)
        if home_priority_started:
            attempt("home-priority rollback", priority)
        if swapped and fenced:
            for target, backup, key, old_key in ((TARGETS[0], "controller.previous", "controller", "controller"),
                                                 (TARGETS[1], "broker.previous", "broker", "broker"),
                                                 (GATE_TARGET, "gate.previous", "gate", "gate"),
                                                 (GUARDED_TARGET, "guarded.previous", "guarded_helper", "guarded")):
                attempt("restore " + backup, lambda target=target, backup=backup, key=key, old_key=old_key:
                        restore(target, backup, plan[key + "_sha256"], plan["current_" + old_key + "_sha256"]))
            attempt("restore broker resources", lambda: restore_broker_resources(work))
            attempt("restore rollback reference", restore_reference)
    finally:
        # Every start is independent: a failed policy/image/start must not strand the other services.
        for service in SERVICES:
            attempt("start " + service, lambda service=service: command("/usr/bin/systemctl", "start", service))
    attempt("rollback health", lambda: health(plan["current_version"]))
    def broker_identity():
        if running_broker_hash() != plan["current_broker_sha256"]:
            raise RuntimeError("rollback broker executable hash mismatch")
    attempt("rollback broker identity", broker_identity)
    return errors


def journal_status(plan):
    opdir = AUDIT / plan["operation_id"]
    if not opdir.exists():
        return {"state":"NOT_STARTED","operation_id":plan["operation_id"]}
    root_dir(AUDIT)
    root_dir(opdir)
    entries = sorted(opdir.glob("*.json"))
    if not entries or len(entries) > 128:
        raise ValueError("unknown or oversized root recovery history: HOLD")
    last = entries[-1]
    event = parse(read(last,owner=0,limit=65536))
    if event.get("schema") != "mcpshka.paired-upgrade-audit/v1" or not isinstance(event.get("state"),str):
        raise ValueError("corrupt immutable maintenance journal: HOLD")
    state = event["state"]
    observed = [sha(read(target,owner=0)) for target in TARGETS + (GATE_TARGET,GUARDED_TARGET)]
    expected_new = [plan["controller_sha256"],plan["broker_sha256"],plan["gate_sha256"],plan["guarded_helper_sha256"]]
    expected_old = [plan["current_controller_sha256"],plan["current_broker_sha256"],plan["current_gate_sha256"],plan["current_guarded_sha256"]]
    if state == "SUCCEEDED" and observed == expected_new and broker_resources_healthy():
        return retention_status({"state":"SUCCEEDED","version":plan["version"],"operation_id":plan["operation_id"]}, plan)
    if state == "ROLLED_BACK" and observed == expected_old:
        return {"state":"ROLLED_BACK","version":plan["current_version"],"operation_id":plan["operation_id"]}
    return {"state":"UNKNOWN_HOLD","last_phase":state,"observed_sha256":observed,
            "operation_id":plan["operation_id"],"error":event.get("error", ""),
            "traceback":event.get("traceback", ""),"rollback_errors":event.get("rollback_errors", []),
            "next_action":"independent_root_reconcile_or_explicit_rollback"}


def explicit_rollback():
    if os.geteuid() != 0:
        raise ValueError("root operator required")
    plan = validate(parse(read(PLAN,limit=16384)))
    if sha(read(Path(__file__))) != plan["helper_sha256"]:
        raise ValueError("operator helper identity changed: HOLD")
    root_dir(ROOT)
    fd = os.open(LOCK, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    task_fd=None
    try:
        st = os.fstat(fd)
        if not safe_deploy_lock(st):
            raise ValueError("unsafe rollback admission lock")
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        task_fd=os.open(ROOT/"ha-automations.lock",os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600)
        if not safe_deploy_lock(os.fstat(task_fd)):
            raise ValueError("unsafe privileged rollback exclusion lock: HOLD")
        fcntl.flock(task_fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
        state = journal_status(plan)
        if state["state"] == "ROLLED_BACK":
            return state
        if state["state"] not in ("SUCCEEDED","UNKNOWN_HOLD"):
            raise ValueError("cannot rollback before a journalled swap: HOLD")
        opdir = AUDIT / plan["operation_id"]
        past = [read(opdir / "controller.previous",owner=0),
                read(opdir / "broker.previous",owner=0)]
        root_past=[read(opdir/"gate.previous",owner=0),read(opdir/"guarded.previous",owner=0)]
        if [sha(x) for x in root_past] != [plan["current_gate_sha256"],plan["current_guarded_sha256"]]:
            raise ValueError("untrusted root helper backup")
        if [sha(x) for x in past] != [plan["current_controller_sha256"],plan["current_broker_sha256"]]:
            raise ValueError("rollback images do not match immutable pins: HOLD")
        for i in (0,1):
            observed = sha(read(TARGETS[i],owner=0))
            if observed not in ((plan["current_controller_sha256"],plan["controller_sha256"]) if i==0
                                else (plan["current_broker_sha256"],plan["broker_sha256"])):
                raise ValueError("foreign installed binary: HOLD")
        record(opdir,"ROLLBACK_INTENT")
        errors = rollback(opdir, plan, True, True)
        record(opdir, "ROLLBACK_FAILED" if errors else "ROLLED_BACK", rollback_errors=errors)
        if errors:
            raise RuntimeError("rollback status UNKNOWN; preserve evidence and inspect host: " + str(errors))
        return journal_status(plan)
    finally:
        if task_fd is not None: os.close(task_fd)
        os.close(fd)


def retain_after_success(result, keep_gates=6, keep_versions=2, dry_run=False):
    if result.get("state") != "SUCCEEDED":
        return result
    # Called after upgrade() releases both locks. Each fixed worker rechecks
    # success and installed/rollback pins under the deployment lock.
    cleanup = {"dry_run": dry_run}
    gates = ("/usr/bin/python3", "-I", "-S", str(GATE_TARGET), "retain-gates",
             "--version", result["version"], "--keep", str(keep_gates))
    if dry_run:
        gates += ("--dry-run",)
    commands = (("gates", gates),
                ("bin", (str(TARGETS[1]), "release-cleanup-worker",
                         "dry-run" if dry_run else "apply", str(keep_versions))))
    for name, args in commands:
        try:
            completed = subprocess.run(args, stdin=subprocess.DEVNULL,
                stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=70,
                env={"PATH":"/usr/bin:/bin", "LANG":"C", "LC_ALL":"C"})
            if len(completed.stdout) > 8192:
                raise ValueError("retention worker held")
            report = parse(completed.stdout)
            if (not isinstance(report, dict) or
                    any(type(report.get(k)) is not int or report[k] < 0
                        for k in ("deleted", "deleted_bytes"))):
                raise ValueError("invalid retention report")
            if completed.returncode:
                report["state"] = "HELD"
            cleanup[name] = report
        except (OSError, ValueError, subprocess.SubprocessError):
            cleanup[name] = {"state":"HELD", "deleted":None, "deleted_bytes":None,
                             "next_action":"inspect_release_journal"}
    result["cleanup"] = cleanup
    try:
        create(AUDIT / result["operation_id"] / "retention-report",
               json.dumps(cleanup, sort_keys=True).encode())
    except OSError:
        cleanup["audit"] = "UNAVAILABLE"
    print("release retention: " + json.dumps(cleanup, sort_keys=True), file=sys.stderr)
    return result


def retention_status(result, plan):
    report = AUDIT / plan["operation_id"] / "retention-report"
    try:
        result["cleanup"] = parse(read(report, owner=0, limit=16384))
    except FileNotFoundError:
        pass
    except (OSError, ValueError):
        result["cleanup"] = {"state":"HELD", "next_action":"inspect_release_journal"}
    return result


if __name__=="__main__":
    ap=argparse.ArgumentParser()
    a=ap.add_mutually_exclusive_group(required=True)
    a.add_argument("--check",action="store_true")
    a.add_argument("--apply",action="store_true")
    a.add_argument("--status",action="store_true")
    a.add_argument("--rollback",action="store_true")
    ap.add_argument("--keep-gates", type=int, default=6)
    ap.add_argument("--keep-versions", type=int, default=2)
    ap.add_argument("--retention-dry-run", action="store_true")
    ap.add_argument("--plan-sha256", default="", help="Pinned plan digest for Face ID broker invocation")
    ap.add_argument("--session-id", default="")
    ap.add_argument("--owner", default="")
    ap.add_argument("--generation", type=int, default=0)
    ap.add_argument("--version", required=True, help="Exact reviewed 0.1.N release version")
    args=ap.parse_args()
    if not 1 <= args.keep_gates <= 100 or not 1 <= args.keep_versions <= 100:
        ap.error("retention counts must be 1..100")
    if not re.fullmatch(r"0\.[0-9]{1,3}\.[0-9]{1,6}", args.version):
        ap.error("invalid version")
    PLAN = BIN / ("mcpshka-coordinated-release-" + args.version + ".json")
    globals()["PLAN"] = PLAN
    if args.plan_sha256:
        if (not re.fullmatch("[a-f0-9]{64}", args.plan_sha256) or
                sha(read(PLAN,limit=16384)) != args.plan_sha256 or
                not re.fullmatch("[a-f0-9]{32}",args.session_id) or
                not re.fullmatch("[a-zA-Z0-9_.-]{1,64}",args.owner) or
                args.generation <= 0):
            raise ValueError("invalid Face ID-pinned upgrade request")
        session=Path("/var/lib/mcpshka-executor/admin-session-current.json")
        current=parse(read(session, owner=0, limit=4096))
        if (current.get("state")!="active" or current.get("id")!=args.session_id or
                current.get("owner")!=args.owner or
                current.get("generation")!=args.generation or
                current.get("policy")!="mcpshka-administration/v1" or
                current.get("auth_method")!="webauthn"):
            raise ValueError("Face ID administration revoked or superseded: HOLD")
    elif args.session_id or args.owner or args.generation:
        raise ValueError("incomplete administrative operation")
    if args.status:
        if os.geteuid()!=0: raise ValueError("root only status")
        info=validate(parse(read(PLAN,limit=16384)))
        print(json.dumps(journal_status(info),sort_keys=True))
    elif args.rollback:
        print(json.dumps(explicit_rollback(),sort_keys=True))
    else:
        result = upgrade(args.check)
        result = retain_after_success(result, args.keep_gates, args.keep_versions, args.retention_dry_run)
        print(json.dumps(result,sort_keys=True))
