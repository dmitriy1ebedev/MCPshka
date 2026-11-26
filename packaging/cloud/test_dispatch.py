#!/usr/bin/env python3
"""Offline contracts: no account access, network, cloud tasks or host writes."""
import copy
import importlib.util
import json
from pathlib import Path
import sys
import unittest
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location("dispatch", Path(__file__).with_name("dispatch.py"))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class DispatchTests(unittest.TestCase):
    def request(self):
        return dict(id="dispatch-one", owner="owner", provider="codex_cloud", commit="a" * 40,
                    prompt="Fix", acceptance="PASS")

    def receipt(self):
        r = m.receipt(self.request(), b"request")
        r.update(snapshot_commit="b" * 40, source_tree="c" * 40, provider_task_id="task_e_1")
        return r

    def test_missing_environment_blocks_before_network(self):
        with patch.object(m, "snapshot_module"), patch.object(m, "settings", side_effect=FileNotFoundError), patch.object(m, "run") as run:
            r = m.start(self.request(), b"request")
            self.assertEqual(r["reason"], "owner_must_configure_codex_environment")
            self.assertEqual(r["state"], "blocked")
            run.assert_not_called()

    def test_claude_is_explicitly_blocked(self):
        p = self.request()
        p["provider"] = "claude_cloud"
        with patch.object(m, "run") as run:
            self.assertEqual(m.start(p, b"request")["state"], "blocked")
            run.assert_not_called()

    def test_submit_once_unknown_and_snapshot_pins(self):
        for output in ["https://chatgpt.com/codex/tasks/task_e_1", "ambiguous response"]:
            with self.subTest(output=output), patch.object(m, "snapshot_module") as mod, patch.object(m, "settings", return_value="env-one"), patch.object(m, "publish", return_value=("b" * 40, "c" * 40, "mcpshka-snapshots/" + "a" * 40)), patch.object(m, "run", side_effect=[json.dumps({"tasks": []}), output]) as run:
                mod.return_value.private_file.return_value = b"test-only-token"
                r = m.start(self.request(), b"request")
                self.assertEqual(run.call_count, 2)
                argv = run.call_args.args[0]
                self.assertEqual(argv[:3], [m.CODEX, "cloud", "exec"])
                self.assertIn("mcpshka-snapshots/" + "a" * 40, argv)
                self.assertEqual(argv[-1], "-")
                self.assertIn("Acceptance:\nPASS", run.call_args.kwargs["input_text"])
                self.assertNotIn("test-only-token", repr(argv))
                self.assertEqual(r["state"], "submitted" if output.startswith("https:") else "unknown")
                self.assertFalse(r["deploy_authorized"])

    def test_snapshot_ref_is_never_rewritten(self):
        mod = Mock()
        mod.source_identity.return_value = ("a" * 40, "c" * 40)
        mod.snapshot_commit.return_value = ("b" * 40, {})
        ref = "refs/heads/mcpshka-snapshots/" + "a" * 40
        mod.git.side_effect = ["", "", "b" * 40 + "\t" + ref]
        self.assertEqual(m.publish(mod, "a" * 40, "test-only-token")[0], "b" * 40)
        push = mod.git.call_args_list[1]
        self.assertEqual(push.args, ("push", m.REMOTE, "b" * 40 + ":" + ref))
        mod.git.reset_mock()
        mod.git.side_effect = ["d" * 40 + "\t" + ref]
        with self.assertRaises(RuntimeError):
            m.publish(mod, "a" * 40, "test-only-token")
        self.assertEqual(mod.git.call_count, 1)

    def pr(self):
        r = self.receipt()
        return dict(state="open", merged_at=None, number=1,
                    html_url="https://github.com/" + m.REPOSITORY + "/pull/1", body="task_e_1",
                    head=dict(repo=dict(full_name=m.REPOSITORY), ref="cloud-agents/" + r["id"], sha="d" * 40),
                    base=dict(repo=dict(full_name=m.REPOSITORY), ref="mcpshka-snapshots/" + r["commit"], sha=r["snapshot_commit"]))

    def test_result_requires_same_repo_source_and_branch(self):
        good = self.pr()
        self.assertEqual(m.result_pr(self.receipt(), [good])["result_commit"], "d" * 40)
        for location, key, value in [("head", "ref", "main"), ("base", "sha", "a" * 40), ("head", "repo", {"full_name": "foreign/repo"}), ("base", "ref", "main")]:
            bad = copy.deepcopy(good)
            bad[location][key] = value
            self.assertIsNone(m.result_pr(self.receipt(), [bad]))
        good["body"] = "unrelated task"
        self.assertIsNone(m.result_pr(self.receipt(), [good]))

    def test_completed_without_pr_is_not_result(self):
        r = self.receipt()
        mod = Mock()
        mod.source_identity.return_value = (r["commit"], r["source_tree"])
        mod.snapshot_commit.return_value = (r["snapshot_commit"], {})
        mod.private_file.return_value = b"test-only-token"
        page = {"tasks": [{"id": r["provider_task_id"], "environment_id": "env-one", "status": "ready"}], "cursor": None}
        with patch.object(m, "snapshot_module", return_value=mod), patch.object(m, "settings", return_value="env-one"), patch.object(m, "run", return_value=json.dumps(page)), patch.object(m, "github", return_value=[]):
            got = m.poll(r)
            self.assertEqual(got["state"], "awaiting_pr")
            self.assertFalse(got["deploy_authorized"])

    def test_cli_output_is_bounded(self):
        with self.assertRaises(RuntimeError):
            m.run([sys.executable, "-c", "print('x'*200000)"])


if __name__ == "__main__":
    unittest.main()
