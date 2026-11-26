import importlib.util
import hashlib
import json
from contextlib import closing
import os
from pathlib import Path
import sqlite3
import subprocess
import tempfile
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location('release_gate', Path(__file__).with_name('release-gate.py'))
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)



class InheritedLimitTests(unittest.TestCase):
    # MCP-142: the deploy execve must not hand a 384 MiB address space to
    # the Go retention worker.
    def test_guard_is_soft_and_restored(self):
        import resource
        before = {w: resource.getrlimit(w) for w in (resource.RLIMIT_AS, resource.RLIMIT_CPU)}
        child = subprocess.run(['/usr/bin/python3', '-I', '-c',
            'import importlib.util,resource,sys;'
            's=importlib.util.spec_from_file_location("g",sys.argv[1]);g=importlib.util.module_from_spec(s);s.loader.exec_module(g);'
            'old=resource.getrlimit(resource.RLIMIT_AS);g.guard_limits();'
            'mid=resource.getrlimit(resource.RLIMIT_AS);g.restore_limits();'
            'print(old==resource.getrlimit(resource.RLIMIT_AS), mid[0]==384<<20, mid[1]==old[1])',
            str(Path(__file__).with_name('release-gate.py'))], capture_output=True, text=True, timeout=30)
        self.assertEqual(child.stdout.split(), ['True', 'True', 'True'], child.stderr)
        self.assertEqual(before, {w: resource.getrlimit(w) for w in before})


class GateRetentionTests(unittest.TestCase):
    def fixture(self, root, number, state='succeeded', image=None, broker=None):
        plan = ReleaseGateTests().plan()
        plan.update(version='0.1.' + str(number + 10), current_version='0.1.' + str(number + 9),
                    candidate_sha256=image or hashlib.sha256(str(number).encode()).hexdigest())
        if broker is not None:
            plan.update(schema='mcpshka.release-plan/v2', broker_sha256=broker,
                        current_broker_sha256='4' * 64)
        name = gate.sha(gate.canonical(plan))
        directory = root / name
        directory.mkdir(mode=0o700)
        approval = dict(schema='mcpshka.release-approval/v1', plan=plan, plan_sha256=name,
                        approved_at=1, expires_at=601)
        deployment = '1792159092900-' + format(number, '012x')
        for file, value in [('approval.json', approval),
                            ('consumed.json', dict(deployment_id=deployment, consumed_at=2))]:
            (directory / file).write_bytes(gate.canonical(value))
            (directory / file).chmod(0o600)
        (directory / 'candidate').write_bytes(b'image')
        (directory / 'candidate').chmod(0o600)
        for file in ('ci-receipt.json', 'helper.sh', 'public.pem', 'approval.sig'):
            (directory / file).write_bytes(b'sealed')
            (directory / file).chmod(0o600)
        if broker is not None:
            (directory / 'broker-candidate').write_bytes(b'broker')
            (directory / 'broker-candidate').chmod(0o600)
        os.utime(directory, (number, number))
        receipt = dict(deployment_id=deployment, state=state, target_version=plan['version'],
                       current_version=plan['current_version'], finished_at='2026-10-25T01:00:00Z')
        return directory, receipt, plan

    def test_shared_broker_does_not_pin_obsolete_controller_releases(self):
        from unittest.mock import patch
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            old, receipt, _ = self.fixture(root, 1, broker='3' * 64)
            _, newer, _ = self.fixture(root, 2, broker='3' * 64)
            records = {x['deployment_id']: x for x in (receipt, newer)}
            with patch.object(gate, 'read_deployment', side_effect=lambda key: gate.canonical(records[key])):
                report = gate.prune_gate_storage(root, {'1' * 64, '2' * 64, '3' * 64, 'b' * 64},
                                                 keep=1, dry_run=True, owner=os.geteuid())
            self.assertEqual(report['would_delete'], 1)
            self.assertEqual([x['path'] for x in report['items'] if x['action'] == 'delete'], [str(old)])

    def test_active_gate_paths_preserves_launchers_even_after_terminal_receipt(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            authority = base / 'gates'
            proc = base / 'proc'
            pid = proc / '123'
            pid.mkdir(parents=True)
            path = authority / ('a' * 64) / 'helper.sh'
            (pid / 'cmdline').write_bytes(b'/bin/bash\0' + str(path).encode() + b'\0deploy\0')
            self.assertEqual(gate.active_gate_paths(authority, proc), {'a' * 64})

    def test_active_gate_paths_skips_a_process_that_exits_mid_scan(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            authority = base / 'gates'
            proc = base / 'proc'
            (proc / '7').mkdir(parents=True)
            (proc / '7' / 'cmdline').write_bytes(b'/bin/sh\0' + str(authority / ('b' * 64) / 'x').encode() + b'\0')
            (proc / '9').mkdir()
            real_open = Path.open

            def vanishing(self, *args, **kwargs):
                if self.parent.name == '9':
                    raise ProcessLookupError(3, 'No such process')
                return real_open(self, *args, **kwargs)
            with mock.patch.object(Path, 'open', vanishing):
                self.assertEqual(gate.active_gate_paths(authority, proc), {'b' * 64})

    def test_gate_retention_mtime_count_pins_and_unknown(self):
        from unittest.mock import patch
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            records, directories = {}, []
            for n in range(1, 11):
                directory, receipt, plan = self.fixture(root, n)
                directories.append(directory)
                records[receipt['deployment_id']] = receipt
            current = self.fixture(root, 20, image='1' * 64)
            rollback = self.fixture(root, 21, image='2' * 64)
            active = self.fixture(root, 22, state='running')
            for directory, receipt, _ in (current, rollback, active):
                records[receipt['deployment_id']] = receipt
                os.utime(directory, (0, 0))
            (directories[0] / '.lock').write_bytes(b'')
            os.utime(directories[0], (0, 0))
            with patch.object(gate, 'read_deployment', side_effect=lambda key: gate.canonical(records[key])):
                report = gate.prune_gate_storage(root, {'1' * 64, '2' * 64}, keep=6,
                                                 dry_run=True, owner=os.geteuid())
                deleted = {Path(x['path']) for x in report['items'] if x['action'] == 'delete'}
                self.assertEqual(deleted, set(directories[1:4]))
                self.assertEqual(report['deleted'], 0)
                self.assertGreater(report['would_delete_bytes'], 0)
                self.assertTrue(all(x.exists() for x in directories))
                applied = gate.prune_gate_storage(root, {'1' * 64, '2' * 64}, keep=6,
                                                  owner=os.geteuid())
                self.assertEqual(applied['deleted'], 3)
                self.assertEqual(applied['deleted_bytes'], report['would_delete_bytes'])
                self.assertTrue(all(x[0].exists() for x in (current, rollback, active)))
                self.assertTrue(directories[0].exists())

    def test_gate_retention_preserves_partial_corrupt_and_aliased(self):
        from unittest.mock import patch
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            directory, receipt, _ = self.fixture(root, 1)
            (directory / 'approval.json').write_bytes(b'broken')
            partial = root / ('f' * 64)
            partial.mkdir()
            alias = root / ('e' * 64)
            alias.symlink_to(directory, target_is_directory=True)
            special, special_receipt, _ = self.fixture(root, 2)
            (special / 'link').symlink_to(directory / 'candidate')
            with patch.object(gate, 'read_deployment', return_value=gate.canonical(receipt)):
                report = gate.prune_gate_storage(root, {'1' * 64, '2' * 64}, keep=1,
                                                 owner=os.geteuid())
            self.assertEqual(report['deleted'], 0)
            self.assertTrue(all(x.exists() for x in (directory, partial, alias, special)))

    def test_gate_retention_requires_terminal_matching_receipt(self):
        from unittest.mock import patch
        for change in ({'state': 'accepted'}, {'finished_at': ''},
                       {'deployment_id': 'different'}, {'target_version': '0.1.999'}):
            with self.subTest(change=change), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                directory, receipt, _ = self.fixture(root, 1)
                _, newer, _ = self.fixture(root, 2)
                receipt.update(change)
                with patch.object(gate, 'read_deployment', side_effect=lambda key: gate.canonical(
                        newer if key == newer['deployment_id'] else receipt)):
                    report = gate.prune_gate_storage(root, {'1' * 64, '2' * 64}, keep=1,
                                                     owner=os.geteuid())
                self.assertEqual(report['deleted'], 0)
                self.assertTrue(directory.exists())

class ReleaseGateTests(unittest.TestCase):
    def test_sqlite_receipt_with_real_limit_report(self):
        with tempfile.TemporaryDirectory() as tmp:
            database = Path(tmp) / 'fixture.db'
            raw = b'{"state":"accepted"}'
            with closing(sqlite3.connect(database)) as connection:
                connection.execute('CREATE TABLE records(payload BLOB, sha256 TEXT)')
                connection.execute('INSERT INTO records VALUES (?,?)', (raw, hashlib.sha256(raw).hexdigest()))
                connection.commit()
            result = subprocess.run(['/usr/bin/sqlite3', '-readonly', '-safe', '-batch', '-bail',
                '-noheader', '-cmd', '.limit length 262144', str(database),
                "SELECT hex(payload)||'|'||sha256 FROM records;"], check=True, capture_output=True, timeout=5)
            self.assertEqual(gate.decode_deployment_query(result.stdout), raw)

    def test_sqlite_receipt_rejects_changed_limits_rows_and_hashes(self):
        raw = b'{"state":"accepted"}'
        row = raw.hex().encode() + b'|' + hashlib.sha256(raw).hexdigest().encode()
        for output in (row, b'length 524288\n' + row,
                       b'length 262144\n' + row + b'\n' + row,
                       b'length 262144\n' + row[:-1] + b'x'):
            with self.subTest(output=output), self.assertRaises(ValueError):
                gate.decode_deployment_query(output)

    def plan(self):
        return dict(schema='mcpshka.release-plan/v1', version='0.1.110',
                    candidate_sha256='a' * 64, current_sha256='b' * 64,
                    current_version='0.1.109', source_tree='c' * 40,
                    source_digest='d' * 64, ci_run=38743038805,
                    ci_receipt_sha256='e' * 64, helper_sha256='f' * 64)

    def test_plan_strict_identity(self):
        p = self.plan()
        gate.validate_plan(p)
        for field, value in [('candidate_sha256', 'A' * 64), ('ci_run', True),
                             ('version', '../evil'), ('schema', 'other'),
                             ('current_version', p['version']), ('source_tree', 'a' * 39)]:
            with self.subTest(field=field), self.assertRaises(ValueError):
                gate.validate_plan(dict(p, **{field: value}))
        with self.assertRaises(ValueError):
            gate.validate_plan(dict(p, command='/bin/sh'))

    def test_paired_v2_plan_and_broker_ci_binding(self):
        p=self.plan()
        p.update(schema='mcpshka.release-plan/v2',broker_sha256='1'*64,current_broker_sha256='2'*64)
        gate.validate_plan(p)
        receipt=self.receipt(p)
        receipt.update(schema='mcpshka.operator-verified-ci/v2',broker_binary_sha256=p['broker_sha256'])
        gate.validate_receipt(receipt,p)
        for k,v in [('broker_binary_sha256','9'*64),('schema','mcpshka.operator-verified-ci/v1')]:
            with self.subTest(key=k), self.assertRaises(ValueError):
                gate.validate_receipt(dict(receipt,**{k:v}),p)
        with self.assertRaises(ValueError):
            gate.validate_plan(dict(p,broker_sha256='invalid'))
        with self.assertRaises(ValueError):
            gate.validate_plan(dict(p,unexpected='root'))

    def test_regular_file_rejects_alias_permissions_and_budget(self):
        with tempfile.TemporaryDirectory() as tmp:
            p = Path(tmp) / 'image'
            p.write_bytes(b'candidate')
            p.chmod(0o600)
            self.assertEqual(gate.read_regular(p, 100), b'candidate')
            with self.assertRaises(ValueError):
                gate.read_regular(p, 2)
            with self.assertRaises(ValueError):
                gate.read_regular(p, 100, owner=os.geteuid() + 1)
            p.chmod(0o660)
            with self.assertRaises(ValueError):
                gate.read_regular(p, 100, owner=os.geteuid())
            p.chmod(0o666)
            with self.assertRaises(ValueError):
                gate.read_regular(p, 100)
            p.chmod(0o600)
            alias = Path(tmp) / 'alias'
            alias.symlink_to(p)
            with self.assertRaises(OSError):
                gate.read_regular(alias, 100)
            alias.unlink()
            os.link(p, alias)
            with self.assertRaises(ValueError):
                gate.read_regular(p, 100)

    def test_signature_rejects_changed_plan_signature_and_key(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            key, public = root / 'key', root / 'public'
            subprocess.run(['/usr/bin/openssl', 'genpkey', '-algorithm', 'ED25519',
                            '-out', str(key)], check=True, capture_output=True)
            subprocess.run(['/usr/bin/openssl', 'pkey', '-in', str(key), '-pubout',
                            '-out', str(public)], check=True, capture_output=True)
            payload = root / 'plan'
            payload.write_bytes(gate.canonical(self.plan()))
            sig = root / 'sig'
            subprocess.run(['/usr/bin/openssl', 'pkeyutl', '-sign', '-rawin',
                            '-inkey', str(key), '-in', str(payload), '-out', str(sig)],
                           check=True, capture_output=True)
            gate.verify_signature(public, payload, sig)
            payload.write_bytes(gate.canonical(dict(self.plan(), ci_run=123)))
            with self.assertRaises(ValueError):
                gate.verify_signature(public, payload, sig)
            payload.write_bytes(gate.canonical(self.plan()))
            sig.write_bytes(b'bad')
            with self.assertRaises(ValueError):
                gate.verify_signature(public, payload, sig)

    def test_approval_is_expiring_one_time_and_plan_bound(self):
        p = self.plan()
        record = dict(plan=p, plan_sha256=gate.sha(gate.canonical(p)), approved_at=100,
                      expires_at=700, schema='mcpshka.release-approval/v1')
        gate.validate_approval(record, now=101, consumed=False)
        for modified, now, consumed in [
                (record, 700, False), (record, 99, False), (record, 101, True),
                (dict(record, plan_sha256='0' * 64), 101, False),
                (dict(record, expires_at=10000), 101, False)]:
            with self.subTest(now=now, consumed=consumed), self.assertRaises(ValueError):
                gate.validate_approval(modified, now=now, consumed=consumed)

    def test_non_root_cannot_authorize_or_launch(self):
        from unittest.mock import patch
        with patch.object(gate.os, 'geteuid', return_value=994):
            with self.assertRaisesRegex(ValueError, 'authorized root'):
                gate.require_operator()

    def test_duplicate_json_is_rejected(self):
        with self.assertRaises(ValueError):
            gate.strict_json(b'{"version":"0.1.110","version":"0.1.999"}')

    def test_launch_is_once_even_after_unknown_result(self):
        from unittest.mock import patch
        for lost, native in ((False, False), (True, False), (False, True)):
            with self.subTest(lost=lost, native=native), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                (root / 'bin').mkdir()
                (root / 'packaging/native').mkdir(parents=True)
                helper = root / 'packaging/native/self-deploy.sh'
                helper.write_bytes(b'fixture-helper')
                helper.chmod(0o600)
                p = self.plan()
                p['candidate_sha256'] = gate.sha(b'fixture-candidate')
                p['current_sha256'] = gate.sha(b'fixture-installed')
                p['helper_sha256'] = gate.sha(helper.read_bytes())
                receipt = root / 'receipt'
                receipt.write_bytes(gate.canonical(self.receipt(p)))
                receipt.chmod(0o600)
                p['ci_receipt_sha256'] = gate.sha(receipt.read_bytes())
                planfile = root / 'plan'
                planfile.write_bytes(gate.canonical(p))
                planfile.chmod(0o600)
                real_read, real_run, uid = gate.read_regular, gate.run, os.geteuid()
                calls = []
                real_fstat = os.fstat
                def fstat(fd):
                    info = real_fstat(fd)
                    if os.readlink('/proc/self/fd/' + str(fd)) == str(root / 'authority/.lock'):
                        from types import SimpleNamespace
                        fields = {name: getattr(info, name) for name in dir(info) if name.startswith('st_')}
                        fields['st_uid'] = 0
                        return SimpleNamespace(**fields)
                    return info
                def read(path, limit, owner=None):
                    if str(path) == '/usr/local/bin/mcpshka':
                        return b'fixture-installed'
                    return real_read(path, limit, owner=uid if owner == 0 else owner)
                def run(*args):
                    if args[0] == '/usr/bin/systemd-run':
                        calls.append(args)
                        if lost:
                            raise subprocess.TimeoutExpired(args, 30)
                        return b''
                    return real_run(*args)
                def directory(path):
                    path.mkdir(mode=0o700, exist_ok=True)
                def execve(path, argv, env):
                    calls.append(tuple(argv))
                    raise RuntimeError('NATIVE_EXEC_INTERCEPTED')
                previous_umask = os.umask(0o077)
                try:
                    with patch.object(gate, 'REPO', root), patch.object(gate, 'AUTHORITY', root / 'authority'), \
                            patch.object(gate, 'HELPER', helper), \
                            patch.object(gate, 'require_operator'), patch.object(gate, 'private_directory', side_effect=directory), \
                            patch.object(gate, 'read_regular', side_effect=read), patch.object(gate, 'run', side_effect=run), \
                            patch.object(gate.os, 'fstat', side_effect=fstat), \
                            patch.object(gate.os, 'execve', side_effect=execve):
                        image = gate.candidate_path(p)
                        image.write_bytes(b'fixture-candidate')
                        image.chmod(0o600)
                        pin = gate.sha(gate.canonical(p))
                        deployment = '1792087000000-abcdef123456'
                        kwargs = {'broker_args': ['deploy', deployment, str(image), p['version'], p['current_version'],
                                                  '/var/lib/mcpshka/deploys/' + deployment + '.json']} if native else {}
                        with self.assertRaisesRegex(ValueError, 'approved plan'):
                            gate.approve_and_deploy(planfile, receipt, '0' * 64, **kwargs)
                        self.assertFalse(calls)
                        if native:
                            with self.assertRaisesRegex(RuntimeError, 'NATIVE_EXEC_INTERCEPTED'):
                                gate.approve_and_deploy(planfile, receipt, pin, **kwargs)
                            self.assertEqual(calls[0][2], deployment)
                            self.assertTrue(calls[0][0].endswith('/helper.sh'))
                        elif lost:
                            with self.assertRaisesRegex(ValueError, 'UNKNOWN'):
                                gate.approve_and_deploy(planfile, receipt, pin, **kwargs)
                        else:
                            self.assertEqual(gate.approve_and_deploy(planfile, receipt, pin, **kwargs)['state'], 'accepted')
                        self.assertEqual(len(calls), 1)
                        self.assertEqual(calls[0][-2:], (p['candidate_sha256'], p['current_sha256']))
                        with self.assertRaises(FileExistsError):
                            gate.approve_and_deploy(planfile, receipt, pin, **kwargs)
                        self.assertEqual(len(calls), 1)
                        self.assertTrue((root / 'authority' / pin / 'consumed.json').is_file())
                        self.assertFalse((root / 'authority' / pin / 'signing-key.pem').exists())
                finally:
                    os.umask(previous_umask)


    def test_broker_request_is_bound_to_fixed_native_deploy(self):
        p = self.plan()
        deployment = '1792087000000-abcdef123456'
        candidate = str(gate.candidate_path(p))
        status = '/var/lib/mcpshka/deploys/' + deployment + '.json'
        args = ['deploy', deployment, candidate, p['version'], p['current_version'], status]
        self.assertEqual(gate.validate_broker_request(args, p), deployment)
        for index, replacement in [(0, 'rm'), (1, '../evil'), (2, '/tmp/other'),
                                   (3, '0.1.999'), (4, '0.1.100'), (5, '/tmp/status')]:
            modified = list(args)
            modified[index] = replacement
            with self.subTest(index=index), self.assertRaises(ValueError):
                gate.validate_broker_request(modified, p)
        with self.assertRaises(ValueError):
            gate.validate_broker_request(args + ['--arbitrary'], p)


    def receipt(self, p):
        return dict(schema='mcpshka.operator-verified-ci/v1', provider='github',
                    repository='dmitriy1ebedev/mcpshka', run_id=p['ci_run'],
                    source_sha='1' * 40, head_sha='2' * 40, tree=p['source_tree'],
                    source_digest=p['source_digest'], conclusion='success',
                    suites={name: 'success' for name in ('release', 'race', 'fuzz', 'build', 'release-arm64')},
                    artifact_id=123, artifact_archive_sha256='3' * 64,
                    binary_sha256=p['candidate_sha256'])

    def sealed_local_build(self, receipt, controller=b'controller-binary', broker=b'broker-binary'):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        old = gate.LOCAL_RELEASES, gate.LOCAL_OWNER
        self.addCleanup(lambda: (setattr(gate, 'LOCAL_RELEASES', old[0]), setattr(gate, 'LOCAL_OWNER', old[1])))
        gate.LOCAL_RELEASES, gate.LOCAL_OWNER = Path(tmp.name), os.geteuid()
        d = Path(tmp.name) / receipt['build_permit']
        d.mkdir(mode=0o700)
        # Go writes the build receipt with a zero test_evidence; the broker fills it later.
        sealed = dict(receipt, test_evidence=dict(kind='', id='', tree='', conclusion='', suites=None))
        for name, raw in (('receipt.json', json.dumps(sealed).encode()), ('controller', controller), ('broker', broker)):
            (d / name).write_bytes(raw)
            os.chmod(d / name, 0o400)
        return d

    def test_local_receipt_needs_the_sealed_build(self):
        p = dict(self.plan(), schema='mcpshka.release-plan/v2', ci_run=0,
                 candidate_sha256=hashlib.sha256(b'controller-binary').hexdigest(),
                 broker_sha256=hashlib.sha256(b'broker-binary').hexdigest(), current_broker_sha256='9' * 64)
        r = dict(schema='mcpshka.operator-verified-local/v1', provider='h96-local',
                 repository='dmitriy1ebedev/mcpshka', source_sha='1' * 40,
                 head_sha='1' * 40, tree=p['source_tree'], source_digest=p['source_digest'],
                 build_permit='a' * 32, build_env_digest='b' * 64,
                 binary_sha256=p['candidate_sha256'], broker_binary_sha256=p['broker_sha256'],
                 test_evidence=dict(kind='module', id='c' * 32, tree=p['source_tree'],
                                    conclusion='success', suites={'module': 'success'}))
        d = self.sealed_local_build(r)
        gate.validate_receipt(r, p)
        with self.assertRaisesRegex(ValueError, 'own test evidence: HOLD'):
            gate.validate_receipt(dict(r, test_evidence=dict(r['test_evidence'], id=r['build_permit'])), p)
        os.chmod(d / 'controller', 0o600)
        (d / 'controller').write_bytes(b'tampered')
        os.chmod(d / 'controller', 0o400)
        with self.assertRaisesRegex(ValueError, 'binary mismatch: HOLD'):
            gate.validate_receipt(r, p)
        d = self.sealed_local_build(dict(r, build_env_digest='e' * 64))
        with self.assertRaisesRegex(ValueError, 'differs from sealed: HOLD|differs from sealed local build: HOLD'):
            gate.validate_receipt(r, p)
        os.chmod(d, 0o755)
        with self.assertRaisesRegex(ValueError, 'unsafe sealed local build: HOLD'):
            gate.validate_receipt(r, p)
        with self.assertRaisesRegex(ValueError, 'sealed local build missing: HOLD'):
            gate.validate_receipt(dict(r, build_permit='d' * 32), p)

    def test_local_receipt_paired_and_strict(self):
        p = dict(self.plan(), schema='mcpshka.release-plan/v2', ci_run=0,
                 broker_sha256='8' * 64, current_broker_sha256='9' * 64)
        self.addCleanup(setattr, gate, 'verify_local_authority', gate.verify_local_authority)
        gate.verify_local_authority = lambda receipt: None  # authority is covered above
        r = dict(schema='mcpshka.operator-verified-local/v1', provider='h96-local',
                 repository='dmitriy1ebedev/mcpshka', source_sha='1' * 40,
                 head_sha='1' * 40, tree=p['source_tree'], source_digest=p['source_digest'],
                 build_permit='a' * 32, build_env_digest='b' * 64,
                 binary_sha256=p['candidate_sha256'], broker_binary_sha256=p['broker_sha256'])
        for kind, suites in [('gitverse', ['release-suite', 'snapshot-contracts']), ('module', ['module']),
                             ('github', list(gate.LOCAL_TEST_SUITES['github']))]:
            r['test_evidence'] = dict(kind=kind, id='c' * 32, tree=p['source_tree'],
                                      conclusion='success', suites={s: 'success' for s in suites})
            gate.validate_plan(p)
            gate.validate_receipt(r, p)
            for field, value in [('tree', '0' * 40), ('binary_sha256', '0' * 64),
                                 ('broker_binary_sha256', '0' * 64), ('source_digest', '0' * 64),
                                 ('build_permit', '../permit'), ('build_env_digest', ''),
                                 ('head_sha', '2' * 40), ('provider', 'github'), ('extra', True)]:
                with self.subTest(field=field), self.assertRaisesRegex(ValueError, 'HOLD'):
                    gate.validate_receipt(dict(r, **{field: value}), p)
            for field, value in [('tree', '0' * 40), ('kind', 'ci'), ('extra', True),
                                 ('id', ''), ('conclusion', 'failure'), ('suites', {})]:
                with self.subTest(evidence=field), self.assertRaisesRegex(ValueError, 'HOLD'):
                    gate.validate_receipt(dict(r, test_evidence=dict(r['test_evidence'], **{field: value})), p)
        with self.assertRaisesRegex(ValueError, 'HOLD'):
            gate.validate_receipt(r, self.plan())

    def test_ci_receipt_matches_all_required_gates_source_and_artifact(self):
        p = self.plan()
        r = self.receipt(p)
        gate.validate_receipt(r, p)
        for field, value in [('run_id', 999), ('conclusion', 'pending'),
                             ('tree', '0' * 40), ('binary_sha256', '0' * 64),
                             ('artifact_id', True), ('provider', 'untrusted')]:
            with self.subTest(field=field), self.assertRaises(ValueError):
                gate.validate_receipt(dict(r, **{field: value}), p)
        for suite in r['suites']:
            changed = dict(r, suites=dict(r['suites'], **{suite: 'failure'}))
            with self.subTest(suite=suite), self.assertRaises(ValueError):
                gate.validate_receipt(changed, p)
        with self.assertRaises(ValueError):
            gate.validate_receipt(dict(r, extra='execute this'), p)


    def test_repo_helper_cannot_select_privileged_executable(self):
        from unittest.mock import patch
        p = self.plan()
        p['candidate_sha256'] = gate.sha(b'candidate')
        p['current_sha256'] = gate.sha(b'current')
        p['helper_sha256'] = gate.sha(b'hostile-repository-helper')
        receipt = gate.canonical(self.receipt(p))
        p['ci_receipt_sha256'] = gate.sha(receipt)
        seen = []
        def read(path, limit, owner=None):
            seen.append((str(path), owner))
            if str(path) == '/fixture-plan':
                return gate.canonical(p)
            if str(path) == '/fixture-receipt':
                return receipt
            if path == gate.HELPER:
                return b'trusted-installed-helper'
            if str(path) == '/usr/local/bin/mcpshka':
                return b'current'
            if path == gate.candidate_path(p):
                return b'candidate'
            raise AssertionError('unexpected privileged read')
        with patch.object(gate, 'require_operator'), patch.object(gate, 'read_regular', side_effect=read), \
                patch.object(gate, 'private_directory') as authority:
            with self.assertRaisesRegex(ValueError, 'inputs differ'):
                gate.approve_and_deploy(Path('/fixture-plan'), Path('/fixture-receipt'), gate.sha(gate.canonical(p)))
            authority.assert_not_called()
        self.assertIn((str(gate.HELPER), 0), seen)
        self.assertFalse(any(path == str(gate.REPO / 'packaging/native/self-deploy.sh') for path, _ in seen))


class ProductionProofTests(unittest.TestCase):
    def test_production_proof_refuses_unmatched_or_changed_authority(self):
        p, raw = self.fixture()
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            directory = root / gate.sha(gate.canonical(p))
            directory.mkdir(mode=0o700)
            deployment = '1793950000000-123456789abc'
            values = {'approval.json': gate.canonical(dict(schema='mcpshka.release-approval/v1',
                      plan=p, plan_sha256=directory.name)), 'ci-receipt.json': raw,
                      'consumed.json': gate.canonical(dict(deployment_id=deployment)),
                      'public.pem': b'public', 'approval.sig': b'signature'}
            for name, value in values.items():
                (directory / name).write_bytes(value)
                (directory / name).chmod(0o600)
            lock = root / 'deploy.lock'
            lock.write_bytes(b'lock')
            lock.chmod(0o600)
            real = gate.read_regular
            def read(path, limit, owner=None):
                if str(path) == '/usr/local/bin/mcpshka':
                    return b'controller'
                if str(path) == '/usr/local/libexec/mcpshka-broker':
                    return b'broker'
                return real(path, limit)
            with mock.patch.object(gate, 'require_operator'), mock.patch.object(gate, 'AUTHORITY', root), \
                 mock.patch.object(gate, 'DEPLOY_LOCK', lock), mock.patch.object(gate, 'retention_directory'), \
                 mock.patch.object(gate, 'LOCK_UID', os.getuid()), \
                 mock.patch.object(gate, 'read_regular', side_effect=read), \
                 mock.patch.object(gate, 'verify_signature') as signature, \
                 mock.patch.object(gate, 'release_source_proof', return_value={'tree':p['source_tree']}) as bind:
                result = gate.production_proof(deployment)
                self.assertEqual(result['deployment_id'], deployment)
                signature.assert_called_once()
                bind.assert_called_once_with(p, raw, gate.sha(b'controller'), gate.sha(b'broker'))
                with self.assertRaises(ValueError):
                    gate.production_proof('1793950000000-000000000000')
                signature.side_effect = ValueError('bad signature')
                with self.assertRaises(ValueError):
                    gate.production_proof(deployment)

    def fixture(self):
        existing = ReleaseGateTests()
        p = dict(existing.plan(), schema='mcpshka.release-plan/v2',
                 broker_sha256='4'*64, current_broker_sha256='5'*64)
        r = dict(existing.receipt(p), schema='mcpshka.operator-verified-ci/v2',
                 broker_binary_sha256=p['broker_sha256'])
        raw = gate.canonical(r)
        p['ci_receipt_sha256'] = gate.sha(raw)
        return p, raw

    def test_source_is_bound_to_both_installed_images_and_ci(self):
        p, raw = self.fixture()
        proof = gate.release_source_proof(p, raw, p['candidate_sha256'], p['broker_sha256'])
        self.assertEqual(proof['source_sha'], '1'*40)
        self.assertEqual(proof['tree'], p['source_tree'])
        self.assertEqual(proof['version'], p['version'])
        for controller, broker in [('0'*64, p['broker_sha256']),
                                   (p['candidate_sha256'], '0'*64)]:
            with self.subTest(controller=controller), self.assertRaises(ValueError):
                gate.release_source_proof(p, raw, controller, broker)

    def test_changed_receipt_or_missing_suite_is_not_proof(self):
        p, raw = self.fixture()
        with self.assertRaises(ValueError):
            gate.release_source_proof(p, raw+b' ', p['candidate_sha256'], p['broker_sha256'])
        r = gate.strict_json(raw)
        r['suites']['race'] = 'failure'
        raw = gate.canonical(r)
        p['ci_receipt_sha256'] = gate.sha(raw)
        with self.assertRaises(ValueError):
            gate.release_source_proof(p, raw, p['candidate_sha256'], p['broker_sha256'])
