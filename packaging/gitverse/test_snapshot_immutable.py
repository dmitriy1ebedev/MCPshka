#!/usr/bin/env python3
"""Offline acceptance of GitVerse immutable snapshot. No cloud calls."""
import importlib.util
from pathlib import Path
import subprocess
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("immutable", Path(__file__).with_name("snapshot_immutable.py"))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class ImmutableSnapshotTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.original = m.REPO
        m.REPO = self.root
        self.addCleanup(lambda: setattr(m, "REPO", self.original))
        self.g("init", "-b", "main")
        self.g("config", "user.name", "Test")
        self.g("config", "user.email", "test@example.invalid")
        (self.root / "README.md").write_text("no production data\n")
        self.g("add", "README.md")
        self.g("commit", "-m", "baseline")

    def g(self, *args):
        p = subprocess.run(["git", "-C", str(self.root), *args],
                           capture_output=True, timeout=5, check=True)
        return p.stdout.decode().strip()

    def test_source_and_parentless_repeatable_snapshot(self):
        sha, tree = m.source_identity()
        self.assertEqual(sha, self.g("rev-parse", "HEAD"))
        self.assertEqual(tree, self.g("rev-parse", "HEAD^{tree}"))
        first, _ = m.snapshot_commit(sha, tree, {})
        second, _ = m.snapshot_commit(sha, tree, {})
        self.assertEqual(first, second)
        self.assertEqual(self.g("rev-parse", first + "^{tree}"), tree)
        self.assertEqual(len(self.g("rev-list", "--parents", "-n", "1", first).split()), 1)

    def test_foreign_branch_denied(self):
        self.g("switch", "-c", "feature")
        with self.assertRaisesRegex(RuntimeError, "source_not_main"):
            m.source_identity()

    def test_explicit_commit_of_any_branch(self):
        self.g("switch", "-c", "feature")
        (self.root / "README.md").write_text("feature\n")
        self.g("commit", "-am", "feature")
        sha = self.g("rev-parse", "HEAD")
        self.g("switch", "main")
        (self.root / "README.md").write_text("dirty main is irrelevant")
        self.assertEqual(m.source_identity(sha), (sha, self.g("rev-parse", sha + "^{tree}")))
        for bad in ("0" * 40, self.g("rev-parse", sha + "^{tree}"), "HEAD", sha[:12]):
            with self.assertRaisesRegex(RuntimeError, "source_commit_unknown"):
                m.source_identity(bad)

    def test_dirty_tracked_denied_without_mutation(self):
        (self.root / "README.md").write_text("changed")
        with self.assertRaisesRegex(RuntimeError, "source_dirty"):
            m.source_identity()

    def test_untracked_never_enters_snapshot(self):
        sha, tree = m.source_identity()
        (self.root / "untracked_secret").write_text("not part of Git HEAD")
        self.assertEqual((sha, tree), m.source_identity())

    def test_official_host_pin_exact(self):
        good = "4096 " + m.FINGERPRINT + " gitverse.ru (RSA)\n"
        self.assertTrue(m.pin_matches(good))
        self.assertFalse(m.pin_matches(good.replace("gitverse.ru", "evil.invalid")))
        self.assertFalse(m.pin_matches(good.replace(m.FINGERPRINT, "SHA256:wrong")))
        self.assertFalse(m.pin_matches(""))


if __name__ == "__main__":
    unittest.main()


class FailureReasonTests(unittest.TestCase):
    def setUp(self):
        # run() uses REPO as cwd; CI runners have no /root/repos/mcpshka.
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.original = m.REPO
        m.REPO = Path(tmp.name)
        self.addCleanup(lambda: setattr(m, "REPO", self.original))

    def test_failed_step_is_named_and_remote_hidden(self):
        e = m.classify("push", b"ssh: connect to host gitverse.ru port 22: Connection timed out\r\nfatal: Could not read from remote repository.\n")
        self.assertEqual(str(e), "push_network")
        e = m.classify("push", b"remote: error: denied by hook\nTo git@gitverse.ru:owner/repo.git\n ! [remote rejected]\n")
        self.assertEqual(str(e), "push_remote_rejected")
        self.assertNotIn("git@gitverse.ru:", e.detail)
        self.assertEqual(str(m.classify("ls_remote", b"git@gitverse.ru: Permission denied (publickey).")), "ls_remote_auth")
        with self.assertRaises(m.StepFailed) as cm:
            m.run(["git", "-C", "/nonexistent-repo-for-test", "status"])
        self.assertRegex(str(cm.exception), r"^[a-z_]{3,60}$")
        with self.assertRaises(m.StepFailed) as cm:
            m.run(["sleep", "5"], timeout=0.2)
        self.assertEqual(str(cm.exception), "sleep_timeout")
