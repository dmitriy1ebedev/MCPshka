import importlib.util
import os
from pathlib import Path
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("probe", Path(__file__).with_name("release-probe.py"))
probe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(probe)

class ReleaseProbeTests(unittest.TestCase):
    def test_fixed_sources_and_operations(self):
        for path in ("/usr/local/bin/mcpshka", "/usr/local/bin/mcpshka.previous", "/root/repos/mcpshka/bin/mcpshka-0.1.99-r1.candidate", "/usr/local/bin/.mcpshka.restore.abc123"):
            self.assertTrue(probe.allowed_source(path), path)
        for path in ("/etc/mcpshka/auth.json", "/usr/local/bin/sh", "/root/repos/other/bin/mcpshka-a.candidate", "/root/repos/mcpshka/bin/../secret", "/root/repos/mcpshka/bin/not-a-candidate", "/usr/local/bin/.mcpshka.probe.any"):
            self.assertFalse(probe.allowed_source(path), path)
        with self.assertRaises(ValueError):
            probe.unit_args("mcpshka-probe-0123456789abcdef.service", "image", "shell")

    def test_execution_is_unprivileged_and_bounded(self):
        args = probe.unit_args("mcpshka-probe-0123456789abcdef.service", "/usr/local/bin/.mcpshka.probe.abc", "version")
        for property in ("DynamicUser=yes", "User=mcpshka-probe-0123456789abcdef", "NoNewPrivileges=yes", "CapabilityBoundingSet=", "PrivateNetwork=yes", "ProtectHome=yes", "ProtectSystem=strict", "MemoryMax=128M", "CPUQuota=25%", "TasksMax=24", "RuntimeMaxSec=5s", "LimitFSIZE=4096", "KillMode=control-group"):
            self.assertIn("--property=" + property, args)
        self.assertNotIn("--property=User=root", args)
        self.assertIn("--property=BindReadOnlyPaths=/usr/local/bin/.mcpshka.probe.abc:/run/mcpshka-probe-image", args)
        self.assertEqual(args[-7:], ["/usr/bin/env", "-i", "HOME=/nonexistent", "PATH=/usr/bin:/bin", "LANG=C", "/run/mcpshka-probe-image", "version"])

    def test_stage_rejects_aliases_and_copies_independent_inode(self):
        with tempfile.TemporaryDirectory(dir="/var/tmp") as tmp:
            source = Path(tmp) / "source"
            source.write_bytes(b"bounded image")
            image = Path(probe.stage_image(str(source), tmp))
            self.assertEqual(image.read_bytes(), source.read_bytes())
            self.assertNotEqual(image.stat().st_ino, source.stat().st_ino)
            self.assertEqual(image.stat().st_mode & 0o777, 0o555)
            image.unlink()
            alias = Path(tmp) / "alias"
            alias.symlink_to(source)
            with self.assertRaises((OSError, ValueError)):
                probe.stage_image(str(alias), tmp)
            alias.unlink()
            os.link(source, alias)
            with self.assertRaises(ValueError):
                probe.stage_image(str(source), tmp)

    def test_unknown_fence_preserves_image_and_confirmed_exit_removes_it(self):
        for fenced in (True, False):
            with self.subTest(fenced=fenced), tempfile.TemporaryDirectory(dir="/var/tmp") as tmp:
                image = Path(tmp) / "image"
                image.write_bytes(b"image")
                def run(args, **kwargs):
                    if args[0] == "/usr/bin/systemd-run":
                        kwargs["stdout"].write(b"mcpshka 0.1.99\n")
                    return SimpleNamespace(returncode=0)
                with patch.object(probe, "PROBE_ROOT", tmp), patch.object(probe, "stage_image", return_value=str(image)), patch.object(probe, "stopped", return_value=fenced), patch.object(probe.subprocess, "run", side_effect=run), patch.object(probe.pwd, "getpwnam", side_effect=KeyError):
                    if fenced:
                        self.assertEqual(probe.run_probe("source", "version"), "mcpshka 0.1.99")
                    else:
                        with self.assertRaisesRegex(ValueError, "fencing unverified"):
                            probe.run_probe("source", "version")
                self.assertEqual(image.exists(), not fenced)

    def test_launch_timeout_preserves_image_even_when_unit_is_absent(self):
        with tempfile.TemporaryDirectory(dir="/var/tmp") as tmp:
            image = Path(tmp) / "image"
            image.write_bytes(b"image")
            def run(args, **kwargs):
                if args[0] == "/usr/bin/systemd-run":
                    raise subprocess.TimeoutExpired(args, 10)
                return SimpleNamespace(returncode=0)
            with patch.object(probe,"PROBE_ROOT",tmp), patch.object(probe,"stage_image",return_value=str(image)), patch.object(probe,"stopped",return_value=True), patch.object(probe.subprocess,"run",side_effect=run), patch.object(probe.pwd,"getpwnam",side_effect=KeyError):
                with self.assertRaises(subprocess.TimeoutExpired):
                    probe.run_probe("source","version")
            self.assertTrue(image.exists())

    def test_private_storage_rejects_wrong_owner_permissions_and_alias(self):
        with tempfile.TemporaryDirectory(dir="/var/tmp") as tmp:
            scratch = Path(tmp) / "probe"
            with patch.object(probe, "PROBE_ROOT", str(scratch)), patch.object(probe, "run_probe", return_value="mcpshka 0.1.99"):
                if os.getuid() != 0:
                    with self.assertRaisesRegex(ValueError, "storage unverified"):
                        probe.probe("source", "version")
                    return
                self.assertEqual(probe.probe("source", "version"), "mcpshka 0.1.99")
                scratch.chmod(0o755)
                with self.assertRaisesRegex(ValueError, "storage unverified"):
                    probe.probe("source", "version")
                scratch.chmod(0o700)
                scratch.rename(Path(tmp) / "original")
                scratch.symlink_to(Path(tmp) / "original", target_is_directory=True)
                with self.assertRaisesRegex(ValueError, "storage unverified"):
                    probe.probe("source", "version")

if __name__ == "__main__":
    unittest.main()
