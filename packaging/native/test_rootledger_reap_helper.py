"""Pinned helper regression tests; no privileged Docker or production writes."""
import importlib.util
from pathlib import Path
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("reap", Path(__file__).with_name("rootledger-reap.py"))
reap = importlib.util.module_from_spec(spec)
spec.loader.exec_module(reap)

class ContractTest(unittest.TestCase):
    def test_identity_and_persistent_patch(self):
        self.assertTrue(reap.valid_id("0123456789abcdef"))
        for bad in ("../root", "", "A" * 16, "0" * 17):
            self.assertFalse(reap.valid_id(bad))
        original = "services:\n  app:\n    ports:\n      - '127.0.0.1:8766:8000'\n"
        fixed = reap.vpn_patch(original)
        self.assertIn("    init: true\n", fixed)
        self.assertIn('"test": ["CMD", "python"', fixed)
        self.assertTrue(fixed.endswith(original.split("  app:\n")[1]))
        with self.assertRaises(RuntimeError):
            reap.vpn_patch(fixed)
        self.assertEqual(reap.overlay("sha256:" + "a"*64, True, {})["services"]["app"]["image"], "sha256:"+"a"*64)
        for bad in ("rootledger:latest", "sha256:abc", "sha256:" + "G"*64):
            with self.assertRaises(ValueError):
                reap.overlay(bad, True, {})

    def test_app_only_and_full_compose(self):
        env = b"ROOTLEDGER_HTTPS_PROFILE=private\n"
        with patch.object(Path, "read_bytes", return_value=env):
            args = reap.compose(Path("/var/lib/mcpshka/rootledger-reap/0123456789abcdef/fixed.json"))
        self.assertIn(str(reap.ROOT / "docker-compose.vpn.yml"), args)
        self.assertIn(str(reap.ROOT / "docker-compose.private-https.yml"), args)
        self.assertEqual(args[-2], "-f")
        self.assertNotIn("--privileged", args)

    def test_failures_do_not_publish_command_secrets(self):
        import subprocess
        result = subprocess.CompletedProcess(["docker"], 1, b"", b"SECRET")
        with patch.object(subprocess, "run", return_value=result):
            with self.assertRaises(RuntimeError) as caught:
                reap.run(["docker", "inspect"])
        self.assertNotIn("SECRET", str(caught.exception))

if __name__ == "__main__":
    unittest.main()
