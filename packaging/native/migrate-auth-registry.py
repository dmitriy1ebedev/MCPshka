#!/usr/bin/env python3
"""Prepare MCPshka hashed auth registry without interrupting production.

The raw bearer stays in the legacy environment during the first registry-aware
release so rollback to the previous binary remains possible. No secret value is
printed. Re-running is idempotent.
"""

import argparse
import grp
import hashlib
import json
import os
import pathlib
import tempfile

CONFIG_DIR = pathlib.Path("/etc/mcpshka")
ENV_FILE = CONFIG_DIR / "mcpshka.env"
HEADER_FILE = CONFIG_DIR / "mcp-auth-header"
AUTH_FILE = CONFIG_DIR / "auth.json"
IDENTITY = "openai-tunnel"


def set_config_dir(value):
    global CONFIG_DIR, ENV_FILE, HEADER_FILE, AUTH_FILE
    CONFIG_DIR = pathlib.Path(value).resolve()
    ENV_FILE = CONFIG_DIR / "mcpshka.env"
    HEADER_FILE = CONFIG_DIR / "mcp-auth-header"
    AUTH_FILE = CONFIG_DIR / "auth.json"


def read_env():
    lines = ENV_FILE.read_text(encoding="utf-8").splitlines()
    values = {}
    for line in lines:
        if not line or line.lstrip().startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        values[key.strip()] = value.strip()
    return lines, values


def read_header_token():
    try:
        header = HEADER_FILE.read_text(encoding="utf-8").strip()
    except OSError:
        return ""
    if not header.startswith("Bearer "):
        raise RuntimeError("mcp-auth-header has invalid format")
    token = header[len("Bearer "):].strip()
    if not token:
        raise RuntimeError("mcp-auth-header bearer token is empty")
    return token


def token_hash(token):
    return hashlib.sha256(token.encode("utf-8")).hexdigest()


def validate_auth_registry_metadata(st):
    mode = st.st_mode & 0o777
    if st.st_uid != 0:
        raise RuntimeError("auth.json must be owned by root")
    if mode == 0o600:
        return
    if mode == 0o640:
        try:
            mcpshka_gid = grp.getgrnam("mcpshka").gr_gid
        except KeyError as exc:
            raise RuntimeError("mcpshka group is missing") from exc
        if st.st_gid == mcpshka_gid:
            return
        raise RuntimeError("auth.json group-read requires group mcpshka")
    raise RuntimeError("auth.json permissions must be 0600 or 0640 root:mcpshka")


def load_registry():
    if not AUTH_FILE.exists():
        return None
    st = AUTH_FILE.lstat()
    if not pathlib.Path(AUTH_FILE).is_file() or pathlib.Path(AUTH_FILE).is_symlink():
        raise RuntimeError("auth.json must be a regular non-symlink file")
    validate_auth_registry_metadata(st)
    obj = json.loads(AUTH_FILE.read_text(encoding="utf-8"))
    if obj.get("version") != 1 or not isinstance(obj.get("credentials"), list):
        raise RuntimeError("auth.json has unsupported schema")
    return obj


def atomic_write(path, data, mode=0o600):
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp = tempfile.mkstemp(prefix=".%s." % path.name, dir=str(path.parent))
    try:
        os.fchmod(fd, mode)
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            f.write(data)
            f.flush()
            os.fsync(f.fileno())
        os.replace(tmp, str(path))
        os.chmod(str(path), mode)
        dfd = os.open(str(path.parent), os.O_RDONLY)
        try:
            os.fsync(dfd)
        finally:
            os.close(dfd)
    finally:
        try:
            os.unlink(tmp)
        except OSError:
            pass


def current_state():
    lines, values = read_env()
    env_token = values.get("MCPSHKA_TOKEN", "")
    header_token = read_header_token()
    if env_token and header_token and env_token != header_token:
        raise RuntimeError("legacy env token and tunnel auth header disagree")
    token = header_token or env_token
    if not token:
        raise RuntimeError("no current bearer credential found")

    digest = token_hash(token)
    registry = load_registry()
    matches = False
    if registry:
        for cred in registry["credentials"]:
            if (
                cred.get("token_sha256", "").lower() == digest
                and not cred.get("revoked", False)
            ):
                matches = True
                break
    return {
        "lines": lines,
        "values": values,
        "digest": digest,
        "registry": registry,
        "registry_matches": matches,
        "header_present": bool(header_token),
        "env_has_legacy_token": bool(env_token),
    }


def prepare():
    if os.geteuid() != 0:
        raise RuntimeError("prepare requires root")
    state = current_state()
    registry = state["registry"]
    if registry is None:
        registry = {
            "version": 1,
            "credentials": [{
                "id": IDENTITY,
                "token_sha256": state["digest"],
            }],
        }
        atomic_write(AUTH_FILE, json.dumps(registry, sort_keys=True) + "\n")
        state["registry_matches"] = True
    elif not state["registry_matches"]:
        raise RuntimeError("existing auth.json does not authorize the current tunnel bearer; refusing overwrite")

    out_lines = []
    for line in state["lines"]:
        if line.startswith("MCPSHKA_AUTH_FILE="):
            continue
        out_lines.append(line)
    out_lines.append("MCPSHKA_AUTH_FILE=%s" % AUTH_FILE)
    atomic_write(ENV_FILE, "\n".join(out_lines).rstrip() + "\n")

    return {
        "schema": "mcpshka.auth-registry-migration/v1",
        "prepared": True,
        "auth_file": str(AUTH_FILE),
        "identity": IDENTITY,
        "registry_matches_current_tunnel": True,
        "legacy_env_token_retained_for_rollback": state["env_has_legacy_token"],
        "secret_output": False,
    }


def status():
    state = current_state()
    return {
        "schema": "mcpshka.auth-registry-migration/v1",
        "prepared": state["values"].get("MCPSHKA_AUTH_FILE") == str(AUTH_FILE) and state["registry_matches"],
        "auth_file": str(AUTH_FILE),
        "registry_exists": state["registry"] is not None,
        "registry_matches_current_tunnel": state["registry_matches"],
        "header_present": state["header_present"],
        "legacy_env_token_retained_for_rollback": state["env_has_legacy_token"],
        "secret_output": False,
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--config-dir", default="/etc/mcpshka")
    parser.add_argument("action", choices=("status", "prepare"))
    args = parser.parse_args()
    set_config_dir(args.config_dir)
    result = status() if args.action == "status" else prepare()
    print(json.dumps(result, sort_keys=True))


if __name__ == "__main__":
    main()
