"""Offline CI contract checks; never execute Go or a build on the host."""
import importlib.util
import json
from pathlib import Path
import re
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("run_ci", Path(__file__).with_name("run_ci.py"))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class CITests(unittest.TestCase):
    def test_image_matches_go_mod_and_snapshot_only(self):
        root = Path(__file__).resolve().parents[2]
        version = re.search(r"^go (\S+)$", (root / "go.mod").read_text(), re.M)[1]
        yaml = (root / ".gitlab-ci.yml").read_text()
        self.assertIn("MOSHUB_CI_IMAGE: golang:" + version + "-trixie", yaml)
        self.assertIn("image: $MOSHUB_CI_IMAGE", yaml)
        self.assertIn("if ! id moshub", yaml)
        self.assertIn(r"/^mcpshka-snapshots\/[0-9a-f]{40}$/", yaml)
        self.assertIn("- when: never", yaml)
        self.assertIn("when: always", yaml)
        self.assertIn("tags: [appsechub]", yaml)
        self.assertIn("https://mirror.yandex.ru", yaml)
        self.assertIn("python3 sqlite3", yaml)
        self.assertIn("runuser -u moshub --", yaml)

    def test_offline_image_is_built_off_snapshot_branches(self):
        root = Path(__file__).resolve().parents[2]
        flow = (root / ".gitverse" / "workflows" / "moshub-ci-image.yml").read_text()
        self.assertNotIn("mcpshka-snapshots", flow.split("jobs:")[0].replace("Never on mcpshka-snapshots", ""))
        self.assertIn("secrets.MOSHUB_REGISTRY_TOKEN", flow)
        self.assertIn("--password-stdin", flow)
        build = (root / "packaging" / "moshub" / "ci-image" / "build.sh").read_text()
        self.assertIn('${PLATFORMS:-linux/amd64}', build)

    def test_source_identity(self):
        sha, tree = "a" * 40, "b" * 40
        msg = f"Source-Commit: {sha}\nSource-Tree: {tree}\n"
        self.assertEqual(m.identity("mcpshka-snapshots/" + sha, msg, tree), sha)
        for branch, message, expected in [("main", msg, tree), ("mcpshka-snapshots/" + sha, msg, sha), ("mcpshka-snapshots/" + sha, msg + msg, tree)]:
            with self.assertRaises(ValueError):
                m.identity(branch, message, expected)

    def test_race_share_success_failure_timeout(self):
        share = subprocess.CompletedProcess([], 0, stdout="./internal/a ./internal/b\n")
        for code in (0, 1, 124):
            with self.subTest(code=code), tempfile.TemporaryDirectory() as folder:
                out = Path(folder)
                test = subprocess.TimeoutExpired("go", 1500) if code == 124 else subprocess.CompletedProcess([], code)
                with patch.object(m.subprocess, "run", side_effect=[share, test]) as run:
                    self.assertEqual(m.run_checks("a" * 40, "b" * 40, out, sites="github,moshub"), int(code != 0))
                report = json.loads((out / "summary.json").read_text())
                self.assertEqual(report["exit_codes"], [code])
                self.assertEqual(report["schema"], "mcpshka.cloud-results/v1")
                self.assertEqual(run.call_args_list[0].args[0][:4], ["python3", "packaging/ci/shard.py", "--site", "moshub"])
                # MosHub runs only its race share: never the full ./... GitVerse gate.
                self.assertEqual(run.call_args_list[1].args[0], ["go", "test", "-race", "-count=1", "-p", "2", "./internal/a", "./internal/b"])
                self.assertEqual(report["packages"], ["./internal/a", "./internal/b"])
                self.assertEqual((report["source_sha"], report["tree"]), ("a" * 40, "b" * 40))

    def test_empty_share_passes_without_running_go(self):
        with tempfile.TemporaryDirectory() as folder:
            empty = subprocess.CompletedProcess([], 0, stdout="\n")
            with patch.object(m.subprocess, "run", side_effect=[empty]) as run:
                self.assertEqual(m.run_checks("a" * 40, "b" * 40, Path(folder), sites="github"), 0)
            self.assertEqual(run.call_count, 1)

    def test_planner_failure_is_a_failure(self):
        with tempfile.TemporaryDirectory() as folder:
            with patch.object(m.subprocess, "run", side_effect=subprocess.CalledProcessError(1, "shard")):
                self.assertEqual(m.run_checks("a" * 40, "b" * 40, Path(folder)), 1)

    def test_cloud_build_timeout_preserves_local_default(self):
        path = Path(__file__).resolve().parents[1] / "lab" / "build_matrix.py"
        spec = importlib.util.spec_from_file_location("build_matrix", path)
        matrix = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(matrix)
        with tempfile.TemporaryDirectory() as folder:
            out = Path(folder)
            (out / "mcpshka-linux-arm64").write_bytes(b"fixture")
            with patch.object(matrix, "elf_machine", return_value=183), patch.object(matrix.subprocess, "run", return_value=subprocess.CompletedProcess([], 0)) as run:
                matrix.build_one(out, out, "arm64")
                self.assertEqual(run.call_args.kwargs["timeout"], 180)
                matrix.build_one(out, out, "arm64", 720)
                self.assertEqual(run.call_args.kwargs["timeout"], 720)
        for invalid in ("0", "781"):
            result = subprocess.run(["python3", "-B", str(path), "--timeout", invalid], capture_output=True)
            self.assertEqual(result.returncode, 2)
