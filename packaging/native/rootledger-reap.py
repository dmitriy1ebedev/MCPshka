#!/usr/bin/python3
"""Pinned RootLedger maintenance: isolated smoke, app-only reap, guarded rollback."""
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import sys
import time
import urllib.request

ROOT = Path("/root/rootledger/releases/0.3.a.10")
CURRENT = Path("/root/rootledger/current")
STATE = Path("/var/lib/mcpshka-rootledger-reap")
APP = "rootledger-app-1"
HEALTH = ["CMD", "python", "-c", "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8000/api/health', timeout=3).read()"]
ENV = {"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG": "C.UTF-8"}
SMOKE = r"""
import os, time
r,w=os.pipe()
p=os.fork()
if p==0:
    os.close(r)
    g=os.fork()
    if g==0:
        os.close(w)
        time.sleep(.2)
        os._exit(0)
    os.write(w,str(g).encode())
    os.close(w)
    os._exit(0)
os.close(w)
g=int(os.read(r,64))
os.close(r)
os.waitpid(p,0)
deadline=time.monotonic()+4
while os.path.exists('/proc/'+str(g)) and time.monotonic()<deadline:
    time.sleep(.05)
assert not os.path.exists('/proc/'+str(g)), 'init failed to reap adopted child'
print('adopted_child_reaped')
"""

def valid_id(value):
    return bool(re.fullmatch(r"[0-9a-f]{16}", value))

def image_id(value):
    if not re.fullmatch(r"sha256:[0-9a-f]{64}", value):
        raise ValueError("invalid immutable image")
    return value

def run(args, timeout=25):
    result = subprocess.run(args, cwd=ROOT, env=ENV, capture_output=True, timeout=timeout)
    if len(result.stdout) + len(result.stderr) > 65536:
        raise RuntimeError("command output cap exceeded")
    if result.returncode:
        # Docker/Compose stderr can contain configuration; never publish it.
        raise RuntimeError("bounded command failed: " + args[0])
    return result.stdout.decode().strip()

def docker(*args, timeout=25):
    return run(["/usr/bin/docker", *args], timeout)

def inspect(name, template):
    return json.loads(docker("inspect", "--format", template, name))

def source_hash(vpn_override=None):
    digest = hashlib.sha256()
    for name in ("docker-compose.yml", "docker-compose.vpn.yml", ".env", "docker-compose.private-https.yml"):
        path = ROOT / name
        if path.exists():
            digest.update(name.encode() + b"\0" + (vpn_override.encode() if name=="docker-compose.vpn.yml" and vpn_override is not None else path.read_bytes()))
    return digest.hexdigest()

def vpn_patch(text):
    anchor = "  app:\n"
    if text.count(anchor) != 1 or "\n    init:" in text or "\n    healthcheck:" in text:
        raise RuntimeError("VPN Compose app contract changed: HOLD")
    addition = "    init: true\n    healthcheck: " + json.dumps({"test": HEALTH, "interval": "30s", "timeout": "5s", "start_period": "10s", "retries": 3}) + "\n"
    return text.replace(anchor, anchor + addition)

def replace_vpn(expected, replacement):
    path = (ROOT / "docker-compose.vpn.yml").resolve()
    if path.parent != Path("/root/rootledger/shared") or path.name != "docker-compose.vpn.yml":
        raise RuntimeError("VPN Compose is not the pinned shared file")
    st = path.stat()
    if st.st_uid != 0 or st.st_mode & 0o022 or st.st_size > 16384 or path.read_text() != expected:
        raise RuntimeError("VPN Compose identity/content changed")
    fd, temp = tempfile.mkstemp(prefix=".reap-", dir=path.parent)
    try:
        os.fchmod(fd, st.st_mode & 0o777)
        with os.fdopen(fd, "w") as stream:
            stream.write(replacement)
            stream.flush()
            os.fsync(stream.fileno())
        if path.read_text() != expected:
            raise RuntimeError("VPN Compose changed before replacement")
        os.replace(temp, path)
    finally:
        if os.path.exists(temp):
            os.unlink(temp)

def helper_hash():
    return hashlib.sha256(Path(__file__).read_bytes()).hexdigest()

def secure_state():
    STATE.mkdir(mode=0o700, parents=True, exist_ok=True)
    st = STATE.lstat()
    if not STATE.is_dir() or STATE.is_symlink() or st.st_uid != 0 or st.st_mode & 0o077:
        raise RuntimeError("unsafe maintenance state directory")

def read_json(path):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd) as stream:
        st = os.fstat(stream.fileno())
        if st.st_uid != 0 or st.st_mode & 0o077 or st.st_size > 65536:
            raise RuntimeError("unsafe maintenance record")
        return json.load(stream)

def write_json(path, value):
    tmp = path.with_name(path.name + ".tmp")
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    try:
        with os.fdopen(fd, "w") as stream:
            json.dump(value, stream)
            stream.write("\n")
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(tmp, path)
        fd = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)
    finally:
        if tmp.exists():
            tmp.unlink()

def neighbors():
    # Container identity/started-at, never environment or mounts of neighbors.
    ids = docker("ps", "-q").split()
    if len(ids) > 128:
        raise RuntimeError("neighbor snapshot cap exceeded")
    out = {}
    for cid in ids:
        name = inspect(cid, "{{json .Name}}").lstrip("/")
        if name == APP or name.startswith("mcpshka-reap-smoke-"):
            continue
        out[name] = [cid, inspect(cid, "{{json .State.StartedAt}}")]
    return out

def health_version():
    with urllib.request.urlopen("http://172.31.250.3:8000/api/health", timeout=3) as response:
        if response.status != 200:
            raise RuntimeError("API unhealthy")
        data = response.read(4097)
    if len(data) > 4096:
        raise RuntimeError("API response cap exceeded")
    return json.loads(data).get("version")

def app_state():
    return json.loads(docker("inspect","--format","{{json .State}}",APP,timeout=3))

def await_health(version):
    deadline=time.monotonic()+60
    for _ in range(40):
        if time.monotonic()>=deadline:
            break
        try:
            s = app_state()
            if s.get("Running") and s.get("Health", {}).get("Status") == "healthy" and health_version() == version:
                return s["Pid"]
        except (OSError, ValueError, RuntimeError):
            pass
        time.sleep(1)
    raise RuntimeError("RootLedger health acceptance failed")

def zombies(ppid):
    processes={}
    paths=list(Path("/proc").glob("[0-9]*/stat"))
    if len(paths)>16384:
        raise RuntimeError("process snapshot cap exceeded")
    for path in paths:
        try:
            fields=path.read_text().rsplit(")",1)[1].split()
            processes[int(path.parent.name)]=(int(fields[1]),fields[0])
        except FileNotFoundError:
            pass
    count=0
    for pid,(_,state) in processes.items():
        if state!="Z":
            continue
        parent=pid
        for _ in range(64):
            if parent==ppid:
                count+=1
                break
            if parent not in processes:
                break
            parent=processes[parent][0]
    return count

def overlay(image, init, health):
    # JSON is a YAML subset; fixed app-only overlay, no secrets.
    return {"services": {"app": {"image": image_id(image), "init": bool(init), "healthcheck": health}}}

def compose(override):
    args = ["/usr/bin/docker", "compose", "--project-directory", str(ROOT), "-p", "rootledger",
            "-f", str(ROOT / "docker-compose.yml"), "-f", str(ROOT / "docker-compose.vpn.yml")]
    if b"\nROOTLEDGER_HTTPS_PROFILE=private\n" in b"\n" + (ROOT / ".env").read_bytes().strip() + b"\n":
        args += ["-f", str(ROOT / "docker-compose.private-https.yml"), "--profile", "public-https"]
    return args + ["-f", str(override)]

def smoke(operation_id, image):
    name = "mcpshka-reap-smoke-" + operation_id
    before = neighbors()
    cid = docker("create", "--name", name, "--label", "mcpshka.reap=" + operation_id,
                 "--network", "none", "--read-only", "--init", "--cap-drop", "ALL",
                 "--security-opt", "no-new-privileges", "--pids-limit", "32", "--memory", "128m",
                 "--cpus", "0.25", "--entrypoint", "python", image, "-c", SMOKE)
    if not re.fullmatch(r"[0-9a-f]{64}", cid):
        raise RuntimeError("invalid smoke container identity")
    try:
        output = docker("start", "--attach", cid, timeout=15)
        if output != "adopted_child_reaped" or inspect(cid, "{{json .State.ExitCode}}") != 0:
            raise RuntimeError("isolated reap smoke failed")
    finally:
        if inspect(cid, '{{json (index .Config.Labels "mcpshka.reap")}}') != operation_id:
            raise RuntimeError("smoke ownership changed")
        docker("rm", "-f", cid)
    if neighbors() != before:
        raise RuntimeError("neighbor services changed during smoke")
    write_json(STATE / "smoke-proof.json", {"image": image, "helper": helper_hash(), "time": time.time()})

def apply(operation_id, image, before_id):
    proof = read_json(STATE / "smoke-proof.json")
    if proof["image"] != image or proof["helper"] != helper_hash() or not 0 <= time.time()-proof["time"] < 86400:
        raise RuntimeError("matching fresh isolated smoke required")
    before = app_state()
    if not before.get("Running") or before.get("Health", {}).get("Status") != "healthy":
        raise RuntimeError("RootLedger must be healthy before maintenance")
    version = health_version()
    others = neighbors()
    source = source_hash()
    old_health = inspect(APP, "{{json .Config.Healthcheck}}")
    if not old_health:
        raise RuntimeError("rollback healthcheck missing")
    original = {"test": old_health["Test"]}
    for key in ("Interval", "Timeout", "StartPeriod"):
        if old_health.get(key):
            original[{"Interval": "interval", "Timeout": "timeout", "StartPeriod": "start_period"}[key]] = str(old_health[key]) + "ns"
    if old_health.get("Retries"):
        original["retries"] = old_health["Retries"]
    old_init = inspect(APP, "{{json .HostConfig.Init}}") or False
    directory = STATE / operation_id
    rollback = directory / "rollback.json"
    fixed = directory / "fixed.json"
    write_json(rollback, overlay(image, old_init, original))
    write_json(fixed, overlay(image, True, {"test": HEALTH, "interval": "30s", "timeout": "5s", "start_period": "10s", "retries": 3}))
    # Validate only the selected app; do not output the merged secret-bearing config.
    config = json.loads(run(compose(fixed) + ["config", "--format", "json"]))
    app = config["services"]["app"]
    if app["image"] != image or app.get("init") is not True or app["healthcheck"]["test"] != HEALTH:
        raise RuntimeError("merged Compose contract mismatch")
    if source_hash() != source or inspect(APP, "{{json .Id}}") != before_id or inspect(APP, "{{json .Image}}") != image:
        raise RuntimeError("RootLedger changed during preparation")
    vpn_before = (ROOT / "docker-compose.vpn.yml").read_text()
    vpn_after = vpn_patch(vpn_before)
    write_json(directory / "restore.json", {"image": image, "version": version, "source": source, "neighbors": others, "vpn_before": vpn_before, "vpn_after": vpn_after, "installed_source": source_hash(vpn_after)})
    write_json(directory / "status.json", {"operation_id": operation_id, "state": "applying"})
    try:
        replace_vpn(vpn_before, vpn_after)
        installed_source = source_hash(vpn_after)
        if source_hash()!=installed_source:
            raise RuntimeError("source changed during persistent patch")
        run(compose(fixed) + ["up", "-d", "--no-deps", "--no-build", "--pull", "never", "--force-recreate", "app"], timeout=60)
        pid = await_health(version)
        if not inspect(APP, "{{json .HostConfig.Init}}") or inspect(APP, "{{json .Config.Healthcheck}}")["Test"] != HEALTH:
            raise RuntimeError("installed reap contract mismatch")
        time.sleep(35)  # Observe at least one real healthcheck interval.
        if zombies(pid) or neighbors() != others or source_hash() != installed_source:
            raise RuntimeError("post-maintenance acceptance failed")
        write_json(directory / "status.json", {"operation_id": operation_id, "state": "succeeded", "pid": pid, "zombies": 0, "rollback_ready": True, "image": image})
    except Exception:
        try:
            restore(operation_id)
        except Exception:
            write_json(directory / "status.json", {"operation_id": operation_id, "state": "rollback_failed"})
            raise RuntimeError("guarded rollback could not be accepted")
        raise RuntimeError("maintenance failed; guarded rollback accepted")

def restore(operation_id):
    directory = STATE / operation_id
    proof = read_json(directory / "restore.json")
    existing=docker("ps","-a","--filter","name=^/"+APP+"$","--format","{{.ID}}").split()
    if len(existing)>1 or source_hash() not in (proof["source"], proof.get("installed_source")) or (existing and inspect(APP, "{{json .Image}}") != proof["image"]):
        raise RuntimeError("rollback source/image mismatch: HOLD")
    vpn = (ROOT / "docker-compose.vpn.yml").read_text()
    if vpn == proof["vpn_after"]:
        replace_vpn(vpn, proof["vpn_before"])
    elif vpn != proof["vpn_before"]:
        raise RuntimeError("rollback VPN content changed: HOLD")
    run(compose(directory / "rollback.json") + ["up", "-d", "--no-deps", "--no-build", "--pull", "never", "--force-recreate", "app"], timeout=60)
    pid = await_health(proof["version"])
    if neighbors() != proof["neighbors"]:
        raise RuntimeError("rollback neighbor acceptance failed")
    write_json(directory / "status.json", {"operation_id": operation_id, "state": "rolled_back", "pid": pid})

def main():
    if len(sys.argv) != 3 or sys.argv[1] not in ("smoke", "apply", "rollback", "recover") or not valid_id(sys.argv[2]):
        raise ValueError("expected pinned operation and 16-hex identity")
    if os.geteuid() != 0 or CURRENT.resolve() != ROOT or ROOT.is_symlink():
        raise RuntimeError("pinned root-owned release required")
    secure_state()
    fd = os.open("/root/rootledger/.deployment.lock", os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "w") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        operation, operation_id = sys.argv[1:]
        if operation=="recover":
            path=STATE / operation_id / "status.json"
            if not path.exists():
                return
            state=read_json(path)
            if state.get("state")=="applying":
                restore(operation_id)
            elif state.get("state")=="running" and state.get("operation")=="smoke":
                name="mcpshka-reap-smoke-"+operation_id
                ids=docker("ps","-a","--filter","name=^/"+name+"$","--format","{{.ID}}").split()
                if len(ids)>1:
                    raise RuntimeError("smoke cleanup identity ambiguous")
                if ids and inspect(ids[0], '{{json (index .Config.Labels "mcpshka.reap")}}')==operation_id:
                    docker("rm","-f",ids[0])
                write_json(path,{"operation_id":operation_id,"state":"failed","operation":"smoke","detail":"worker interrupted; own smoke cleaned"})
            return
        image = image_id(inspect(APP, "{{json .Image}}"))
        before_id = inspect(APP, "{{json .Id}}")
        directory = STATE / operation_id
        if operation != "rollback":
            directory.mkdir(mode=0o700)
            write_json(directory / "status.json", {"operation_id": operation_id, "state": "running", "operation": operation})
        if operation == "smoke":
            smoke(operation_id, image)
            write_json(directory / "status.json", {"operation_id": operation_id, "state": "succeeded", "operation": "smoke", "image": image})
        elif operation == "apply":
            apply(operation_id, image, before_id)
        else:
            restore(operation_id)
    print(json.dumps({"operation_id": operation_id, "state": "succeeded"}))

if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        if len(sys.argv)==3 and valid_id(sys.argv[2]) and os.geteuid()==0:
            path=STATE / sys.argv[2] / "status.json"
            try:
                previous=read_json(path)
                if previous.get("state") not in ("rolled_back","rollback_failed","succeeded"):
                    write_json(path,{"operation_id":sys.argv[2],"state":"failed","error":type(error).__name__})
            except Exception:
                pass
        # Do not publish exception details that could contain secret command output.
        print(json.dumps({"state": "failed", "error": type(error).__name__}))
        sys.exit(1)
