"""Offline API fixtures: identity mismatches never become a pass."""
import copy
import importlib.util
from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).parent))
import ci_status as m

SHA, TREE, SNAP = "a" * 40, "b" * 40, "c" * 40
REF = "mcpshka-snapshots/" + SHA
PIPELINE = {"id": 123, "ref": REF, "sha": SNAP, "status": "success"}
JOB = {"name": m.JOB, "status": "success", "ref": REF, "allow_failure": False,
       "commit": {"id": SNAP}, "pipeline": {"id": 123}}


class StatusTests(unittest.TestCase):
    def test_runs_isolated_without_script_dir_on_path(self):
        # forge_ci starts it with python3 -I: the sibling must still load.
        import subprocess
        out = subprocess.run([sys.executable, "-I", "-B", str(Path(__file__).with_name("ci_status.py")), "x"],
                             capture_output=True, text=True, timeout=30)
        self.assertNotIn("ModuleNotFoundError", out.stderr)
        self.assertIn('"moshub_ci_unverified"', out.stdout)

    def test_states(self):
        self.assertEqual(m.evaluate(SHA, TREE, SNAP, [], [])["state"], "missing")
        for status, state in [("success", "passed"), ("failed", "failed"), ("running", "running"), ("pending", "running"), ("canceled", "failed"), ("manual", "failed"), ("skipped", "failed")]:
            pipeline, job = dict(PIPELINE, status=status), dict(JOB, status=status)
            self.assertEqual(m.evaluate(SHA, TREE, SNAP, [pipeline], [job])["state"], state)

    def test_unexpected_or_incomplete_is_rejected(self):
        cases = [([PIPELINE], []), ([PIPELINE], [JOB, JOB]),
                 ([dict(PIPELINE, status="unknown")], [JOB]),
                 ([dict(PIPELINE, sha=SHA)], [JOB]),
                 ([dict(PIPELINE, ref="main")], [JOB]),
                 ([dict(PIPELINE, id=True)], [JOB]),
                 ([PIPELINE, PIPELINE], [JOB]),
                 ([PIPELINE], [dict(JOB, allow_failure=True)]),
                 ([PIPELINE], [dict(JOB, commit={"id": SHA})]),
                 ([PIPELINE], [dict(JOB, pipeline={"id": 999})]),
                 ([PIPELINE], [dict(JOB, status="skipped")]),
                 ([PIPELINE], [dict(JOB, status="pending")]),
                 ([PIPELINE], [dict(JOB, name="unexpected")]),
                 ({}, []), (None, []), ([None], []), ([PIPELINE], {})]
        for pipelines, jobs in cases:
            with self.subTest(pipelines=pipelines, jobs=jobs), self.assertRaises(RuntimeError):
                m.evaluate(SHA, TREE, SNAP, pipelines, jobs)

    def test_api_routes_and_branch_binding(self):
        def request(path):
            if path.startswith("/pipelines?"):
                self.assertIn("ref=mcpshka-snapshots%2F" + SHA, path)
                return [PIPELINE]
            if path.startswith("/repository/branches/"):
                return {"name": REF, "commit": {"id": SNAP}}
            self.assertEqual(path, "/pipelines/123/jobs?per_page=100&include_retried=false")
            return [JOB]
        result = m.observe(SHA, TREE, SNAP, request)
        self.assertEqual(result["state"], "passed")
        self.assertEqual(result["tree"], TREE)
        def wrong_branch(path):
            return [PIPELINE] if path.startswith("/pipelines?") else {"name": REF, "commit": {"id": SHA}}
        with self.assertRaises(RuntimeError):
            m.observe(SHA, TREE, SNAP, wrong_branch)
        with self.assertRaises(OSError):
            m.observe(SHA, TREE, SNAP, lambda _: (_ for _ in ()).throw(OSError("offline")))

    def test_retry_posts_only_for_a_valid_pipeline(self):
        from unittest import mock
        sent = {}

        class Resp:
            status = 201

            def __enter__(self):
                return self

            def __exit__(self, *a):
                return False

        class Opener:
            def open(self, req, timeout):
                sent.update(url=req.full_url, method=req.get_method())
                return Resp()

        with mock.patch.object(m, "private_file", return_value=b"tok"), mock.patch.object(m.urllib.request, "build_opener", return_value=Opener()):
            m.retry(123)
        self.assertEqual(sent, {"url": "https://hub.mos.ru/api/v4/projects/91385/pipelines/123/retry", "method": "POST"})
        with self.assertRaises(RuntimeError):
            m.retry(0)

