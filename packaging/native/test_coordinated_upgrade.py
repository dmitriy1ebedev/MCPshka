import importlib.util
from contextlib import ExitStack
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

FILE = Path(__file__).with_name("coordinated-upgrade.py")
spec = importlib.util.spec_from_file_location("paired", FILE)
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class RetentionIntegrationTests(unittest.TestCase):
    def test_unfinished_release_never_runs_retention(self):
        for state in ('READY', 'ROLLED_BACK', 'UNKNOWN_HOLD'):
            with self.subTest(state=state), mock.patch.object(m.subprocess, 'run') as run:
                result = dict(state=state, version='0.1.189')
                self.assertEqual(m.retain_after_success(result), result)
                run.assert_not_called()

    def test_success_reports_both_sweeps_and_preserves_success_on_cleanup_error(self):
        result = dict(state='SUCCEEDED', version='0.1.189', operation_id='a' * 32)
        completed = type('Result', (), dict(returncode=0, stdout=b'{"deleted":3,"deleted_bytes":123}', stderr=b''))()
        with mock.patch.object(m.subprocess, 'run', return_value=completed) as run, \
                mock.patch.object(m, 'create') as audit:
            observed = m.retain_after_success(dict(result), keep_gates=7, keep_versions=3)
            self.assertEqual(observed['cleanup']['gates']['deleted'], 3)
            self.assertEqual(observed['cleanup']['bin']['deleted_bytes'], 123)
            self.assertEqual(run.call_count, 2)
            self.assertIn('--keep', run.call_args_list[0].args[0])
            self.assertEqual(run.call_args_list[1].args[0][-2:], ('apply', '3'))
            audit.assert_called_once()
        with mock.patch.object(m.subprocess, 'run', side_effect=OSError('unavailable')), \
                mock.patch.object(m, 'create'):
            observed = m.retain_after_success(dict(result))
            self.assertEqual(observed['state'], 'SUCCEEDED')
            self.assertEqual(observed['cleanup']['gates']['state'], 'HELD')
            self.assertEqual(observed['cleanup']['bin']['state'], 'HELD')

class PairedUpgradeTests(unittest.TestCase):
    def plan(self, root):
        d = {"schema":"mcpshka.coordinated-upgrade/v1",
             "operation_id":"a"*32,"version":"0.1.134",
             "current_version":"0.1.133","ci_run":123,"source_tree":"d"*40,
             "controller_sha256":m.sha(b"controller-new"),
             "broker_sha256":m.sha(b"broker-new"),
             "current_controller_sha256":m.sha(b"controller-old"),
             "current_broker_sha256":m.sha(b"broker-old"), "helper_sha256":m.sha(FILE.read_bytes()),
             "gate_sha256":m.sha(b"gate-new"), "guarded_helper_sha256":m.sha(b"guarded-new"),
             "current_gate_sha256":m.sha(b"gate-old"), "current_guarded_sha256":m.sha(b"guarded-old")}
        d["controller_candidate"]=str(root / ("mcpshka-"+d["version"]+"."+d["controller_sha256"][:12]+".candidate"))
        d["broker_candidate"]=str(root / ("mcpshka-broker-"+d["version"]+"."+d["broker_sha256"][:12]+".candidate"))
        return d

    def test_reject_duplicate_and_foreign_path(self):
        with self.assertRaises(ValueError):
            m.parse(b'{"a":1,"a":2}')
        with tempfile.TemporaryDirectory() as name, mock.patch.object(m,"BIN",Path(name)):
            p=self.plan(Path(name))
            self.assertIs(m.validate(p),p)
            for key,value in (("operation_id","x"),("ci_run",True),("source_tree","invalid"),("version","v134"),("broker_candidate","/tmp/hijack"),("unexpected",None)):
                d=dict(p);d[key]=value
                with self.subTest(key=key), self.assertRaises(ValueError):
                    m.validate(d)

    def test_read_refuses_symlink(self):
        with tempfile.TemporaryDirectory() as name:
            d=Path(name);(d/"real").write_bytes(b"content");(d/"alias").symlink_to(d/"real")
            with self.assertRaises(OSError):
                m.read(d/"alias")

    def test_fixed_paths_and_shas(self):
        with tempfile.TemporaryDirectory() as name:
            root=Path(name);p=self.plan(root)
            old=(root/"old-controller",root/"old-broker")
            for file,data in zip(old,(b"controller-old",b"broker-old")): file.write_bytes(data)
            for file,data in zip((Path(p["controller_candidate"]),Path(p["broker_candidate"])),(b"controller-new",b"broker-new")):file.write_bytes(data)
            with mock.patch.object(m,"BIN",root),mock.patch.object(m,"TARGETS",old),mock.patch.object(m,"read",side_effect=lambda path, owner=None, limit=128<<20:Path(path).read_bytes()):
                images,previous=m.validate_images(p)
                self.assertEqual(images[1],b"broker-new")
                self.assertEqual(previous[0],b"controller-old")
                Path(p["broker_candidate"]).write_bytes(b"altered")
                with self.assertRaisesRegex(ValueError,"SHA changed"):
                    m.validate_images(p)

    def test_recovery_is_idempotent_fail_closed(self):
        with tempfile.TemporaryDirectory() as name:
            root=Path(name)
            root.chmod(0o700)
            plan=self.plan(root)
            targets=(root/"controller",root/"broker")
            reference = root/"controller.previous"
            reference.write_bytes(b"prior-rollback-version")
            for path,data in zip(targets,(b"controller-old",b"broker-old")):path.write_bytes(data)
            for path,data in zip((Path(plan["controller_candidate"]),Path(plan["broker_candidate"])),(b"controller-new",b"broker-new")):path.write_bytes(data)
            (root/"mcpshka-coordinated-release-0.1.134.json").write_text(json.dumps(plan))
            for name,data in (("gate",b"gate-old"),("guarded",b"guarded-old"),
                              ("new-gate",b"gate-new"),("new-guarded",b"guarded-new")):
                (root/name).write_bytes(data)
            statuses={x:True for x in m.SERVICES}
            home_calls=[]
            def fake_command(*args):
                if args[1] == "home-priority":
                    self.assertEqual(targets[1].read_bytes(), b"broker-new")
                    home_calls.append(args[2])
                    self.assertFalse(any(statuses.values()))
                if args[1] == "is-active":
                    if not statuses[args[-1]]: raise RuntimeError("not active")
                elif args[1] == "stop": statuses[args[-1]]=False
                elif args[1] == "start": statuses[args[-1]]=True
                return b""
            with ExitStack() as stack:
                stack.enter_context(mock.patch.object(m,"BIN",root))
                stack.enter_context(mock.patch.object(m,"PLAN",root/"mcpshka-coordinated-release-0.1.134.json"))
                stack.enter_context(mock.patch.object(m,"ROOT",root))
                stack.enter_context(mock.patch.object(m,"AUDIT",root/"coordinated-upgrades"))
                stack.enter_context(mock.patch.object(m,"LOCK",root/"lock"))
                stack.enter_context(mock.patch.object(m,"HELPER_TARGET",root/"installed-helper"))
                stack.enter_context(mock.patch.object(m,"GATE_TARGET",root/"gate"))
                stack.enter_context(mock.patch.object(m,"GUARDED_TARGET",root/"guarded"))
                stack.enter_context(mock.patch.object(m,"GATE_SOURCE",root/"new-gate"))
                stack.enter_context(mock.patch.object(m,"GUARDED_SOURCE",root/"new-guarded"))
                stack.enter_context(mock.patch.object(m,"TARGETS",targets))
                stack.enter_context(mock.patch.object(m,"BROKER_RESOURCES",root/"mcpshka-broker.service.d"/"90-mcpshka-release-resources.conf"))
                stack.enter_context(mock.patch.object(m,"broker_resource_parent",lambda: (root/"mcpshka-broker.service.d").is_dir()))
                stack.enter_context(mock.patch.object(m,"broker_resources_healthy",return_value=True))
                stack.enter_context(mock.patch.object(m,"root_dir",lambda _:None))
                stack.enter_context(mock.patch.object(m,"safe_deploy_lock",lambda _:True))
                stack.enter_context(mock.patch.object(m,"prepare_heavy_queue"))
                stack.enter_context(mock.patch.object(m,"schedule_cleanup"))
                stack.enter_context(mock.patch.object(m,"verify_installed", return_value=["healthz"]))
                stack.enter_context(mock.patch.object(m,"running_broker_hash",side_effect=lambda: m.sha(targets[1].read_bytes())))
                stack.enter_context(mock.patch.object(m,"read",side_effect=lambda path, owner=None, limit=128<<20:Path(path).read_bytes()))
                stack.enter_context(mock.patch.object(m.os,"statvfs",return_value=type("Space",(),{"f_bavail":100000000,"f_frsize":4096,"f_blocks":150000000})()))
                stack.enter_context(mock.patch.object(m,"command",side_effect=fake_command))
                stack.enter_context(mock.patch.object(m,"health",lambda version:None))
                stack.enter_context(mock.patch.object(m.os,"geteuid",return_value=0))
                self.assertEqual(m.upgrade(True)["state"],"READY")
                self.assertEqual(m.upgrade(False)["state"],"SUCCEEDED")
                self.assertEqual(targets[0].read_bytes(),b"controller-new")
                self.assertEqual(targets[1].read_bytes(),b"broker-new")
                self.assertEqual((root/"mcpshka-broker.service.d"/"90-mcpshka-release-resources.conf").read_bytes(),m.BROKER_RESOURCE_TEXT)
                with self.assertRaises((FileExistsError,ValueError)):
                    m.upgrade(False)
                audit=root/"coordinated-upgrades"/plan["operation_id"]
                self.assertEqual((audit/"broker.previous").read_bytes(),b"broker-old")
                self.assertTrue(list(audit.glob("*SUCCEEDED.json")))
                self.assertEqual(m.journal_status(plan)["state"],"SUCCEEDED")
                self.assertEqual(reference.read_bytes(), b"controller-old")
                self.assertEqual(m.explicit_rollback()["state"],"ROLLED_BACK")
                self.assertEqual(reference.read_bytes(), b"prior-rollback-version")
                self.assertFalse((root/"mcpshka-broker.service.d"/"90-mcpshka-release-resources.conf").exists())
                self.assertEqual(home_calls, ["--startup", "--rollback"])
                self.assertEqual([p.read_bytes() for p in targets],[b"controller-old",b"broker-old"])
                self.assertEqual(m.explicit_rollback()["state"],"ROLLED_BACK")

    def test_failure_rolls_back_both(self):
        for failure in ("health", "verify", "verify_timeout", "priority", "resources", "image", "start", "fence"):
            with self.subTest(failure=failure):
                self.failure_rolls_back(failure)

    def failure_rolls_back(self, failure):
        with tempfile.TemporaryDirectory() as name:
            root=Path(name);root.chmod(0o700)
            plan=self.plan(root)
            targets=(root/"controller",root/"broker")
            reference = root/"controller.previous"
            reference.write_bytes(b"prior-rollback-version")
            for path,data in zip(targets,(b"controller-old",b"broker-old")):path.write_bytes(data)
            for path,data in zip((Path(plan["controller_candidate"]),Path(plan["broker_candidate"])),(b"controller-new",b"broker-new")):path.write_bytes(data)
            (root/"mcpshka-coordinated-release-0.1.134.json").write_text(json.dumps(plan))
            for name,data in (("gate",b"gate-old"),("guarded",b"guarded-old"),
                              ("new-gate",b"gate-new"),("new-guarded",b"guarded-new")):
                (root/name).write_bytes(data)
            live={x:True for x in m.SERVICES}
            home_calls=[]
            starts=[]
            def command(*a):
                if a[1]=="home-priority":
                    self.assertEqual(targets[1].read_bytes(), b"broker-new")
                    home_calls.append(a[2])
                    if failure == "priority" and a[2] == "--rollback":
                        raise RuntimeError("priority rollback exit=1")
                if a[1]=="is-active":
                    if failure == "fence" and home_calls:
                        raise OSError("fence probe unavailable")
                    if not live[a[-1]]:raise RuntimeError("not active")
                elif a[1]=="stop":live[a[-1]]=False
                elif a[1]=="start":
                    starts.append(a[-1])
                    if failure == "start" and a[-1] == m.SERVICES[0]:
                        raise RuntimeError("start exit=1")
                    live[a[-1]]=True
                return b""
            def fail_new(version):
                if version == "0.1.134" and failure not in ("verify", "verify_timeout"):
                    raise RuntimeError("not ready")
            def verify_new(_):
                if failure == "verify_timeout":
                    raise m.subprocess.TimeoutExpired("verify-production", 100)
                raise RuntimeError("installed verifier failed")
            with ExitStack() as stack:
                stack.enter_context(mock.patch.object(m,"BIN",root))
                stack.enter_context(mock.patch.object(m,"PLAN",root/"mcpshka-coordinated-release-0.1.134.json"))
                stack.enter_context(mock.patch.object(m,"ROOT",root))
                stack.enter_context(mock.patch.object(m,"AUDIT",root/"coordinated-upgrades"))
                stack.enter_context(mock.patch.object(m,"LOCK",root/"lock"))
                stack.enter_context(mock.patch.object(m,"HELPER_TARGET",root/"installed-helper"))
                stack.enter_context(mock.patch.object(m,"GATE_TARGET",root/"gate"))
                stack.enter_context(mock.patch.object(m,"GUARDED_TARGET",root/"guarded"))
                stack.enter_context(mock.patch.object(m,"GATE_SOURCE",root/"new-gate"))
                stack.enter_context(mock.patch.object(m,"GUARDED_SOURCE",root/"new-guarded"))
                stack.enter_context(mock.patch.object(m,"TARGETS",targets))
                stack.enter_context(mock.patch.object(m,"BROKER_RESOURCES",root/"mcpshka-broker.service.d"/"90-mcpshka-release-resources.conf"))
                stack.enter_context(mock.patch.object(m,"broker_resource_parent",lambda: (root/"mcpshka-broker.service.d").is_dir()))
                stack.enter_context(mock.patch.object(m,"broker_resources_healthy",return_value=True))
                stack.enter_context(mock.patch.object(m,"root_dir",lambda _:None))
                stack.enter_context(mock.patch.object(m,"safe_deploy_lock",lambda _:True))
                stack.enter_context(mock.patch.object(m,"prepare_heavy_queue"))
                stack.enter_context(mock.patch.object(m,"schedule_cleanup"))
                stack.enter_context(mock.patch.object(m,"verify_installed", return_value=["healthz"]))
                stack.enter_context(mock.patch.object(m,"running_broker_hash",side_effect=lambda: m.sha(targets[1].read_bytes())))
                stack.enter_context(mock.patch.object(m,"read",side_effect=lambda path, owner=None, limit=128<<20:Path(path).read_bytes()))
                stack.enter_context(mock.patch.object(m.os,"statvfs",return_value=type("Space",(),{"f_bavail":100000000,"f_frsize":4096,"f_blocks":150000000})()))
                stack.enter_context(mock.patch.object(m,"command",side_effect=command))
                stack.enter_context(mock.patch.object(m,"health",side_effect=fail_new))
                stack.enter_context(mock.patch.object(m,"verify_installed",side_effect=verify_new))
                stack.enter_context(mock.patch.object(m.os,"geteuid",return_value=0))
                real_replace = m.replace
                def replace(path, raw):
                    if failure == "image" and path == targets[0] and raw == b"controller-old":
                        raise OSError("restore controller denied")
                    real_replace(path, raw)
                stack.enter_context(mock.patch.object(m, "replace", side_effect=replace))
                if failure == "resources":
                    stack.enter_context(mock.patch.object(m, "restore_broker_resources", side_effect=OSError("resource restore failed")))
                rolled_back = failure in ("health", "verify", "verify_timeout")
                with self.assertRaisesRegex(RuntimeError, "rollback verified" if rolled_back else "rollback UNKNOWN"):
                    m.upgrade(False)
                self.assertEqual(starts[-3:], list(m.SERVICES))
                if failure != "start": self.assertTrue(all(live.values()))
                events = sorted((root/"coordinated-upgrades"/plan["operation_id"]).glob("*.json"))
                event = json.loads(events[-1].read_text())
                self.assertEqual(event["state"], "ROLLED_BACK" if rolled_back else "ROLLBACK_FAILED")
                expected_error = {"start": "start exit=1", "verify": "installed verifier failed",
                                  "verify_timeout": "timed out"}.get(failure, "not ready")
                self.assertIn(expected_error, event["error"])
                self.assertIn("Traceback", event["traceback"])
                if not rolled_back: self.assertTrue(event["rollback_errors"])
                self.assertEqual(targets[1].read_bytes(), b"broker-new" if failure == "fence" else b"broker-old")
                if not rolled_back: return
                self.assertEqual(reference.read_bytes(), b"prior-rollback-version")
                self.assertEqual(home_calls, ["--startup", "--rollback"])
                self.assertEqual([p.read_bytes() for p in targets],[b"controller-old",b"broker-old"])
                self.assertTrue(list((root/"coordinated-upgrades"/plan["operation_id"]).glob("*ROLLED_BACK.json")))
                self.assertFalse((root/"mcpshka-broker.service.d"/"90-mcpshka-release-resources.conf").exists())

    def test_installed_verifier_requires_the_pair_and_rollback(self):
        plan = {"version": "0.17.2", "current_version": "0.17.1"}
        report = {"ok": True, "installed_version": "0.17.2",
                  "rollback_version": "0.17.1", "checks": [{"name": "healthz", "ok": True}]}
        def complete(value):
            return m.subprocess.CompletedProcess([], 0, json.dumps(value).encode(), b"")
        with mock.patch.object(m.subprocess, "run", return_value=complete(report)) as run:
            self.assertEqual(m.verify_installed(plan), ["healthz"])
            self.assertIn("mcpshka", run.call_args.args[0])
            self.assertEqual(run.call_args.args[0][-1], "verify-production")
            self.assertNotIn("MCPSHKA_PRIVILEGED_BROKER", run.call_args.kwargs["env"])
        failing = dict(report, ok=False, checks=[{"name": "installed_version", "ok": False,
                                                  "detail": "connection reset"}])
        with mock.patch.object(m.subprocess, "run", return_value=m.subprocess.CompletedProcess(
                [], 1, json.dumps(failing).encode(), b"")):
            with self.assertRaisesRegex(RuntimeError, "exit 1.*installed_version: connection reset"):
                m.verify_installed(plan)
        for bad in (dict(report, ok=False), dict(report, installed_version="0.17.1"),
                    dict(report, rollback_version=None), dict(report, checks=[])):
            with mock.patch.object(m.subprocess, "run", return_value=complete(bad)):
                with self.assertRaises((ValueError, RuntimeError)):
                    m.verify_installed(plan)

    def test_command_preserves_failed_step(self):
        failed = m.subprocess.CompletedProcess([], 7, b"partial output", b"real failure")
        with mock.patch.object(m.subprocess, "run", return_value=failed):
            with self.assertRaisesRegex(RuntimeError, "exit=7.*real failure"):
                m.command("/usr/bin/systemctl", "start", "fixture.service")

    def test_broker_resource_drift_fails_closed(self):
        ok = b"CPUWeight=400\nCPUQuotaPerSecUSec=1s\nIOWeight=400\nTasksMax=96\n"
        with mock.patch.object(m, "command", return_value=ok):
            self.assertTrue(m.broker_resources_healthy())
        with mock.patch.object(m, "command", return_value=ok.replace(b"TasksMax=96", b"TasksMax=32")):
            self.assertFalse(m.broker_resources_healthy())

if __name__=="__main__":
    unittest.main()
