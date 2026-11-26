#!/usr/bin/python3 -I
"""Root-only, one-use signed release approval; no daemon grant endpoint."""
import argparse
import datetime
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import resource
import secrets
import stat
import subprocess
import sys
import time

REPO = Path('/root/repos/mcpshka')
AUTHORITY = Path('/var/lib/mcpshka-release-gates')
HELPER = Path('/usr/local/libexec/mcpshka-self-deploy-guarded')
FENCES = Path('/var/lib/mcpshka-deploy-fences')
SESSION_ROOT = Path('/var/lib/mcpshka-executor')
# Root executor seals local builds here (0700 dir, 0400 files); tests patch both.
LOCAL_RELEASES = SESSION_ROOT / 'release-artifacts'
LOCAL_OWNER = 0
INSTALLED_GATE = Path('/usr/local/libexec/mcpshka-self-deploy')
FENCE_CHECK = b'python3 -I -S /usr/local/libexec/mcpshka-self-deploy check-fence --deployment-id "$ID" || exit 75'
DEPLOY_LOCK = Path('/run/mcpshka-self-deploy.lock')
LOCK_UID = 0  # owner of the deploy lock; tests of an unprivileged CI runner patch it
ENV = {'PATH': '/usr/bin:/bin', 'LANG': 'C', 'LC_ALL': 'C', 'OPENSSL_CONF': '/dev/null'}
FIELDS = {'schema', 'version', 'current_version', 'candidate_sha256', 'current_sha256',
          'source_tree', 'source_digest', 'ci_run', 'ci_receipt_sha256', 'helper_sha256'}
PAIRED_FIELDS = FIELDS | {'broker_sha256', 'current_broker_sha256'}
LIMIT = 128 << 20

class DeploymentFenced(ValueError):
    pass


# Test evidence a local release accepts, with the full check set each kind
# must pass (internal/releaseprep.ParseLocalReceipt mirrors this). github is
# the 0.9.8 fallback when the GitVerse site itself is down.
LOCAL_TEST_SUITES = {
    'module': ('module',),
    'gitverse': ('release-suite', 'snapshot-contracts'),
    'github': ('admin-recovery', 'checks (build)', 'checks (fuzz)', 'checks (race)',
               'checks (release)', 'checks (release-arm64)', 'isolated-workbench'),
}

def fence_directory(create=False):
    if os.path.realpath(FENCES) != str(FENCES):
        raise ValueError('aliased deployment fences: HOLD')
    if create:
        try:
            FENCES.mkdir(mode=0o755)
        except FileExistsError:
            pass
        else:
            os.chmod(FENCES, 0o755, follow_symlinks=False)
            sync_directory(FENCES.parent)
    info = FENCES.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != 0 or stat.S_IMODE(info.st_mode) != 0o755:
        raise ValueError('unsafe deployment fence directory: HOLD')


def validate_fence(record, deployment):
    fields = {'schema', 'deployment_id', 'receipt_sha256', 'fenced_at', 'reason',
              'gate_sha256', 'helper_sha256', 'observed_current_sha256', 'authorization_session_id'}
    if (not isinstance(record, dict) or set(record) != fields or
            record['schema'] != 'mcpshka.deployment-fence/v1' or
            record['deployment_id'] != deployment or
            not isinstance(record['authorization_session_id'], str) or not re.fullmatch('[a-f0-9]{32}', record['authorization_session_id']) or
            not re.fullmatch('[0-9]{13}-[a-f0-9]{12}', deployment) or
            type(record['fenced_at']) is not int or not 0 < record['fenced_at'] <= int(time.time()) or
            not isinstance(record['reason'], str) or not record['reason'].strip() or not 1 <= len(record['reason'].encode()) <= 200 or
            any(ord(c) < 32 for c in record['reason'])):
        raise ValueError('invalid deployment fence: HOLD')
    for field in ('receipt_sha256', 'gate_sha256', 'helper_sha256', 'observed_current_sha256'):
        if not isinstance(record[field], str) or not re.fullmatch('[a-f0-9]{64}', record[field]):
            raise ValueError('invalid deployment fence digest: HOLD')


def deployment_fenced(deployment):
    if not re.fullmatch('[0-9]{13}-[a-f0-9]{12}', deployment):
        raise ValueError('invalid deployment identity: HOLD')
    try:
        fence_directory()
    except FileNotFoundError:
        return False
    try:
        record = strict_json(read_regular(FENCES / (deployment + '.json'), 8192, owner=0))
    except FileNotFoundError:
        return False
    validate_fence(record, deployment)
    return True


def require_unfenced(deployment):
    try:
        fenced = deployment_fenced(deployment)
    except (OSError, ValueError) as error:
        raise DeploymentFenced('deployment fence inspection failed: HOLD; original receipt retained') from error
    if fenced:
        raise DeploymentFenced('deployment permanently FENCED; original outcome UNKNOWN; do not replay')


def read_deployment(deployment):
    # Read SQLite as its service owner; an untrusted DB never runs with root access.
    key = ('deploys/' + deployment + '.json').encode().hex()
    database = Path('/var/lib/mcpshka/state.db')
    if read_regular('/var/lib/mcpshka/state.mode', 64).strip() != b'sqlite-v1':
        raise ValueError('deployment authority marker mismatch: HOLD')
    info = database.lstat()
    if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o600 or info.st_uid == 0:
        raise ValueError('deployment authority unavailable: HOLD')
    result = subprocess.run(['/usr/bin/sqlite3', '-readonly', '-safe', '-batch', '-bail',
        '-noheader', '-cmd', '.timeout 4000', '-cmd', '.limit length 262144', str(database),
        "SELECT hex(payload)||'|'||sha256 FROM records WHERE path=CAST(X'" + key + "' AS TEXT);"],
        user=info.st_uid, group=info.st_gid, extra_groups=[], env=ENV, stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=8, check=True)
    return decode_deployment_query(result.stdout)


def decode_deployment_query(output):
    if not 0 < len(output) < 132000:
        raise ValueError('deployment receipt budget: HOLD')
    # SQLite reports the effective .limit before the query's single row.
    lines = output.decode('ascii').splitlines()
    if len(lines) != 2 or not re.fullmatch(r'\s*length\s+262144', lines[0]):
        raise ValueError('deployment query limit or row count mismatch: HOLD')
    payload, digest = lines[1].split('|')
    raw = bytes.fromhex(payload)
    if not 0 < len(raw) <= 65536 or sha(raw) != digest:
        raise ValueError('deployment receipt integrity: HOLD')
    return raw


def retirement_approval(session_id, owner, now):
    if not re.fullmatch('[a-f0-9]{32}', session_id or '') or not owner:
        raise ValueError('Face ID administrative session required: HOLD')
    permit = strict_json(read_regular(SESSION_ROOT / 'admin-session-current.json', 4096, owner=0))
    if (not isinstance(permit, dict) or permit.get('schema') != 'mcpshka.admin-session/v1' or
            permit.get('id') != session_id or permit.get('owner') != owner or
            permit.get('auth_method') != 'webauthn' or permit.get('policy') != 'mcpshka-administration/v1' or
            permit.get('state') != 'active' or type(permit.get('generation')) is not int or permit['generation'] <= 0 or
            not isinstance(permit.get('authorized_at'), str)):
        raise ValueError('administrative session inactive or owner/policy mismatch: HOLD')
    issued = datetime.datetime.fromisoformat(permit['authorized_at'].replace('Z', '+00:00'))
    if issued.tzinfo is None or not 0 < issued.timestamp() <= now:
        raise ValueError('administrative session timestamp invalid: HOLD')
    return permit


def retire_deployment(deployment, receipt_sha256, current_sha256, reason, session_id, owner):
    require_operator()
    if not re.fullmatch('[0-9]{13}-[a-f0-9]{12}', deployment):
        raise ValueError('invalid deployment identity: HOLD')
    session_lock = os.open(SESSION_ROOT / 'admin-session.lock', os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600)
    try:
        info = os.fstat(session_lock)
        if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_nlink != 1 or info.st_mode & 0o077:
            raise ValueError('unsafe administrative session lock: HOLD')
        fcntl.flock(session_lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        retirement_approval(session_id, owner, time.time())
        return fence_deployment(deployment, receipt_sha256, current_sha256, reason, session_id)
    finally:
        os.close(session_lock)


def fence_deployment(deployment, receipt_sha256, current_sha256, reason, session_id):
    if Path(__file__).resolve() != INSTALLED_GATE:
        raise ValueError('retirement requires installed fence-aware gate: HOLD')
    helper = read_regular(HELPER, 131072, owner=0)
    if FENCE_CHECK not in helper or helper.index(FENCE_CHECK) < helper.index(b'flock -n 9'):
        raise ValueError('installed helper lacks fence admission under lock: HOLD')
    private_directory(AUTHORITY)
    admission = os.open(AUTHORITY / '.lock', os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600)
    try:
        info = os.fstat(admission)
        if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_nlink != 1 or stat.S_IMODE(info.st_mode) != 0o600:
            raise ValueError('unsafe approval lock: HOLD')
        fcntl.flock(admission, fcntl.LOCK_EX | fcntl.LOCK_NB)
        assert_no_legacy_launchers()
        return fence_under_admission(deployment, receipt_sha256, current_sha256, reason, session_id, helper)
    finally:
        os.close(admission)


def assert_no_legacy_launchers(proc=Path('/proc')):
    entries = list(proc.iterdir())
    if len(entries) > 4096:
        raise ValueError('launcher observation budget: HOLD')
    names = {'mcpshka-self-deploy', 'release-gate.py', 'self-deploy.sh', 'mcpshka-self-deploy-guarded'}
    for entry in entries:
        if not entry.name.isdigit() or int(entry.name) == os.getpid():
            continue
        try:
            with (entry / 'cmdline').open('rb') as stream:
                raw = stream.read(32769)
        except FileNotFoundError:
            continue
        if len(raw) > 32768:
            raise ValueError('launcher arguments observation budget: HOLD')
        for argument in raw.split(b'\0'):
            arg = os.fsdecode(argument)
            if Path(arg).name in names or (arg.startswith(str(AUTHORITY) + '/') and arg.endswith('/helper.sh')):
                raise ValueError('legacy deployment launcher still present: HOLD')


def disable_legacy_helpers(deployment):
    entries = list(AUTHORITY.iterdir())
    if len(entries) > 128:
        raise ValueError('sealed helper inventory budget: HOLD')
    inventory = []
    for entry in entries:
        if entry.name == '.lock':
            continue
        if not re.fullmatch('[a-f0-9]{64}', entry.name):
            raise ValueError('unknown approval authority entry: HOLD')
        private_directory(entry)
        path = entry / 'helper.sh'
        try:
            raw = read_regular(path, 131072, owner=0)
        except FileNotFoundError:
            continue
        if FENCE_CHECK in raw:
            continue
        try:
            consumed = strict_json(read_regular(entry / 'consumed.json', 4096, owner=0))
        except FileNotFoundError:
            consumed = None
        if consumed is not None:
            other = consumed.get('deployment_id')
            if not isinstance(other, str) or not re.fullmatch('[0-9]{13}-[a-f0-9]{12}', other):
                raise ValueError('sealed deployment identity unknown: HOLD')
            if other != deployment and strict_json(read_deployment(other)).get('state') not in ('succeeded', 'failed', 'rolled_back'):
                raise ValueError('another legacy deployment is pending: HOLD')
        inventory.append(dict(path=str(path), sha256=sha(raw)))
    # Direct legacy execve/ExecStart cannot execute these paths, even if PID1 has a queued request.
    for item in inventory:
        fd = os.open(item['path'], os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        try:
            info = os.fstat(fd)
            if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_nlink != 1 or info.st_mode & 0o022:
                raise ValueError('unsafe legacy execution entry: HOLD')
            os.fchmod(fd, 0o600)
            os.fsync(fd)
        finally:
            os.close(fd)
        sync_directory(Path(item['path']).parent)
    return inventory


def fence_under_admission(deployment, receipt_sha256, current_sha256, reason, session_id, helper):
    lock = os.open(DEPLOY_LOCK, os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600)
    try:
        info = os.fstat(lock)
        if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_nlink != 1 or info.st_mode & 0o022:
            raise ValueError('unsafe deployment lock: HOLD')
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if deployment_fenced(deployment):
            previous = strict_json(read_regular(FENCES / (deployment + '.json'), 8192, owner=0))
            if (previous['receipt_sha256'] != receipt_sha256 or previous['observed_current_sha256'] != current_sha256 or
                    previous['reason'] != reason or previous['authorization_session_id'] != session_id):
                raise ValueError('deployment already fenced with different pins: HOLD')
            return fence_result(previous)
        unit = run('/usr/bin/systemctl', 'show', '--no-pager',
                   '--property=LoadState,ActiveState,SubState', 'mcpshka-deploy-' + deployment + '.service')
        if len(unit) > 1024 or dict(line.split('=', 1) for line in unit.decode().splitlines()) != {
                'LoadState': 'not-found', 'ActiveState': 'inactive', 'SubState': 'dead'}:
            raise ValueError('deployment unit not proven absent: HOLD')
        raw = read_deployment(deployment)
        record = strict_json(raw)
        if (sha(raw) != receipt_sha256 or record.get('deployment_id') != deployment or
                record.get('state') not in ('accepted', 'running', 'UNKNOWN', 'unknown', 'rollback_failed') or
                sha(read_regular('/usr/local/bin/mcpshka', LIMIT, owner=0)) != current_sha256):
            raise ValueError('deployment retirement pin mismatch: HOLD')
        fence = dict(schema='mcpshka.deployment-fence/v1', deployment_id=deployment,
            receipt_sha256=receipt_sha256, observed_current_sha256=current_sha256,
            fenced_at=int(time.time()), reason=reason, authorization_session_id=session_id,
            gate_sha256=sha(read_regular(INSTALLED_GATE, 131072, owner=0)),
            helper_sha256=sha(helper))
        validate_fence(fence, deployment)
        disabled = disable_legacy_helpers(deployment)
        assert_no_legacy_launchers()
        fence_directory(create=True)
        write_new(FENCES / (deployment + '.json'), canonical(fence), 0o644)
        return dict(fence_result(fence), legacy_helpers_disabled=disabled)
    finally:
        os.close(lock)


def fence_result(fence):
    return dict(state='FENCED', deployment_id=fence['deployment_id'], historical_outcome='UNKNOWN',
                original_receipt_retained=True, retry_allowed=False, fence_sha256=sha(canonical(fence)),
                authorization_session_id=fence['authorization_session_id'])


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':'), allow_nan=False).encode()


def strict_json(raw):
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError('duplicate JSON field: HOLD')
            result[key] = value
        return result
    return json.loads(raw, object_pairs_hook=unique)


def validate_plan(plan):
    expected = (PAIRED_FIELDS if isinstance(plan,dict) and plan.get('schema') == 'mcpshka.release-plan/v2' else FIELDS)
    if not isinstance(plan, dict) or set(plan) != expected or plan['schema'] not in ('mcpshka.release-plan/v1','mcpshka.release-plan/v2'):
        raise ValueError('invalid release plan: HOLD')
    for field in expected - {'schema', 'ci_run', 'version', 'current_version'}:
        width = 40 if field == 'source_tree' else 64
        if not isinstance(plan[field], str) or not re.fullmatch('[a-f0-9]{' + str(width) + '}', plan[field]):
            raise ValueError('invalid release digest: HOLD')
    for field in ('version', 'current_version'):
        if not isinstance(plan[field], str) or not re.fullmatch(r'0\.[0-9]{1,3}\.[0-9]{1,6}', plan[field]):
            raise ValueError('invalid release version: HOLD')
    if plan['version'] == plan['current_version'] or type(plan['ci_run']) is not int or plan['ci_run'] < 0 or (plan['ci_run'] == 0 and plan['schema'] != 'mcpshka.release-plan/v2'):
        raise ValueError('invalid release transition: HOLD')


def read_regular(path, limit, owner=None):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        before = os.fstat(fd)
        if (not stat.S_ISREG(before.st_mode) or before.st_nlink != 1 or
                not 0 < before.st_size <= limit or before.st_mode & 0o002 or
                (owner is not None and (before.st_uid != owner or before.st_mode & 0o022))):
            raise ValueError('unsafe release file: HOLD')
        chunks, count = [], 0
        while count <= limit:
            chunk = os.read(fd, min(65536, limit + 1 - count))
            if not chunk:
                break
            chunks.append(chunk)
            count += len(chunk)
        pin = lambda s: (s.st_dev, s.st_ino, s.st_nlink, s.st_mode, s.st_uid,
                         s.st_size, s.st_mtime_ns, s.st_ctime_ns)
        if count != before.st_size or pin(before) != pin(os.fstat(fd)) or pin(before) != pin(os.lstat(path)):
            raise ValueError('release file changed: HOLD')
        return b''.join(chunks)
    finally:
        os.close(fd)


def run(*args):
    return subprocess.run(args, check=True, timeout=30, env=ENV, stdin=subprocess.DEVNULL,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE).stdout


def verify_signature(public, payload, signature):
    try:
        run('/usr/bin/openssl', 'pkeyutl', '-verify', '-pubin', '-rawin',
            '-inkey', str(public), '-in', str(payload), '-sigfile', str(signature))
    except subprocess.CalledProcessError as error:
        raise ValueError('release signature invalid: HOLD') from error


def validate_approval(record, *, now, consumed):
    if not isinstance(record, dict) or set(record) != {'schema', 'plan', 'plan_sha256', 'approved_at', 'expires_at'}:
        raise ValueError('invalid approval: HOLD')
    validate_plan(record['plan'])
    if (record['schema'] != 'mcpshka.release-approval/v1' or consumed or
            record['plan_sha256'] != sha(canonical(record['plan'])) or
            type(record['approved_at']) is not int or type(record['expires_at']) is not int or
            not record['approved_at'] <= now < record['expires_at'] <= record['approved_at'] + 600):
        raise ValueError('approval expired, consumed or mismatched: HOLD')


def require_operator():
    if os.geteuid() != 0:
        raise ValueError('authorized root launcher required')


def private_directory(path):
    if os.path.realpath(path) != str(path):
        raise ValueError('aliased authority: HOLD')
    path.mkdir(mode=0o700, exist_ok=True)
    info = path.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != 0 or stat.S_IMODE(info.st_mode) != 0o700:
        raise ValueError('unsafe authority directory: HOLD')


def sync_directory(path):
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def write_new(path, raw, mode=0o600):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode)
    with os.fdopen(fd, 'wb') as stream:
        os.fchmod(stream.fileno(), mode)
        stream.write(raw)
        stream.flush()
        os.fsync(stream.fileno())
    sync_directory(path.parent)


def candidate_path(plan):
    return REPO / 'bin' / ('mcpshka-' + plan['version'] + '.' + plan['candidate_sha256'][:12] + '.candidate')


def approve_and_deploy(plan_file, receipt_file, approved_plan_sha256, broker_args=None):
    require_operator()
    os.umask(0o077)
    plan = strict_json(read_regular(plan_file, 16384))
    validate_plan(plan)
    if broker_args is not None:
        validate_broker_request(broker_args, plan)
    receipt = read_regular(receipt_file, 131072)
    validate_receipt(strict_json(receipt), plan)
    candidate = read_regular(candidate_path(plan), LIMIT)
    paired = plan['schema'] == 'mcpshka.release-plan/v2'
    broker = None
    if paired:
        broker_name = 'mcpshka-broker-' + plan['version'] + '.' + plan['broker_sha256'][:12] + '.candidate'
        broker = read_regular(REPO / 'bin' / broker_name, LIMIT)
        if (sha(broker) != plan['broker_sha256'] or
                sha(read_regular('/usr/local/libexec/mcpshka-broker',LIMIT,owner=0)) != plan['current_broker_sha256']):
            raise ValueError('privileged broker candidate/current SHA mismatch: HOLD')
    helper = read_regular(HELPER, 131072, owner=0)
    if (sha(receipt) != plan['ci_receipt_sha256'] or sha(candidate) != plan['candidate_sha256'] or
            sha(helper) != plan['helper_sha256'] or
            sha(read_regular('/usr/local/bin/mcpshka', LIMIT, owner=0)) != plan['current_sha256']):
        raise ValueError('release inputs differ from plan: HOLD')
    space = os.statvfs('/var/lib')
    if space.f_bavail * space.f_frsize - len(candidate) - (len(broker) if broker is not None else 0) < max(3 << 30, space.f_blocks * space.f_frsize // 10):
        raise ValueError('release disk reserve insufficient: HOLD')
    # The root launcher supplies this pin only after explicit human authorization.
    # A CI receipt or chat log cannot itself provide launch authority.
    plan_hash = sha(canonical(plan))
    if approved_plan_sha256 != plan_hash:
        raise ValueError('explicit approved plan SHA256 required: HOLD')
    private_directory(AUTHORITY)
    lock = os.open(AUTHORITY / '.lock', os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600)
    try:
        info = os.fstat(lock)
        if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_nlink != 1 or stat.S_IMODE(info.st_mode) != 0o600:
            raise ValueError('unsafe approval lock: HOLD')
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        directory = AUTHORITY / plan_hash
        directory.mkdir(mode=0o700)
        sync_directory(AUTHORITY)
        private_directory(directory)
        now = int(time.time())
        record = dict(schema='mcpshka.release-approval/v1', plan=plan, plan_sha256=plan_hash,
                      approved_at=now, expires_at=now + 600)
        write_new(directory / 'approval.json', canonical(record))
        write_new(directory / 'ci-receipt.json', receipt)
        write_new(directory / 'candidate', candidate)
        if paired:
            write_new(directory / 'broker-candidate', broker)
        write_new(directory / 'helper.sh', helper, 0o700)
        key = directory / 'signing-key.pem'
        try:
            run('/usr/bin/openssl', 'genpkey', '-algorithm', 'ED25519', '-out', str(key))
            run('/usr/bin/openssl', 'pkey', '-in', str(key), '-pubout', '-out', str(directory / 'public.pem'))
            run('/usr/bin/openssl', 'pkeyutl', '-sign', '-rawin', '-inkey', str(key),
                '-in', str(directory / 'approval.json'), '-out', str(directory / 'approval.sig'))
        finally:
            key.unlink(missing_ok=True)
        for name in ('approval.json', 'ci-receipt.json', 'candidate', 'helper.sh', 'public.pem', 'approval.sig') + (('broker-candidate',) if paired else ()):
            read_regular(directory / name, LIMIT, owner=0)
        verify_signature(directory / 'public.pem', directory / 'approval.json', directory / 'approval.sig')
        saved = strict_json(read_regular(directory / 'approval.json', 16384, owner=0))
        validate_approval(saved, now=int(time.time()), consumed=(directory / 'consumed.json').exists())
        if (saved != record or sha(read_regular(directory / 'candidate', LIMIT, owner=0)) != plan['candidate_sha256'] or
                sha(read_regular(directory / 'helper.sh', 131072, owner=0)) != plan['helper_sha256'] or
                (paired and sha(read_regular(directory / 'broker-candidate', LIMIT, owner=0)) != plan['broker_sha256'])):
            raise ValueError('sealed release changed: HOLD')
        deployment = broker_args[1] if broker_args is not None else str(int(time.time() * 1000)) + '-' + secrets.token_hex(6)
        require_unfenced(deployment)
        status = '/var/lib/mcpshka/deploys/' + deployment + '.json'
        # Consume before launch. Lost replies remain UNKNOWN; never authorize a retry.
        write_new(directory / 'consumed.json', canonical(dict(deployment_id=deployment, consumed_at=int(time.time()))))
        extra = ([str(directory / 'broker-candidate'), plan['broker_sha256'], plan['current_broker_sha256']] if paired else [])
        if broker_args is not None:
            restore_limits()
            os.execve(str(directory / 'helper.sh'),
                      [str(directory / 'helper.sh'), 'deploy', deployment, str(candidate_path(plan)),
                       plan['version'], plan['current_version'], status,
                       plan['candidate_sha256'], plan['current_sha256']] + extra, ENV)
            raise RuntimeError('exec unexpectedly returned')
        command = ['/usr/bin/systemd-run', '--unit=mcpshka-deploy-' + deployment, '--collect', '--no-block',
                   '--property=Type=oneshot', '--property=TimeoutStartSec=300s', '--property=UMask=0077',
                   '--property=MemoryMax=256M', '--property=CPUQuota=100%', '--property=TasksMax=64',
                   str(directory / 'helper.sh'), 'deploy', deployment, str(candidate_path(plan)),
                   plan['version'], plan['current_version'], status, plan['candidate_sha256'], plan['current_sha256']] + extra
        try:
            run(*command)
        except (subprocess.SubprocessError, OSError) as error:
            raise ValueError('launch UNKNOWN; reconcile deployment ' + deployment + '; do not replay') from error
        return dict(state='accepted', deployment_id=deployment, approval=str(directory),
                    candidate_sha256=plan['candidate_sha256'], status_path=status)
    finally:
        os.close(lock)


def validate_receipt(receipt, plan):
    if isinstance(receipt, dict) and receipt.get('schema') == 'mcpshka.operator-verified-local/v1':
        return validate_local_receipt(receipt, plan)
    fields = {'schema', 'provider', 'repository', 'run_id', 'source_sha', 'head_sha', 'tree',
              'source_digest', 'conclusion', 'suites', 'artifact_id', 'artifact_archive_sha256', 'binary_sha256'}
    paired = plan.get('schema') == 'mcpshka.release-plan/v2'
    if paired:
        fields.add('broker_binary_sha256')
    suites = {'release', 'race', 'fuzz', 'build', 'release-arm64'}
    if (not isinstance(receipt, dict) or set(receipt) != fields or
            receipt['schema'] != ('mcpshka.operator-verified-ci/v2' if paired else 'mcpshka.operator-verified-ci/v1') or
            receipt['provider'] != 'github' or receipt['repository'] != 'dmitriy1ebedev/mcpshka' or
            type(receipt['run_id']) is not int or receipt['run_id'] <= 0 or receipt['run_id'] != plan['ci_run'] or
            receipt['tree'] != plan['source_tree'] or receipt['source_digest'] != plan['source_digest'] or
            receipt['binary_sha256'] != plan['candidate_sha256'] or
            (paired and receipt['broker_binary_sha256'] != plan['broker_sha256']) or
            receipt['conclusion'] != 'success' or
            receipt['suites'] != {name: 'success' for name in suites} or
            type(receipt['artifact_id']) is not int or receipt['artifact_id'] <= 0):
        raise ValueError('CI gates, source or artifact mismatch: HOLD')
    for field, size in [('source_sha', 40), ('head_sha', 40), ('artifact_archive_sha256', 64)]:
        if not isinstance(receipt[field], str) or not re.fullmatch('[a-f0-9]{' + str(size) + '}', receipt[field]):
            raise ValueError('invalid CI identity: HOLD')


def validate_local_receipt(receipt, plan):
    fields = {'schema', 'provider', 'repository', 'source_sha', 'head_sha', 'tree',
              'source_digest', 'build_permit', 'build_env_digest', 'binary_sha256',
              'broker_binary_sha256', 'test_evidence'}
    if (set(receipt) != fields or plan.get('schema') != 'mcpshka.release-plan/v2' or
            plan.get('ci_run') != 0 or receipt['provider'] != 'h96-local' or
            receipt['repository'] != 'dmitriy1ebedev/mcpshka' or
            receipt['head_sha'] != receipt['source_sha'] or
            receipt['tree'] != plan['source_tree'] or receipt['source_digest'] != plan['source_digest'] or
            receipt['binary_sha256'] != plan['candidate_sha256'] or
            receipt['broker_binary_sha256'] != plan['broker_sha256']):
        raise ValueError('local source or paired artifact mismatch: HOLD')
    for field, size in [('source_sha', 40), ('head_sha', 40), ('tree', 40),
                        ('source_digest', 64), ('build_permit', 32), ('build_env_digest', 64),
                        ('binary_sha256', 64), ('broker_binary_sha256', 64)]:
        if not isinstance(receipt[field], str) or not re.fullmatch('[a-f0-9]{' + str(size) + '}', receipt[field]):
            raise ValueError('invalid local identity: HOLD')
    ev = receipt['test_evidence']
    if (not isinstance(ev, dict) or set(ev) != {'kind', 'id', 'tree', 'conclusion', 'suites'} or
            not isinstance(ev['kind'], str) or ev['kind'] not in LOCAL_TEST_SUITES or
            not isinstance(ev['id'], str) or not re.fullmatch('[a-f0-9]{32}', ev['id']) or
            ev['tree'] != plan['source_tree'] or ev['conclusion'] != 'success'):
        raise ValueError('local test evidence mismatch: HOLD')
    if ev['suites'] != {name: 'success' for name in LOCAL_TEST_SUITES[ev['kind']]}:
        raise ValueError('local test suites incomplete: HOLD')
    if ev['id'] == receipt['build_permit']:
        raise ValueError('build permit cannot be its own test evidence: HOLD')
    verify_local_authority(receipt)


def verify_local_authority(receipt):
    """The receipt is only a claim: the root executor's sealed copy and the
    binaries it built must say the same, or nothing is deployed."""
    directory = LOCAL_RELEASES / receipt['build_permit']
    try:
        info = os.lstat(directory)
    except OSError as error:
        raise ValueError('sealed local build missing: HOLD') from error
    if (not stat.S_ISDIR(info.st_mode) or info.st_uid != LOCAL_OWNER or
            stat.S_IMODE(info.st_mode) != 0o700):
        raise ValueError('unsafe sealed local build: HOLD')
    sealed = strict_json(read_regular(directory / 'receipt.json', 8192, owner=LOCAL_OWNER))
    if not isinstance(sealed, dict) or set(sealed) != set(receipt) or any(
            sealed[k] != receipt[k] for k in receipt if k != 'test_evidence'):
        raise ValueError('receipt differs from sealed local build: HOLD')
    for name, field in (('controller', 'binary_sha256'), ('broker', 'broker_binary_sha256')):
        if sha(read_regular(directory / name, LIMIT, owner=LOCAL_OWNER)) != receipt[field]:
            raise ValueError('sealed local binary mismatch: HOLD')


def validate_broker_shape(args):
    if (len(args) != 6 or args[0] != 'deploy' or
            not re.fullmatch('[0-9]{13}-[a-f0-9]{12}', args[1]) or
            Path(args[2]).parent != REPO / 'bin' or
            not re.fullmatch(r'mcpshka-0\.[0-9]{1,3}\.[0-9]{1,6}\.[a-f0-9]{12}\.candidate', Path(args[2]).name) or
            args[5] != '/var/lib/mcpshka/deploys/' + args[1] + '.json'):
        raise ValueError('invalid fixed native deploy request: HOLD')


def validate_broker_request(args, plan):
    validate_broker_shape(args)
    validate_plan(plan)
    if (args[2] != str(candidate_path(plan)) or args[3] != plan['version'] or
            args[4] != plan['current_version']):
        raise ValueError('native deploy differs from approved plan: HOLD')
    return args[1]


_SAVED_LIMITS = {}


def guard_limits():
    for which, soft in ((resource.RLIMIT_AS, 384 << 20), (resource.RLIMIT_CPU, 120)):
        old = resource.getrlimit(which)
        _SAVED_LIMITS[which] = old
        hard = old[1]
        if hard != resource.RLIM_INFINITY:
            soft = min(soft, hard)
        resource.setrlimit(which, (soft, hard))


def restore_limits():
    for which, old in _SAVED_LIMITS.items():
        resource.setrlimit(which, old)


def broker_deploy(args):
    require_operator()
    validate_broker_shape(args)
    require_unfenced(args[1])
    # Soft limits guard this Python only; restore_limits() restores them
    # before execve, or the Go retention worker of the deploy inherits a
    # 384 MiB address space and dies reserving page summaries (MCP-142).
    guard_limits()
    plan_file = Path(args[2] + '.release-plan.json')
    plan = strict_json(read_regular(plan_file, 16384))
    validate_broker_request(args, plan)
    # This fixed broker request carries delegated operator authority. Companion
    # evidence alone cannot trigger a grant or choose privileged executable code.
    return approve_and_deploy(plan_file, Path(args[2] + '.ci-receipt.json'),
                              sha(canonical(plan)), broker_args=args)


def write_broker_failure(args):
    validate_broker_shape(args)
    helper = Path('/usr/local/libexec/mcpshka-state-store.py')
    read_regular(helper, 131072, owner=0)
    record = dict(deployment_id=args[1], state='failed', target_version=args[3],
                  current_version=args[4], finished_at=time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()))
    subprocess.run(['/usr/bin/python3', '-I', '-S', str(helper), 'write', '/var/lib/mcpshka',
                    'deploys/' + args[1] + '.json'], input=canonical(record), check=True, timeout=10,
                   env=ENV, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)


# Retention is part of the fixed root release path, never a service-user grant.
GATE_FILES = {'approval.json', 'ci-receipt.json', 'candidate', 'broker-candidate',
              'helper.sh', 'public.pem', 'approval.sig', 'consumed.json'}


def retention_directory(path, owner):
    info = path.lstat()
    if (not stat.S_ISDIR(info.st_mode) or info.st_uid != owner or
            stat.S_IMODE(info.st_mode) != 0o700 or Path(os.path.realpath(path)) != path):
        raise ValueError('unsafe retention directory: HOLD')
    return info


def gate_inventory(directory, owner):
    info = retention_directory(directory, owner)
    entries = list(directory.iterdir())
    if len(entries) > len(GATE_FILES):
        raise ValueError('partial or active gate: KEEP')
    inventory = {}
    for entry in entries:
        st = entry.lstat()
        if (entry.name not in GATE_FILES or not stat.S_ISREG(st.st_mode) or
                st.st_uid != owner or st.st_nlink != 1 or st.st_mode & 0o022 or
                st.st_dev != info.st_dev):
            raise ValueError('unsafe or active gate contents: KEEP')
        inventory[entry.name] = (st.st_dev, st.st_ino, st.st_size, st.st_mtime_ns, st.st_ctime_ns)
    if not (GATE_FILES - {'broker-candidate'}).issubset(inventory):
        raise ValueError('incomplete sealed gate: KEEP')
    return inventory


def completed_gate(directory, owner):
    approval = strict_json(read_regular(directory / 'approval.json', 16384, owner=owner))
    plan = approval['plan']
    validate_plan(plan)
    if plan['schema'] == 'mcpshka.release-plan/v2':
        read_regular(directory / 'broker-candidate', LIMIT, owner=owner)
    if (approval.get('schema') != 'mcpshka.release-approval/v1' or
            approval.get('plan_sha256') != directory.name or sha(canonical(plan)) != directory.name):
        raise ValueError('gate plan binding: KEEP')
    consumed = strict_json(read_regular(directory / 'consumed.json', 4096, owner=owner))
    deployment = consumed['deployment_id']
    if not isinstance(deployment, str) or not re.fullmatch('[0-9]{13}-[a-f0-9]{12}', deployment):
        raise ValueError('invalid gate deployment: KEEP')
    receipt = strict_json(read_deployment(deployment))
    if (not isinstance(receipt, dict) or receipt.get('deployment_id') != deployment or
            receipt.get('state') not in ('succeeded', 'failed', 'rolled_back') or
            not isinstance(receipt.get('finished_at'), str) or
            not re.fullmatch(r'[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:.]+Z', receipt['finished_at']) or
            receipt.get('target_version') != plan['version'] or
            receipt.get('current_version') != plan['current_version']):
        raise ValueError('unfinished or unbound receipt: KEEP')
    return plan


def active_gate_paths(authority, proc=Path('/proc')):
    entries = list(proc.iterdir())
    if len(entries) > 4096:
        raise ValueError('gate launcher observation budget: HOLD')
    active = set()
    prefix = str(authority) + '/'
    for entry in entries:
        if not entry.name.isdigit():
            continue
        try:
            with (entry / 'cmdline').open('rb') as stream:
                raw = stream.read(32769)
        except (FileNotFoundError, ProcessLookupError):
            # The process exited between listing /proc and reading it (ESRCH
            # on a busy host): it can no longer hold a gate path.
            continue
        if len(raw) > 32768:
            raise ValueError('gate launcher arguments budget: HOLD')
        for argument in raw.split(b'\0'):
            arg = os.fsdecode(argument)
            if arg.startswith(prefix):
                name = arg[len(prefix):].split('/', 1)[0]
                if re.fullmatch('[a-f0-9]{64}', name):
                    active.add(name)
    return active


def prune_gate_storage(authority, pins, keep=6, dry_run=False, owner=0):
    if type(keep) is not int or not 1 <= keep <= 100:
        raise ValueError('gate retention requires 1..100')
    if not pins or any(not isinstance(p, str) or not re.fullmatch('[a-f0-9]{64}', p) for p in pins):
        raise ValueError('installed and rollback pins required')
    retention_directory(authority, owner)
    entries = list(authority.iterdir())
    if len(entries) > 4096:
        raise ValueError('gate retention inventory budget: HOLD')
    report = dict(dry_run=dry_run, keep=keep, deleted=0, deleted_bytes=0,
                  would_delete=0, would_delete_bytes=0, items=[])
    # mtime is policy, name is a deterministic tie-breaker. Protected entries
    # are additional to the newest N: a protected old gate never evicts a new one.
    directories = []
    for directory in entries:
        if re.fullmatch('[a-f0-9]{64}', directory.name):
            directories.append((directory.lstat().st_mtime_ns, directory.name, directory))
    directories.sort(reverse=True)
    newest = {name for _, name, _ in directories[:keep]}
    active = active_gate_paths(authority)
    mounts = [line.split()[4] for line in Path('/proc/self/mountinfo').read_text().splitlines()]
    for _, name, directory in directories:
        item = dict(path=str(directory), action='keep', reason='newest retained gates', bytes=0)
        report['items'].append(item)
        if name in active:
            item['reason'] = 'active release launcher'
            continue
        try:
            inventory = gate_inventory(directory, owner)
            plan = completed_gate(directory, owner)
            # A broker may be shared by many versions; the controller identifies
            # the release directory that production or rollback actually needs.
            images = {plan['candidate_sha256']}
            if images & pins:
                item['reason'] = 'production or rollback image'
                continue
            if name in newest:
                continue
            if any(m == str(directory) or m.startswith(str(directory) + '/') for m in mounts):
                raise ValueError('mounted gate: KEEP')
            item.update(action='delete', reason='completed gate outside retention',
                        bytes=sum(x[2] for x in inventory.values()))
            report['would_delete'] += 1
            report['would_delete_bytes'] += item['bytes']
            if dry_run:
                continue
            # The authority lock excludes launchers; descriptor-relative unlink
            # also refuses aliases and a changed inventory before the first write.
            fd = os.open(directory, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
            try:
                named = directory.lstat()
                opened = os.fstat(fd)
                if ((named.st_dev, named.st_ino) != (opened.st_dev, opened.st_ino) or
                        gate_inventory(directory, owner) != inventory):
                    raise ValueError('gate changed: KEEP')
                for file, metadata in inventory.items():
                    os.unlink(file, dir_fd=fd)
                    report['deleted_bytes'] += metadata[2]
                os.fsync(fd)
                os.rmdir(directory)
                sync_directory(authority)
            finally:
                os.close(fd)
            report['deleted'] += 1
        except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError):
            item.update(action='keep', reason='active, partial, corrupt or unsafe gate')
    return report


def release_source_proof(plan, receipt_raw, controller_sha256, broker_sha256):
    validate_plan(plan)
    if (plan['schema'] != 'mcpshka.release-plan/v2' or
            sha(receipt_raw) != plan['ci_receipt_sha256'] or
            controller_sha256 != plan['candidate_sha256'] or
            broker_sha256 != plan['broker_sha256']):
        raise ValueError('paired production source mismatch: HOLD')
    receipt = strict_json(receipt_raw)
    validate_receipt(receipt, plan)
    return dict(schema='mcpshka.production-source/v1', source_sha=receipt['source_sha'],
                tree=receipt['tree'], source_digest=receipt['source_digest'],
                version=plan['version'], ci_run=plan['ci_run'],
                controller_sha256=controller_sha256, broker_sha256=broker_sha256,
                ci_receipt_sha256=plan['ci_receipt_sha256'])


def production_proof(deployment):
    """Read signed provenance under the existing deploy lock; never grants or deploys."""
    require_operator()
    if not isinstance(deployment, str) or not re.fullmatch('[0-9]{13}-[a-f0-9]{12}', deployment):
        raise ValueError('invalid production deployment identity: HOLD')
    fd = os.open(DEPLOY_LOCK, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        info = os.fstat(fd)
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != LOCK_UID or
                info.st_nlink != 1 or stat.S_IMODE(info.st_mode) != 0o600):
            raise ValueError('unsafe deployment lock: HOLD')
        fcntl.flock(fd, fcntl.LOCK_SH | fcntl.LOCK_NB)
        retention_directory(AUTHORITY, 0)
        with os.scandir(AUTHORITY) as scan:
            entries = []
            for item in scan:
                if len(entries) == 128:
                    raise ValueError('production authority scan budget: HOLD')
                entries.append(Path(item.path))
        for directory in entries:
            if not re.fullmatch('[a-f0-9]{64}', directory.name):
                continue
            retention_directory(directory, 0)
            try:
                consumed = strict_json(read_regular(directory / 'consumed.json', 4096, owner=0))
            except FileNotFoundError:
                continue
            if consumed.get('deployment_id') != deployment:
                continue
            approval_raw = read_regular(directory / 'approval.json', 16384, owner=0)
            approval = strict_json(approval_raw)
            plan = approval.get('plan')
            validate_plan(plan)
            if (approval.get('schema') != 'mcpshka.release-approval/v1' or
                    approval.get('plan_sha256') != directory.name or
                    sha(canonical(plan)) != directory.name):
                raise ValueError('production approval binding invalid: HOLD')
            read_regular(directory / 'public.pem', 4096, owner=0)
            read_regular(directory / 'approval.sig', 4096, owner=0)
            verify_signature(directory / 'public.pem', directory / 'approval.json', directory / 'approval.sig')
            if approval_raw != read_regular(directory / 'approval.json', 16384, owner=0):
                raise ValueError('production approval changed: HOLD')
            proof = release_source_proof(plan,
                read_regular(directory / 'ci-receipt.json', 16384, owner=0),
                sha(read_regular('/usr/local/bin/mcpshka', LIMIT, owner=0)),
                sha(read_regular('/usr/local/libexec/mcpshka-broker', LIMIT, owner=0)))
            proof['deployment_id'] = deployment
            return proof
        raise ValueError('sealed production deployment unavailable: HOLD')
    finally:
        os.close(fd)


def retention_pins(version=None, deployment=None):
    current = sha(read_regular('/usr/local/bin/mcpshka', LIMIT, owner=0))
    if version is not None:
        if not re.fullmatch(r'0\.[0-9]{1,3}\.[0-9]{1,6}', version):
            raise ValueError('invalid retention version')
        plan = strict_json(read_regular(REPO / 'bin' / ('mcpshka-coordinated-release-' + version + '.json'), 16384, owner=0))
        operation = plan['operation_id']
        if not re.fullmatch('[a-f0-9]{32}', operation) or plan['version'] != version:
            raise ValueError('invalid successful release binding')
        directory = SESSION_ROOT / 'coordinated-upgrades' / operation
        retention_directory(directory, 0)
        events = sorted(directory.glob('[0-9]*.json'))
        if not 0 < len(events) <= 128:
            raise ValueError('release journal unavailable')
        event = strict_json(read_regular(events[-1], 2048, owner=0))
        if event.get('state') != 'SUCCEEDED' or event.get('schema') != 'mcpshka.paired-upgrade-audit/v1':
            raise ValueError('release is unfinished: no retention')
        rollback = sha(read_regular(directory / 'controller.previous', LIMIT, owner=0))
        broker = sha(read_regular('/usr/local/libexec/mcpshka-broker', LIMIT, owner=0))
        previous_broker = sha(read_regular(directory / 'broker.previous', LIMIT, owner=0))
        if (current, rollback, broker, previous_broker) != (
                plan['controller_sha256'], plan['current_controller_sha256'],
                plan['broker_sha256'], plan['current_broker_sha256']):
            raise ValueError('release/rollback identity changed: no retention')
        return {current, rollback}
    if not isinstance(deployment, str) or not re.fullmatch('[0-9]{13}-[a-f0-9]{12}', deployment):
        raise ValueError('successful deployment required')
    receipt = strict_json(read_deployment(deployment))
    if not isinstance(receipt, dict) or receipt.get('deployment_id') != deployment or receipt.get('state') != 'succeeded' or not receipt.get('finished_at'):
        raise ValueError('deployment unfinished: no retention')
    previous = sha(read_regular('/usr/local/bin/mcpshka.previous', LIMIT, owner=0))
    # Locate the sealed approval rather than trusting caller-supplied pins.
    for directory in AUTHORITY.iterdir():
        if not re.fullmatch('[a-f0-9]{64}', directory.name):
            continue
        try:
            retention_directory(directory, 0)
            consumed = strict_json(read_regular(directory / 'consumed.json', 4096, owner=0))
            if consumed.get('deployment_id') != deployment:
                continue
            plan = completed_gate(directory, 0)
            if (plan['candidate_sha256'], plan['current_sha256']) != (current, previous):
                raise ValueError('deployment images changed')
            pins = {current, previous}
            if plan['schema'] == 'mcpshka.release-plan/v2':
                for path, key in (('/usr/local/libexec/mcpshka-broker', 'broker_sha256'),
                                  ('/usr/local/libexec/mcpshka-broker.previous', 'current_broker_sha256')):
                    image = sha(read_regular(path, LIMIT, owner=0))
                    if image != plan[key]:
                        raise ValueError('broker image changed')
            return pins
        except (OSError, ValueError, KeyError, TypeError):
            continue
    raise ValueError('successful sealed deployment unavailable')


def retain_gates(version=None, deployment=None, keep=6, dry_run=False):
    require_operator()
    # Hold both existing locks; nonblocking acquisition defers on contention.
    # Sandbox properties and service-user privileges stay unchanged.
    deploy_fd = os.open(DEPLOY_LOCK, os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    gate_fd = None
    try:
        st = os.fstat(deploy_fd)
        if not stat.S_ISREG(st.st_mode) or st.st_uid != 0 or st.st_nlink != 1 or stat.S_IMODE(st.st_mode) != 0o600:
            raise ValueError('unsafe retention lock')
        fcntl.flock(deploy_fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        pins = retention_pins(version, deployment)
        if not AUTHORITY.exists():
            return dict(dry_run=dry_run, keep=keep, deleted=0, deleted_bytes=0,
                        would_delete=0, would_delete_bytes=0, items=[])
        retention_directory(AUTHORITY, 0)
        gate_fd = os.open(AUTHORITY / '.lock', os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
        st = os.fstat(gate_fd)
        if not stat.S_ISREG(st.st_mode) or st.st_uid != 0 or st.st_nlink != 1 or stat.S_IMODE(st.st_mode) != 0o600:
            raise ValueError('unsafe approval lock')
        fcntl.flock(gate_fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        report = prune_gate_storage(AUTHORITY, pins, keep, dry_run)
        print('release gate retention: ' + json.dumps({k: v for k, v in report.items() if k != 'items'}), file=sys.stderr)
        return report
    finally:
        if gate_fd is not None:
            os.close(gate_fd)
        os.close(deploy_fd)


def main():
    if len(sys.argv) > 1 and sys.argv[1] == 'deploy':
        try:
            broker_deploy(sys.argv[1:])
        except DeploymentFenced:
            raise  # Keep the original historical receipt intact.
        except (OSError, ValueError, subprocess.SubprocessError):
            try:
                write_broker_failure(sys.argv[1:])
            except (OSError, ValueError, subprocess.SubprocessError):
                pass  # The original accepted status remains UNKNOWN; inspect its unit.
            raise
        return
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['check-plan', 'approve-deploy', 'check-fence', 'retire-deployment', 'retain-gates', 'production-proof'])
    parser.add_argument('--plan', type=Path)
    parser.add_argument('--version')
    parser.add_argument('--keep', type=int, default=6)
    parser.add_argument('--dry-run', action='store_true')
    parser.add_argument('--deployment-id')
    parser.add_argument('--expected-receipt-sha256')
    parser.add_argument('--expected-current-sha256')
    parser.add_argument('--reason')
    parser.add_argument('--session-id')
    parser.add_argument('--owner')
    parser.add_argument('--ci-receipt', type=Path)
    parser.add_argument('--approved-plan-sha256')
    args = parser.parse_args()
    if args.action == 'production-proof':
        print(json.dumps(production_proof(args.deployment_id)))
        return
    if args.action == 'retain-gates':
        if bool(args.version) == bool(args.deployment_id):
            parser.error('exactly one successful --version or --deployment-id required')
        report = retain_gates(args.version, args.deployment_id, args.keep, args.dry_run)
        print(json.dumps({k: v for k, v in report.items() if k != 'items'}))
        return
    if args.action in ('check-fence', 'retire-deployment'):
        if not args.deployment_id:
            parser.error('--deployment-id is required')
        if args.action == 'check-fence':
            if deployment_fenced(args.deployment_id):
                raise DeploymentFenced('deployment permanently FENCED: HOLD')
            return
        if not all((args.expected_receipt_sha256, args.expected_current_sha256, args.reason, args.session_id, args.owner)):
            parser.error('exact receipt/current pins, reason and Face ID administrative session/owner required')
        print(json.dumps(retire_deployment(args.deployment_id, args.expected_receipt_sha256,
                                         args.expected_current_sha256, args.reason, args.session_id, args.owner)))
        return
    if args.plan is None:
        parser.error('--plan is required')
    if args.action == 'check-plan':
        plan = strict_json(read_regular(args.plan, 16384))
        validate_plan(plan)
        print(json.dumps(dict(state='UNTRUSTED_PLAN', plan_sha256=sha(canonical(plan)), deploy_authorized=False)))
    else:
        if args.ci_receipt is None:
            parser.error('--ci-receipt is required')
        print(json.dumps(approve_and_deploy(args.plan, args.ci_receipt, args.approved_plan_sha256)))


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
