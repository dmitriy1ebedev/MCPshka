#!/usr/bin/python3 -I
"""Unprivileged bounded CI artifact preparation; never installs or executes it."""
import hashlib
import io
import json
import os
from pathlib import Path
import re
import runpy
import shutil
import stat
import struct
import sys
import time
import urllib.parse
import urllib.request
import zipfile

MAX = 64 << 20  # BUG-20261023-80: the controller outgrew 32 MiB
REPO = Path('/root/repos/mcpshka')
HELPER = Path('/usr/local/libexec/mcpshka-self-deploy-guarded')
NAMES = {'checks.log', 'mcpshka-linux-arm64', 'release-arm64-buildinfo.txt',
         'release-arm64-sha256.txt', 'source-identity.json', 'source-sha.txt', 'source-tree.txt', 'toolchain.txt'}
PAIRED = {'mcpshka-broker-linux-arm64', 'broker-arm64-sha256.txt', 'broker-arm64-buildinfo.txt'}


def version_key(version):
    # 0.2.1 > 0.1.196: compare all parts, not only the last one.
    return tuple(int(part) for part in version.split('.'))

def sha(data):
    return hashlib.sha256(data).hexdigest()

def validate_url(url):
    if not isinstance(url, str) or len(url) > 8192 or any(ord(c) <= 32 for c in url):
        raise ValueError('invalid artifact URL')
    p = urllib.parse.urlsplit(url)
    if (p.scheme != 'https' or not p.hostname or not p.hostname.endswith('.oaiusercontent.com')
            or p.username or p.password or p.port not in (None, 443) or p.fragment):
        raise ValueError('artifact host rejected')

class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        raise ValueError('artifact redirect rejected')

def download(url):
    validate_url(url)
    deadline = time.monotonic() + 60
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    with opener.open(url, timeout=15) as response:
        out = bytearray()
        while True:
            part = response.read(min(65536, MAX + 1 - len(out)))
            out.extend(part)
            if len(out) > MAX or time.monotonic() > deadline:
                raise ValueError('artifact resource limit')
            if not part:
                return bytes(out)

def unpack(raw, source_sha, digest, tree):
    if not 22 <= len(raw) <= MAX:
        raise ValueError('invalid archive size')
    end = raw.rfind(b'PK\x05\x06', max(0, len(raw)-65557))
    if end < 0 or end + 22 > len(raw):
        raise ValueError('invalid ZIP directory')
    _, disk, cd_disk, entries_disk, entries, size, offset, comment = struct.unpack_from('<4s4H2LH', raw, end)
    if (disk or cd_disk or entries not in (8, 11) or entries_disk != entries or size > 16384
            or offset + size != end or end + 22 + comment != len(raw)):
        raise ValueError('unsupported ZIP directory')
    with zipfile.ZipFile(io.BytesIO(raw)) as archive:
        infos = archive.infolist()
        if len(infos) not in (8, 11) or {i.filename for i in infos} not in (NAMES, NAMES | PAIRED):
            raise ValueError('unexpected artifact entries')
        for item in infos:
            kind = stat.S_IFMT(item.external_attr >> 16)
            if (kind not in (0, stat.S_IFREG) or item.flag_bits & 1
                    or item.compress_type not in (zipfile.ZIP_STORED, zipfile.ZIP_DEFLATED)
                    or not 0 < item.file_size <= (MAX if item.filename in ('mcpshka-linux-arm64', 'mcpshka-broker-linux-arm64') else 65536)):
                raise ValueError('unsafe artifact entry')
        values = {i.filename: archive.read(i) for i in infos}
    identity = json.loads(values['source-identity.json'])
    if (set(identity) != {'schema', 'source_digest', 'file_count', 'total_bytes'}
            or identity['schema'] != 'mcpshka.source-identity/v1'
            or identity['source_digest'] != digest
            or any(type(identity[k]) is not int or not 0 < identity[k] <= 100000000 for k in ('file_count','total_bytes'))
            or values['source-sha.txt'].decode().strip() != source_sha
            or values['source-tree.txt'].decode().strip() != tree
            or not re.fullmatch('[a-f0-9]{40}', tree)):
        raise ValueError('artifact source mismatch')
    binary = values['mcpshka-linux-arm64']
    checksum = values['release-arm64-sha256.txt'].decode().split()
    build = values['release-arm64-buildinfo.txt'].decode()
    if ((len(checksum) != 2 or checksum[0] != sha(binary) or Path(checksum[1]).name != 'mcpshka-linux-arm64') or len(binary) < 64
            or binary[:6] != b'\x7fELF\x02\x01' or struct.unpack_from('<H', binary, 18)[0] != 183
            or 'GOARCH=arm64' not in build or 'CGO_ENABLED=1' not in build):
        raise ValueError('artifact binary mismatch')
    broker = None
    if 'mcpshka-broker-linux-arm64' in values:
        broker = values['mcpshka-broker-linux-arm64']
        broker_sum = values['broker-arm64-sha256.txt'].decode().split()
        broker_build = values['broker-arm64-buildinfo.txt'].decode()
        if (len(broker_sum) != 2 or broker_sum[0] != sha(broker)
                or Path(broker_sum[1]).name != 'mcpshka-broker-linux-arm64'
                or len(broker) < 64 or broker[:6] != b'\x7fELF\x02\x01'
                or struct.unpack_from('<H', broker, 18)[0] != 183
                or 'GOARCH=arm64' not in broker_build or 'CGO_ENABLED=1' not in broker_build):
            raise ValueError('privileged broker binary mismatch')
    return binary, identity, broker

def write_new(path, data, mode=0o600):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode)
    with os.fdopen(fd, 'wb') as stream:
        stream.write(data)
        stream.flush()
        os.fsync(stream.fileno())

def write_verified(path, data, mode=0o600):
    try:
        write_new(path, data, mode)
    except FileExistsError:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        with os.fdopen(fd, 'rb') as stream:
            info = os.fstat(stream.fileno())
            if (not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or
                    info.st_uid != os.geteuid() or info.st_mode & 0o077 or
                    info.st_size != len(data) or stream.read(len(data)+1) != data):
                raise ValueError('existing release file conflicts; HOLD')

def prepare(request, url):
    gate = runpy.run_path(str(Path(__file__).with_name('release-gate.py')))
    if set(request) != {'version', 'current_version', 'source_digest', 'receipt'}:
        raise ValueError('invalid preparation fields')
    for name in ('version', 'current_version'):
        if not isinstance(request[name], str) or not re.fullmatch(r'0\.[0-9]{1,3}\.[0-9]{1,6}', request[name]):
            raise ValueError('invalid version')
    if not re.fullmatch('[a-f0-9]{64}', request['source_digest']):
        raise ValueError('invalid source digest')
    receipt = dict(request['receipt'])
    paired = version_key(request['version']) >= (0, 1, 134)
    expected_binary = receipt.get('binary_sha256')
    expected_broker = receipt.get('broker_binary_sha256')
    if expected_binary is None:
        receipt['binary_sha256'] = '0'*64
    if paired:
        if receipt.get('schema') != 'mcpshka.operator-verified-ci/v2':
            raise ValueError('paired release requires operator-verified v2 receipt')
        if expected_broker is None:
            receipt['broker_binary_sha256'] = '0'*64
    # Validate every receipt field before network access, using a temporary matching plan.
    plan = dict(schema='mcpshka.release-plan/v2' if paired else 'mcpshka.release-plan/v1', version=request['version'],
                current_version=request['current_version'], source_digest=request['source_digest'],
                source_tree=receipt.get('tree'), ci_run=receipt.get('run_id'),
                candidate_sha256=receipt.get('binary_sha256'), current_sha256='0'*64,
                helper_sha256='0'*64, ci_receipt_sha256='0'*64)
    if paired:
        plan.update(broker_sha256=receipt['broker_binary_sha256'], current_broker_sha256='0'*64)
    gate['validate_plan'](plan)
    gate['validate_receipt'](receipt, plan)
    space = shutil.disk_usage(REPO)
    if space.free - (96 << 20) < max(3 << 30, space.total // 10):
        raise ValueError('insufficient free space')
    if url is None:
        source = os.environ.pop('MCPSHKA_RELEASE_FILE')
        if not re.fullmatch(r'/var/lib/mcpshka/artifact-inbox/[a-f0-9]{32}\.blob', source):
            raise ValueError('invalid artifact inbox path')
        fd = os.open(source, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        try:
            info = os.fstat(fd)
            if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_mode & 0o077 or not 22 <= info.st_size <= MAX:
                raise ValueError('unsafe artifact inbox file')
            with os.fdopen(fd, 'rb', closefd=False) as stream:
                raw = stream.read(MAX + 1)
            after = os.fstat(fd)
            if len(raw) != info.st_size or (info.st_dev, info.st_ino, info.st_mtime_ns, info.st_ctime_ns) != (after.st_dev, after.st_ino, after.st_mtime_ns, after.st_ctime_ns):
                raise ValueError('artifact inbox file changed')
        finally:
            os.close(fd)
    else:
        raw = download(url)
    if sha(raw) != receipt['artifact_archive_sha256']:
        raise ValueError('archive digest mismatch')
    binary, identity, broker = unpack(raw, receipt['source_sha'], request['source_digest'], receipt['tree'])
    if version_key(request['version']) >= (0, 1, 134) and broker is None:
        raise ValueError('paired release requires same-tree broker binary')
    if expected_binary is not None and sha(binary) != expected_binary:
        raise ValueError('binary digest mismatch')
    if paired and (broker is None or expected_broker is not None and sha(broker) != expected_broker):
        raise ValueError('paired broker artifact mismatch')
    receipt['binary_sha256'] = sha(binary)
    plan['candidate_sha256'] = sha(binary)
    if paired:
        receipt['broker_binary_sha256'] = sha(broker)
        plan['broker_sha256'] = sha(broker)
    gate['validate_receipt'](receipt, plan)
    receipt_raw = gate['canonical'](receipt)
    plan.update(current_sha256=sha(gate['read_regular'](Path('/usr/local/bin/mcpshka'),128<<20,owner=0)),
                helper_sha256=sha(gate['read_regular'](HELPER,1<<20,owner=0)),
                ci_receipt_sha256=sha(receipt_raw))
    if paired:
        plan['current_broker_sha256'] = sha(gate['read_regular'](Path('/usr/local/libexec/mcpshka-broker'),128<<20,owner=0))
    gate['validate_plan'](plan)
    candidate = REPO / 'bin' / ('mcpshka-' + request['version'] + '.' + sha(binary)[:12] + '.candidate')
    # Exclusive files survive UNKNOWN; inspect them rather than silently overwriting.
    write_verified(candidate, binary, 0o600)
    write_verified(Path(str(candidate)+'.ci-receipt.json'), receipt_raw)
    plan_raw = gate['canonical'](plan)
    write_verified(Path(str(candidate)+'.release-plan.json'), plan_raw)
    candidate.chmod(0o700)
    if broker is not None:
        broker_path = REPO / 'bin' / ('mcpshka-broker-' + request['version'] + '.' + sha(broker)[:12] + '.candidate')
        write_verified(broker_path, broker)
        broker_path.chmod(0o700)
        paired = dict(schema='mcpshka.coordinated-upgrade/v1',
                      operation_id=sha((receipt['source_sha']+receipt['artifact_archive_sha256']).encode())[:32],
                      version=request['version'], current_version=request['current_version'],
                      ci_run=receipt['run_id'], source_tree=receipt['tree'],
                      controller_sha256=sha(binary), broker_sha256=sha(broker),
                      helper_sha256=sha(gate['read_regular'](Path(__file__).with_name('coordinated-upgrade.py'), 65536)),
                      gate_sha256=sha(gate['read_regular'](Path(__file__).with_name('release-gate.py'), 65536)),
                      guarded_helper_sha256=sha(gate['read_regular'](Path(__file__).with_name('self-deploy.sh'), 65536)),
                      current_controller_sha256=plan['current_sha256'],
                      current_broker_sha256=sha(gate['read_regular'](Path('/usr/local/libexec/mcpshka-broker'),128<<20,owner=0)),
                      current_gate_sha256=sha(gate['read_regular'](Path('/usr/local/libexec/mcpshka-self-deploy'), 65536,owner=0)),
                      current_guarded_sha256=sha(gate['read_regular'](HELPER, 65536,owner=0)),
                      controller_candidate=str(candidate), broker_candidate=str(broker_path))
        paired_path = REPO / 'bin' / ('mcpshka-coordinated-release-' + request['version'] + '.json')
        write_verified(paired_path, gate['canonical'](paired))
    gate['sync_directory'](candidate.parent)
    return dict(schema='mcpshka.release-prepared/v1', candidate=str(candidate),
                plan_sha256=sha(plan_raw), binary_sha256=sha(binary), version=request['version'],
                source_identity=identity, trust='UNTRUSTED', deploy_authorized=False)

if __name__ == '__main__':
    try:
        request = json.loads(sys.argv[1])
        url = os.environ.pop('MCPSHKA_RELEASE_URL', None)
        if (url is None) == ('MCPSHKA_RELEASE_FILE' not in os.environ):
            raise ValueError('exactly one artifact source required')
        print(json.dumps(prepare(request, url), sort_keys=True))
    except Exception as error:
        # Never print exception strings: URL errors may embed signed artifact URLs.
        # Emit only a fixed, audited stage and exception class for diagnosis.
        allowed = (ValueError, OSError, KeyError, TypeError, zipfile.BadZipFile,
                   json.JSONDecodeError)
        kind = type(error).__name__ if type(error) in allowed else 'OtherError'
        print('release preparation failed [' + kind + ']; reconcile job and candidate before retry', file=sys.stderr)
        sys.exit(1)
