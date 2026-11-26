"""Fixed local daemon operations, embedded in the paired privileged broker."""
import json
import os
from pathlib import Path
import re
import socket
import stat
import struct
import sys
import time

CWD = '/root/repos/mcpshka'
SOCKET = Path('/root/.codex/app-server-control/app-server-control.sock')


def identifier(value):
    return isinstance(value, str) and re.fullmatch(r'[A-Za-z0-9_-]{8,128}', value)


def peer_matches(uid, trusted, peer):
    # The managed package may belong to its installer; this path is never executed.
    return bool(uid == 0 and stat.S_ISREG(trusted.st_mode) and trusted.st_mode & 0o111
                and not trusted.st_mode & 0o022
                and (peer.st_dev, peer.st_ino) == (trusted.st_dev, trusted.st_ino))


class RPC:
    def __enter__(self):
        from websockets.sync.client import connect
        for path in (SOCKET.parent.parent, SOCKET.parent):
            info = path.lstat()
            if not stat.S_ISDIR(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022:
                raise ValueError('unsafe daemon endpoint parent')
        target = SOCKET.resolve(strict=True)
        info, parent = target.stat(), target.parent.stat()
        if (target.parent != Path('/tmp/codex-daemon-0') or not stat.S_ISSOCK(info.st_mode)
                or info.st_uid != 0 or stat.S_IMODE(info.st_mode) != 0o600
                or parent.st_uid != 0 or stat.S_IMODE(parent.st_mode) != 0o700):
            raise ValueError('unexpected daemon endpoint')
        sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        try:
            sock.settimeout(5)
            sock.connect(str(SOCKET))
            pid, uid, _ = struct.unpack('3i', sock.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))
            executable = Path('/root/.local/bin/codex').resolve(strict=True)
            trusted = executable.stat()
            peer = Path('/proc/' + str(pid) + '/exe').stat()
            if not peer_matches(uid, trusted, peer):
                raise ValueError('daemon peer does not match installed Codex')
            self.ws = connect('ws://localhost/', sock=sock, open_timeout=5,
                              max_size=2*1024*1024, compression=None)
            self.seq = 0
            self.call('initialize', {'clientInfo': {'name': 'mcpshka-session', 'version': '0.1.139'},
                                     'capabilities': {'experimentalApi': True}})
            self.ws.send(json.dumps({'method': 'initialized', 'params': {}}))
            return self
        except BaseException:
            sock.close()
            raise

    def __exit__(self, *_):
        self.ws.close()

    def call(self, method, params):
        self.seq += 1
        self.ws.send(json.dumps({'id': self.seq, 'method': method, 'params': params}))
        deadline = time.monotonic() + 8
        for _ in range(128):
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise TimeoutError('daemon deadline')
            response = json.loads(self.ws.recv(timeout=remaining))
            if response.get('id') == self.seq:
                if 'error' in response:
                    raise ValueError('daemon RPC rejected')
                return response['result']
        raise ValueError('daemon notification budget')


def isolated_config(rpc):
    config = rpc.call('config/read', {'cwd': CWD, 'includeLayers': False})['config']
    overrides = {'apps._default.enabled': False, 'apps._default.destructive_enabled': False,
                 'apps._default.open_world_enabled': False}
    servers = []
    for section in ('mcp_servers', 'plugins', 'apps'):
        values = config.get(section, {})
        if not isinstance(values, dict) or len(values) > 128:
            raise ValueError('daemon configuration inventory unavailable or exceeds bound')
        for name, value in values.items():
            if section == 'apps' and name == '_default' and value is None:
                continue
            if not isinstance(name, str) or not re.fullmatch(r'[A-Za-z0-9_.@/-]{1,128}', name) or not isinstance(value, dict):
                raise ValueError('unsupported daemon configuration entry')
            overrides[section + '.' + json.dumps(name) + '.enabled'] = False
            if section == 'mcp_servers':
                servers.append(name)
    if sum(len(name.encode()) for name in servers) > 4096:
        raise ValueError('server identity budget exceeded')
    return overrides, servers


def metadata(result, expected=None):
    thread = result['thread']
    thread_id, session_id = thread.get('id'), thread.get('sessionId')
    if not identifier(thread_id) or (expected and thread_id != expected):
        raise ValueError('daemon session identity unavailable')
    session_valid = bool(identifier(session_id))
    # Keep a returned thread ID recoverable even when other metadata is incomplete.
    if not session_valid:
        session_id = ''
    sandbox = result.get('sandbox')
    if not isinstance(sandbox, dict):
        sandbox = {}
    return {'thread_id': thread_id, 'codex_session_id': session_id,
            'policy_verified': session_valid and result.get('cwd') == CWD and thread.get('cwd') == CWD
            and result.get('approvalPolicy') == 'never'
            and sandbox.get('type') == 'readOnly'
            and sandbox.get('networkAccess', False) is False}


def create(rpc):
    overrides, servers = isolated_config(rpc)
    result = rpc.call('thread/start', {'cwd': CWD, 'approvalPolicy': 'never',
                                     'sandbox': 'read-only', 'ephemeral': False,
                                     'config': overrides, 'serviceName': 'mcpshka'})
    out = metadata(result)
    out['disabled_servers'] = servers
    return out


def probe(rpc, thread_id, servers):
    thread = rpc.call('thread/read', {'threadId': thread_id, 'includeTurns': False})['thread']
    if thread.get('id') != thread_id or thread.get('cwd') != CWD or thread.get('status', {}).get('type') != 'idle':
        raise ValueError('owned thread is not loaded and idle')
    page = rpc.call('mcpServerStatus/list', {'threadId': thread_id, 'limit': 128})
    data = page.get('data')
    if not isinstance(data, list) or len(data) > 128 or page.get('nextCursor'):
        raise ValueError('thread tool inventory incomplete')
    if not set(servers).issubset({entry.get('name') for entry in data}):
        raise ValueError('disabled server configuration not confirmed')
    for entry in data:
        if entry.get('runtimeStatus') != 'disabled' or entry.get('tools') or entry.get('resources') or entry.get('resourceTemplates') or entry.get('toolsError'):
            raise ValueError('thread tool isolation not confirmed')
    return {'thread_id': thread_id, 'isolated': True}


def load(rpc, thread_id):
    thread = rpc.call('thread/read', {'threadId': thread_id, 'includeTurns': False})['thread']
    if thread.get('id') != thread_id or thread.get('cwd') != CWD or thread.get('status', {}).get('type') not in ('idle', 'notLoaded'):
        raise ValueError('owned thread is busy or identity changed')
    overrides, servers = isolated_config(rpc)
    result = rpc.call('thread/resume', {'threadId': thread_id, 'excludeTurns': True,
                                      'cwd': CWD, 'sandbox': 'read-only',
                                      'approvalPolicy': 'never', 'config': overrides})
    out = metadata(result, thread_id)
    out['disabled_servers'] = servers
    return out


def main():
    raw = sys.stdin.buffer.read(8193)
    if len(raw) > 8192:
        raise ValueError('input budget')
    args = json.loads(raw)
    with RPC() as rpc:
        if args == {'action': 'create'}:
            out = create(rpc)
        elif set(args) == {'action', 'thread_id'} and args['action'] == 'load' and identifier(args['thread_id']):
            out = load(rpc, args['thread_id'])
        elif set(args) == {'action', 'thread_id', 'servers'} and args['action'] == 'probe' and identifier(args['thread_id']) and isinstance(args['servers'], list) and len(args['servers']) <= 128:
            out = probe(rpc, args['thread_id'], args['servers'])
        else:
            raise ValueError('fixed operation required')
    print(json.dumps(out))


if __name__ == '__main__':
    try:
        main()
    except Exception:
        print('Codex session operation unconfirmed; inspect durable intent', file=sys.stderr)
        sys.exit(1)
