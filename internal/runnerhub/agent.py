#!/usr/bin/env python3
"""MCPshka pull runner: one file, Python 3.8+ standard library only.

Runs in any cloud IDE or laptop (GigaIDE Cloud, Codespaces, Gitpod, WSL).
It only calls MCPshka outward over HTTPS — no open ports, no SSH:
  register once with a one-time token (MCPshka web → «Раннеры», Face ID),
  then long-poll for one job, run it, send heartbeats, return the result.

  python3 mcpshka-runner.py --url https://HOST --token mrt_...   # first time
  python3 mcpshka-runner.py --url https://HOST                   # afterwards

The key is kept in ~/.mcpshka-runner.json (0600) or --conf; /runner/setup
installs everything with one line and keeps the key in the IDE workspace. Ctrl+C stops it; a job it
was running goes back to the MCPshka queue by itself after the lease ends.
"""
import argparse, fcntl, io, json, os, platform, shutil, signal, socket, subprocess
import re, sys, tarfile, tempfile, threading, time, urllib.error, urllib.request, zipfile

CONF = os.path.expanduser("~/.mcpshka-runner.json")
# Tools live next to the key (set_home): cloud IDEs wipe ~ but keep the workspace.
GO_HOME = os.path.expanduser("~/.mcpshka-go")
BIN = os.path.expanduser("~/.mcpshka-tools/bin")
PYLIB = os.path.expanduser("~/.mcpshka-tools/py")
VERSION = "3"


def set_home(conf_path):
    global GO_HOME, BIN, PYLIB
    home = os.path.dirname(os.path.abspath(conf_path))
    if home != os.path.expanduser("~"):
        GO_HOME, BIN, PYLIB = (os.path.join(home, d) for d in ("go", "bin", "py"))


def log(*a):
    print(time.strftime("%H:%M:%S"), *a, flush=True)


def api(base, path, body=None, key=None, timeout=60, raw=False):
    req = urllib.request.Request(base + "/runner/" + path, method="POST" if body is not None else "GET")
    req.add_header("User-Agent", "mcpshka-runner/" + VERSION)
    if key:
        req.add_header("Authorization", "Bearer " + key)
    if body is not None:
        req.add_header("Content-Type", "application/json")
        req.data = json.dumps(body).encode()
    with urllib.request.urlopen(req, timeout=timeout) as r:
        data = r.read()
    return data if raw else json.loads(data or b"{}")


def arch():
    m = platform.machine().lower()
    return {"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}.get(m, m)


def caps():
    c = {"os": sys.platform, "arch": arch(), "cpus": str(os.cpu_count() or 1), "go": "auto"}
    go = shutil.which("go")
    if go:
        try:
            c["go"] = subprocess.run([go, "env", "GOVERSION"], capture_output=True, text=True, timeout=20).stdout.strip() or "auto"
        except Exception:
            pass
    if shutil.which("sqlite3", path=tool_path()):
        c["sqlite3"] = "1"
    if PYLIB not in sys.path:
        sys.path.insert(0, PYLIB)
    try:
        import yaml  # noqa: F401
        c["pyyaml"] = "1"
    except Exception:
        pass
    return c


def tool_path():
    return BIN + os.pathsep + os.environ.get("PATH", "")


def ensure_tools():
    """Fetch what project tests need (sqlite3 CLI, PyYAML) into the runner
    folder without sudo — cloud IDEs like GigaIDE have none. A tool that still
    fails is simply missing from caps, so the hub never hands this runner a job
    that needs it."""
    c = caps()
    if "sqlite3" not in c and arch() == "amd64" and sys.platform.startswith("linux"):
        try:
            page = urllib.request.urlopen("https://sqlite.org/download.html", timeout=60).read().decode("utf-8", "replace")
            rel = re.search(r"PRODUCT,[^,]*,([0-9]{4}/sqlite-tools-linux-x64-[0-9]+\.zip)", page).group(1)
            log("fetching sqlite3", rel)
            blob = urllib.request.urlopen("https://sqlite.org/" + rel, timeout=300).read()
            os.makedirs(BIN, exist_ok=True)
            with zipfile.ZipFile(io.BytesIO(blob)) as z:
                name = next(n for n in z.namelist() if os.path.basename(n) == "sqlite3")
                dst = os.path.join(BIN, "sqlite3")
                with open(dst, "wb") as f:
                    f.write(z.read(name))
                os.chmod(dst, 0o755)
        except Exception as e:
            log("sqlite3 install:", e)
    if "pyyaml" not in c:
        base = [sys.executable, "-m", "pip", "install", "-q", "--target", PYLIB, "pyyaml"]
        for cmd in (base, base + ["--break-system-packages"]):
            try:
                if subprocess.run(cmd, capture_output=True, timeout=300).returncode == 0:
                    break
            except Exception as e:
                log("pyyaml install:", e)
                break


def ensure_go(workdir):
    """Use go from PATH; otherwise fetch the version go.mod names (cached)."""
    if shutil.which("go"):
        return None
    want = "1.22.0"
    try:
        for line in open(os.path.join(workdir, "go.mod")):
            parts = line.split()
            if len(parts) == 2 and parts[0] in ("go", "toolchain"):
                want = parts[1].lstrip("go")
    except OSError:
        pass
    root = os.path.join(GO_HOME, want)
    gobin = os.path.join(root, "go", "bin")
    if not os.path.exists(os.path.join(gobin, "go")):
        url = "https://go.dev/dl/go%s.linux-%s.tar.gz" % (want, arch())
        log("fetching", url)
        os.makedirs(root, exist_ok=True)
        with urllib.request.urlopen(url, timeout=600) as r:
            tarfile.open(fileobj=io.BytesIO(r.read()), mode="r:gz").extractall(root)
    return gobin


def run_job(base, key, job):
    jid = job["id"]
    log("job", jid, job["kind"], job["commit"][:12], " ".join(job["argv"]))
    work = tempfile.mkdtemp(prefix="mcpshka-" + jid + "-")
    stop = threading.Event()
    proc = [None]

    def beat():
        while not stop.wait(30):
            try:
                if api(base, "heartbeat", {"job_id": jid}, key).get("cancel"):
                    log("job cancelled by MCPshka")
                    if proc[0] and proc[0].poll() is None:
                        try:
                            os.killpg(proc[0].pid, signal.SIGKILL)
                        except OSError:
                            pass
                    return
            except Exception as e:
                log("heartbeat failed:", e)

    threading.Thread(target=beat, daemon=True).start()
    out, code, err = b"", -1, ""
    try:
        blob = api(base, "archive?job_id=" + jid, key=key, timeout=600, raw=True)
        tarfile.open(fileobj=io.BytesIO(blob), mode="r:gz").extractall(work)
        env = dict(os.environ, PATH=tool_path())
        env["PYTHONPATH"] = PYLIB + os.pathsep + env.get("PYTHONPATH", "")
        gobin = ensure_go(work)
        if gobin:
            env["PATH"] = gobin + os.pathsep + env["PATH"]
        env.setdefault("GOFLAGS", "-mod=mod")
        proc[0] = subprocess.Popen(job["argv"], cwd=work, env=env, stdout=subprocess.PIPE,
                                   stderr=subprocess.STDOUT, start_new_session=True)
        try:
            out, _ = proc[0].communicate(timeout=job.get("timeout_seconds") or 3600)
        except subprocess.TimeoutExpired:
            os.killpg(proc[0].pid, signal.SIGKILL)
            out, _ = proc[0].communicate()
            err = "timeout"
        code = proc[0].returncode
    except Exception as e:
        err = "runner: %s" % e
    finally:
        shutil.rmtree(work, ignore_errors=True)
    text = out.decode("utf-8", "replace")[-256 * 1024:]
    # Heartbeats keep the lease while the result is retried over a flaky link
    # (VPN switch, Wi-Fi drop); a lease lost meanwhile means MCPshka requeued it.
    for attempt in range(12):
        try:
            api(base, "result", {"job_id": jid, "exit_code": code, "output": text, "error": err}, key)
            break
        except urllib.error.HTTPError as e:
            log("result refused:", e)
            break
        except Exception as e:
            log("result upload failed, retry:", e)
            time.sleep(min(5 * (attempt + 1), 30))
    stop.set()
    log("job", jid, "exit", code, err)


def main():
    ap = argparse.ArgumentParser(description="MCPshka pull runner")
    ap.add_argument("--url", required=True, help="public MCPshka origin, https://…")
    ap.add_argument("--token", help="one-time registration token (mrt_…)")
    ap.add_argument("--name", default=socket.gethostname()[:40] or "runner")
    ap.add_argument("--conf", default=CONF, help="key file (setup keeps it in the IDE workspace)")
    ap.add_argument("--idle-exit", type=int, default=10, metavar="MIN",
                    help="exit after MIN minutes without jobs so a cloud IDE can sleep and save its free hours (0 = never)")
    a = ap.parse_args()
    conf_path = a.conf
    lock = open(conf_path + ".lock", "a")
    os.chmod(conf_path + ".lock", 0o600)
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        return  # postStartCommand and the IDE terminal may start together
    set_home(conf_path)
    base = a.url.rstrip("/")
    conf = {}
    if os.path.exists(conf_path):
        conf = json.load(open(conf_path))
    if conf.get("url") != base or not conf.get("key"):
        tok = a.token or os.environ.get("MCPSHKA_RUNNER_TOKEN")
        if not tok:
            sys.exit("not registered: pass --token from MCPshka «Раннеры»")
        name = "".join(ch if ch.isalnum() or ch in "._-" else "-" for ch in a.name)[:48]
        r = api(base, "register", {"token": tok, "name": name})
        conf = {"url": base, "id": r["id"], "key": r["key"]}
        fd = os.open(conf_path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
        with os.fdopen(fd, "w") as f:
            json.dump(conf, f)
        log("registered as", r["id"])
    key, delay = conf["key"], 5
    ensure_tools()
    log("waiting for jobs at", base, "caps:", " ".join(sorted(k for k in caps() if k not in ("os", "arch", "cpus"))))
    idle_since, refused = time.monotonic(), 0
    while True:
        if a.idle_exit > 0 and time.monotonic() - idle_since > a.idle_exit * 60:
            log("no jobs for", a.idle_exit, "min: exiting so the workspace can sleep; autostart brings me back")
            return
        try:
            r = api(base, "poll", {"caps": caps()}, key, timeout=70)
            delay, refused = 5, 0
            if r.get("job"):
                run_job(base, key, r["job"])
                idle_since = time.monotonic()
        except urllib.error.HTTPError as e:
            # 401/403 is final only when it persists: a proxy or a server
            # restart can answer one wrongly, and an exited agent needs the
            # owner to come back to the workspace.
            if e.code in (401, 403):
                refused = refused or time.monotonic()
                if time.monotonic() - refused > 30 * 60:
                    sys.exit("runner key refused for 30 min (disabled on «Раннеры»?): delete %s and register again" % conf_path)
                log("poll: key refused, retrying:", e)
                time.sleep(60)
                continue
            refused = 0
            log("poll:", e)
            time.sleep(delay)
            delay = min(delay * 2, 300)
        except Exception as e:
            log("poll:", e)
            time.sleep(delay)
            delay = min(delay * 2, 300)


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        pass
