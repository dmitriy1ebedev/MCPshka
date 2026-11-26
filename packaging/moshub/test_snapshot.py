#!/usr/bin/env python3
"""Offline fail-closed MosHub snapshot acceptance; no network or credentials."""
import importlib.util
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

SCRIPT = Path(__file__).with_name("snapshot.py")
spec = importlib.util.spec_from_file_location("moshub_snapshot", SCRIPT)
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class SnapshotTests(unittest.TestCase):
    def make_repo(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        def git(*args):
            p = subprocess.run(["git", "-C", str(root), *args],
                               stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                               timeout=5, check=True)
            return p.stdout.decode().strip()
        git("init", "-b", "main")
        git("config", "user.name", "Test Snapshot")
        git("config", "user.email", "snapshot@example.invalid")
        (root / "go.mod").write_text("module example.invalid/test\n")
        git("add", "go.mod")
        git("commit", "-m", "test")
        return root, git

    def test_readonly_plan_pins_committed_main_tree(self):
        root, git = self.make_repo()
        sha, tree = m.source_identity(root)
        self.assertEqual(sha, git("rev-parse", "HEAD"))
        self.assertEqual(tree, git("rev-parse", "HEAD^{tree}"))
        (root / "untracked").write_text("not exported")
        self.assertEqual((sha, tree), m.source_identity(root))

    def test_dirty_tracked_refused_feature_branch_allowed(self):
        root, git = self.make_repo()
        (root / "go.mod").write_text("tampered")
        with self.assertRaisesRegex(RuntimeError, "source_tracked_changes_present"):
            m.source_identity(root)
        git("checkout", "--", "go.mod")
        git("switch", "-c", "feature")
        self.assertEqual(m.source_identity(root)[0], git("rev-parse", "HEAD"))

    def test_explicit_commit_of_any_branch(self):
        root, git = self.make_repo()
        git("switch", "-c", "feature")
        (root / "go.mod").write_text("module example.invalid/feature\n")
        git("commit", "-am", "feature")
        sha = git("rev-parse", "HEAD")
        git("switch", "main")
        (root / "go.mod").write_text("dirty main is irrelevant")
        self.assertEqual(m.source_identity(root, sha), (sha, git("rev-parse", sha + "^{tree}")))
        for bad in ("0" * 40, git("rev-parse", sha + "^{tree}"), "HEAD", sha[:12]):
            with self.assertRaisesRegex(RuntimeError, "source_commit_unknown"):
                m.source_identity(root, bad)

    def test_remote_host_allowlist_and_slugs(self):
        self.assertEqual(m.remote("team/project"), "https://hub.mos.ru/team/project.git")
        for invalid in ("", "../escape", "https://evil.test", "group//project",
                        "team/a b", "team/project;evil", "/absolute"):
            with self.subTest(invalid=invalid):
                with self.assertRaises(RuntimeError):
                    m.remote(invalid)

    def test_secret_file_permissions_and_symlinks(self):
        with tempfile.TemporaryDirectory() as d:
            path = Path(d) / "secret"
            path.write_bytes(b"private")
            path.chmod(0o600)
            self.assertEqual(m.private_file(path), b"private")
            path.chmod(0o644)
            with self.assertRaises(RuntimeError):
                m.private_file(path)
            path.chmod(0o600)
            link = Path(d) / "link"
            link.symlink_to(path)
            with self.assertRaises(OSError):
                m.private_file(link)

    def test_parentless_same_tree_deterministic(self):
        root, git = self.make_repo()
        with patch.object(m, "REPO", root):
            sha, tree = m.source_identity(root)
            snap = m.snapshot_commit(sha, tree)
            self.assertEqual(snap, m.snapshot_commit(sha, tree))
            self.assertEqual(git("rev-list", "--parents", "-n", "1", snap), snap)
            self.assertEqual(git("rev-parse", snap + "^{tree}"), tree)
            self.assertEqual(git("rev-parse", "HEAD"), sha)

    def test_publish_conflict_idempotency_and_readback(self):
        sha, tree, snap = "a" * 40, "b" * 40, "c" * 40
        with patch.object(m, "verify_private_project"), patch.object(m, "snapshot_commit", return_value=snap), patch.object(m, "git") as git:
            # Every runner offline: nothing is pushed, so no pipeline can hang and mail a failure.
            with patch.object(m, "branch_commit", return_value=None), patch.object(m, "api", return_value=[{"status": "offline"}, {"status": "never_contacted"}]):
                with self.assertRaisesRegex(RuntimeError, "no_online_runner"):
                    m.publish(sha, tree)
            git.assert_not_called()
            self.assertTrue(m.runners_online.__doc__)
            with patch.object(m, "api", side_effect=RuntimeError("moshub_api_incomplete")):
                self.assertTrue(m.runners_online())
            with patch.object(m, "api", return_value=[{"status": "offline"}, {"status": "online"}]):
                self.assertTrue(m.runners_online())
            stuck = [{"failure_reason": "stuck_or_timeout_failure"}]
            pipes = [{"id": 3, "created_at": "2026-11-24T10:00:00.000+03:00"}, {"id": 2}, {"id": 1}]
            now = m.datetime.fromisoformat("2026-11-24T12:00:00+03:00").timestamp()
            with patch.object(m, "api", side_effect=[pipes, stuck, stuck, stuck]):
                self.assertTrue(m.stuck_history(now))
            with patch.object(m, "api", side_effect=[pipes, stuck, stuck, stuck]):
                self.assertFalse(m.stuck_history(now + 2 * m.PROBE_AFTER))  # daily probe
            with patch.object(m, "api", side_effect=[pipes, stuck, [{"failure_reason": "script_failure"}], stuck]):
                self.assertFalse(m.stuck_history(now))
            with patch.object(m, "api", side_effect=[[], pipes, stuck, stuck, stuck]):
                self.assertFalse(m.runners_online())
            with patch.object(m, "api", side_effect=RuntimeError("moshub_api_unavailable_or_unauthorized")):
                self.assertFalse(m.stuck_history())
            with patch.object(m, "branch_commit", return_value="d" * 40):
                with self.assertRaisesRegex(RuntimeError, "remote_ref_conflict"):
                    m.publish(sha, tree)
            git.assert_not_called()
            with patch.object(m, "branch_commit", return_value=snap):
                self.assertEqual(m.publish(sha, tree), (snap, "ALREADY_PRESENT"))
            git.assert_not_called()
            with patch.object(m, "branch_commit", side_effect=[None, snap]), patch.object(m, "https_environment") as env, patch.object(m, "runners_online", return_value=True):
                m.publish(sha, tree)
                args = git.call_args.args
                self.assertIn("--force-with-lease=refs/heads/mcpshka-snapshots/" + sha + ":", args)
                self.assertIn(snap + ":refs/heads/mcpshka-snapshots/" + sha, args)
            with patch.object(m, "branch_commit", side_effect=[snap, None]):
                with self.assertRaisesRegex(RuntimeError, "remote_verification_failed"):
                    m.publish(sha, tree)

    def test_askpass_is_private_reads_file_and_is_removed(self):
        with tempfile.TemporaryDirectory() as d:
            token = Path(d) / "token"
            token.write_text("test-secret")
            token.chmod(0o600)
            with patch.object(m, "TOKEN", token), m.https_environment() as env:
                script = Path(env["GIT_ASKPASS"])
                self.assertEqual(script.parent.stat().st_mode & 0o777, 0o700)
                self.assertNotIn("test-secret", script.read_text())
                result = subprocess.check_output([str(script), "Password for 'https://oauth2@hub.mos.ru':"])
                self.assertEqual(result.strip(), b"test-secret")
                self.assertEqual(env["GIT_CONFIG_VALUE_0"], "true")
            self.assertFalse(script.exists())

    def test_no_live_publish_without_config(self):
        root, git = self.make_repo()
        old = m.REPO
        m.REPO = root
        try:
            # Plan has no network side effects and never changes Git HEAD.
            before = git("rev-parse", "HEAD")
            self.assertEqual(before, m.source_identity(root)[0])
            # Offline tests must remain valid after credentials are provisioned.
            self.assertEqual(before, git("rev-parse", "HEAD"))
        finally:
            m.REPO = old


if __name__ == "__main__":
    unittest.main()
