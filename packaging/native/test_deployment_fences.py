import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('fence_gate', Path(__file__).with_name('release-gate.py'))
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)
REAL_SCAN = gate.assert_no_legacy_launchers
ID = '1792159092900-1e327387d562'
SESSION = 'e' * 32


class DeploymentFences(unittest.TestCase):
    def test_new_session_admitted_within_its_authorization_second(self):
        now = 1792170759.75
        permit = dict(schema='mcpshka.admin-session/v1', id=SESSION, owner='owner',
                      auth_method='webauthn', policy='mcpshka-administration/v1',
                      state='active', generation=1,
                      authorized_at='2026-10-16T17:12:39.500000Z')
        # Exercise admission's actual timestamp argument, not just the reader.
        with tempfile.TemporaryDirectory() as tmp, \
                patch.object(gate, 'SESSION_ROOT', Path(tmp)), \
                patch.object(gate, 'require_operator'), \
                patch.object(gate.time, 'time', return_value=now), \
                patch.object(gate, 'retirement_approval', wraps=gate.retirement_approval), \
                patch.object(gate, 'read_regular', return_value=gate.canonical(permit)), \
                patch.object(gate, 'fence_deployment', return_value={'state': 'FENCED'}):
            if os.geteuid() != 0:
                from types import SimpleNamespace
                real_fstat = os.fstat
                def fstat(fd):
                    info = real_fstat(fd)
                    values = {k: getattr(info, k) for k in dir(info) if k.startswith('st_')}
                    values['st_uid'] = 0
                    return SimpleNamespace(**values)
                with patch.object(gate.os, 'fstat', side_effect=fstat):
                    self.assertEqual(gate.retire_deployment(ID, 'a'*64, 'b'*64,
                                     'reviewed', SESSION, 'owner')['state'], 'FENCED')
            else:
                self.assertEqual(gate.retire_deployment(ID, 'a'*64, 'b'*64,
                                 'reviewed', SESSION, 'owner')['state'], 'FENCED')

    def fence(self):
        return dict(schema='mcpshka.deployment-fence/v1', deployment_id=ID,
                    receipt_sha256='a' * 64, observed_current_sha256='b' * 64,
                    gate_sha256='c' * 64, helper_sha256='d' * 64,
                    authorization_session_id=SESSION, fenced_at=int(time.time()), reason='historical UNKNOWN')

    def test_fence_strict_schema_and_identity(self):
        good = self.fence()
        gate.validate_fence(good, ID)
        for field, value in [('schema', 'other'), ('deployment_id', '../bad'),
                             ('fenced_at', True), ('fenced_at', int(time.time()) + 10),
                             ('receipt_sha256', 'A' * 64), ('authorization_session_id', ''),
                             ('reason', 'bad\nreason'), ('reason', 'я' * 101)]:
            with self.subTest(field=field), self.assertRaises(ValueError):
                gate.validate_fence(dict(good, **{field: value}), ID)
        with self.assertRaises(ValueError):
            gate.validate_fence(dict(good, command='replay'), ID)

    def test_general_session_has_no_job_length_limit_and_rejects_ha_consent(self):
        permit = dict(schema='mcpshka.admin-session/v1', id=SESSION, owner='owner',
                      auth_method='webauthn', policy='mcpshka-administration/v1', state='active',
                      generation=1, authorized_at='2026-01-01T00:00:00Z')
        with patch.object(gate, 'read_regular', return_value=gate.canonical(permit)):
            self.assertEqual(gate.retirement_approval(SESSION, 'owner', 1792170759), permit)
            with self.assertRaises(ValueError):
                gate.retirement_approval(SESSION, 'another-owner', 1792170759)
        for change in [dict(state='revoked'), dict(policy='homeassistant_automations'),
                       dict(generation=True), dict(auth_method='totp'), dict(owner='other'),
                       dict(authorized_at='2027-01-01T00:00:00Z')]:
            with patch.object(gate, 'read_regular', return_value=gate.canonical(dict(permit, **change))):
                with self.subTest(change=change), self.assertRaises(ValueError):
                    gate.retirement_approval(SESSION, 'owner', 1792170759)

    def test_missing_directory_is_not_a_fence_but_corruption_holds(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(gate, 'FENCES', Path(tmp) / 'fences'):
            self.assertFalse(gate.deployment_fenced(ID))
            with patch.object(gate, 'fence_directory', side_effect=ValueError('unsafe directory')):
                with self.assertRaises(ValueError):
                    gate.deployment_fenced(ID)

    def test_fenced_or_unreadable_admission_never_rewrites_original_receipt(self):
        args = ['deploy', ID, '/root/repos/mcpshka/bin/mcpshka-0.1.128.abcdef123456.candidate',
                '0.1.128', '0.1.127', '/var/lib/mcpshka/deploys/' + ID + '.json']
        for result in [True, ValueError('corrupt fence'), PermissionError('unreadable')]:
            kwargs = {'side_effect': result} if isinstance(result, Exception) else {'return_value': result}
            with patch.object(gate.sys, 'argv', ['gate'] + args), patch.object(gate, 'require_operator'), \
                    patch.object(gate, 'deployment_fenced', **kwargs), \
                    patch.object(gate, 'approve_and_deploy') as launch, \
                    patch.object(gate, 'write_broker_failure') as overwrite:
                with self.assertRaises(gate.DeploymentFenced):
                    gate.main()
                launch.assert_not_called()
                overwrite.assert_not_called()

    def test_retirement_is_permanent_pinned_and_preserves_original_receipt(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            receipt = b'{"deployment_id":"' + ID.encode() + b'","state":"accepted"}'
            helper = b'flock -n 9\n' + gate.FENCE_CHECK + b'\n'
            real_read, real_fstat = gate.read_regular, os.fstat
            def read(path, limit, owner=None):
                if path == gate.INSTALLED_GATE:
                    return b'fence-aware-installed-gate'
                if path == gate.HELPER:
                    return helper
                if str(path) == '/usr/local/bin/mcpshka':
                    return b'production'
                with patch.object(gate.os, 'fstat', real_fstat):
                    return real_read(path, limit, owner=os.geteuid() if owner == 0 else owner)
            def fstat(fd):
                info = real_fstat(fd)
                from types import SimpleNamespace
                values = {k: getattr(info, k) for k in dir(info) if k.startswith('st_')}
                values['st_uid'] = 0
                return SimpleNamespace(**values)
            def directory(create=False):
                if create:
                    gate.FENCES.mkdir(mode=0o755, exist_ok=True)
                if not gate.FENCES.exists():
                    raise FileNotFoundError()
            with patch.object(gate, 'INSTALLED_GATE', Path(gate.__file__).resolve()), \
                    patch.object(gate, 'FENCES', root / 'fences'), \
                    patch.object(gate, 'DEPLOY_LOCK', root / 'deployment.lock'), \
                    patch.object(gate, 'AUTHORITY', root / 'authority'), \
                    patch.object(gate, 'private_directory', side_effect=lambda p: p.mkdir(mode=0o700, exist_ok=True)), \
                    patch.object(gate, 'assert_no_legacy_launchers'), \
                    patch.object(gate, 'read_regular', side_effect=read), \
                    patch.object(gate, 'read_deployment', return_value=receipt) as original, \
                    patch.object(gate.os, 'fstat', side_effect=fstat), \
                    patch.object(gate, 'fence_directory', side_effect=directory), \
                    patch.object(gate, 'run', return_value=b'LoadState=not-found\nActiveState=inactive\nSubState=dead\n'):
                expected = (ID, gate.sha(receipt), gate.sha(b'production'), 'historical UNKNOWN', SESSION)
                with patch.object(gate, 'run', return_value=b'LoadState=loaded\nActiveState=active\nSubState=running\n'):
                    with self.assertRaisesRegex(ValueError, 'unit not proven absent'):
                        gate.fence_deployment(*expected)
                self.assertFalse((gate.FENCES / (ID + '.json')).exists())
                old = gate.AUTHORITY / ('f' * 64)
                old.mkdir(mode=0o700)
                old_helper = old / 'helper.sh'
                old_helper.write_bytes(b'#!/bin/bash\nread -r line\n')
                old_helper.chmod(0o700)
                real_disable = gate.disable_legacy_helpers
                delayed = []
                def start_between_scan_and_barrier(deployment):
                    delayed.append(subprocess.Popen([str(old_helper)], stdin=subprocess.PIPE,
                                                    stdout=subprocess.PIPE, stderr=subprocess.PIPE))
                    return real_disable(deployment)
                try:
                    with patch.object(gate, 'disable_legacy_helpers', side_effect=start_between_scan_and_barrier), \
                            patch.object(gate, 'assert_no_legacy_launchers', wraps=REAL_SCAN):
                        with self.assertRaisesRegex(ValueError, 'legacy deployment launcher'):
                            gate.fence_deployment(*expected)
                    self.assertFalse((gate.FENCES / (ID + '.json')).exists())
                    self.assertEqual(old_helper.stat().st_mode & 0o111, 0)
                finally:
                    for process in delayed:
                        process.communicate(b'finish\n', timeout=3)
                original.reset_mock()
                result = gate.fence_deployment(*expected)
                self.assertEqual(result['state'], 'FENCED')
                self.assertFalse(result['retry_allowed'])
                saved = (gate.FENCES / (ID + '.json')).read_bytes()
                self.assertEqual(gate.fence_deployment(*expected), {k: v for k, v in result.items() if k != 'legacy_helpers_disabled'})
                self.assertEqual((gate.FENCES / (ID + '.json')).read_bytes(), saved)
                self.assertEqual(original.call_count, 1)
                with self.assertRaisesRegex(ValueError, 'different pins'):
                    gate.fence_deployment(ID, '0' * 64, *expected[2:])
                self.assertTrue(gate.deployment_fenced(ID))
                self.assertEqual(json.loads(saved)['receipt_sha256'], gate.sha(receipt))

    def test_queued_legacy_exec_cannot_start_after_execution_barrier(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            sealed = root / ('a' * 64)
            sealed.mkdir(mode=0o700)
            helper = sealed / 'helper.sh'
            original = b'#!/bin/bash\nprintf UNEXPECTED_EFFECT\n'
            helper.write_bytes(original)
            helper.chmod(0o700)
            (sealed / 'consumed.json').write_bytes(gate.canonical(dict(deployment_id=ID)))
            (sealed / 'consumed.json').chmod(0o600)
            real_read, real_fstat = gate.read_regular, os.fstat
            def read(path, limit, owner=None):
                with patch.object(gate.os, 'fstat', real_fstat):
                    return real_read(path, limit, owner=os.geteuid() if owner == 0 else owner)
            def fstat(fd):
                info = real_fstat(fd)
                from types import SimpleNamespace
                values = {k: getattr(info, k) for k in dir(info) if k.startswith('st_')}
                values['st_uid'] = 0
                return SimpleNamespace(**values)
            with patch.object(gate, 'AUTHORITY', root), patch.object(gate, 'private_directory'), \
                    patch.object(gate, 'read_regular', side_effect=read), \
                    patch.object(gate.os, 'fstat', side_effect=fstat):
                inventory = gate.disable_legacy_helpers(ID)
            self.assertEqual(inventory, [dict(path=str(helper), sha256=gate.sha(original))])
            self.assertEqual(helper.read_bytes(), original)
            with self.assertRaises(PermissionError):
                subprocess.run([str(helper)], capture_output=True, timeout=3)

    def test_waiting_retirement_worker_keeps_gate_scanner_enforced(self):
        with tempfile.TemporaryDirectory() as tmp:
            proc = Path(tmp)
            parent = proc / '999999999'
            parent.mkdir()
            arguments = [b'/usr/bin/systemd-run', b'--wait',
                         b'/usr/local/libexec/mcpshka-broker', b'retirement-worker',
                         b'retire-deployment']
            (parent / 'cmdline').write_bytes(b'\0'.join(arguments) + b'\0')
            gate.assert_no_legacy_launchers(proc)
            arguments[2] = b'/usr/local/libexec/mcpshka-self-deploy'
            (parent / 'cmdline').write_bytes(b'\0'.join(arguments) + b'\0')
            with self.assertRaisesRegex(ValueError, 'legacy deployment launcher'):
                gate.assert_no_legacy_launchers(proc)

    def test_old_sealed_launcher_blocks_retirement_even_before_unit_exists(self):
        with tempfile.TemporaryDirectory() as tmp:
            authority = Path(tmp) / 'authority'
            delayed = subprocess.Popen(['/bin/bash', '-c', 'read -r line; : "$1"',
                                        'delayed', str(authority / ('a' * 64) / 'helper.sh')],
                                       stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            try:
                self.assertIsNone(delayed.poll())
                with patch.object(gate, 'AUTHORITY', authority):
                    with self.assertRaisesRegex(ValueError, 'legacy deployment launcher'):
                        gate.assert_no_legacy_launchers()
            finally:
                delayed.communicate(b'finish\n', timeout=3)
            self.assertEqual(delayed.returncode, 0)

    def test_delayed_helper_refuses_before_effects_and_lock_conflict_retains_receipt(self):
        source = Path(__file__).with_name('self-deploy.sh').read_text()
        # Run only the real admission prefix; no production mutation body is present.
        prefix = source[:source.index('# Lock order matches candidate retirement')]
        with tempfile.TemporaryDirectory() as tmp:
            lock = Path(tmp) / 'lock'
            script = prefix.replace('LOCK=/run/mcpshka-self-deploy.lock', 'LOCK=' + str(lock))
            script = script.replace(gate.FENCE_CHECK.decode(), 'false || exit 75')
            script += '\nprintf "UNEXPECTED_EFFECT"\n'
            args = ['/bin/bash', '-c', script, 'test', 'deploy', ID,
                    '/root/repos/mcpshka/bin/mcpshka-0.1.128.test.candidate', '0.1.128', '0.1.127',
                    '/var/lib/mcpshka/deploys/' + ID + '.json']
            import fcntl
            for busy in (False, True):
                with lock.open('w') as holder:
                    if busy:
                        fcntl.flock(holder, fcntl.LOCK_EX | fcntl.LOCK_NB)
                    result = subprocess.run(args, capture_output=True, timeout=3)
                self.assertEqual(result.returncode, 75, result.stderr)
                self.assertEqual(result.stdout, b'')


if __name__ == '__main__':
    unittest.main()
