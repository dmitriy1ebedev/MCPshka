import importlib.util
import hashlib
import io
from pathlib import Path
import os
import subprocess
import sys
import signal
import tempfile
import unittest

sp = importlib.util.spec_from_file_location("source_digest", Path(__file__).with_name("source_digest.py"))
module = importlib.util.module_from_spec(sp)
sp.loader.exec_module(module)

class SourceDigestTests(unittest.TestCase):
    def run_git(self, root, *args):
        subprocess.run(["git", *args], cwd=root, check=True, stdout=subprocess.DEVNULL,
                       stderr=subprocess.DEVNULL)
    def test_parent_history_and_docs_do_not_change_digest(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            self.run_git(root, "init", "-q")
            self.run_git(root, "config", "user.name", "Test")
            self.run_git(root, "config", "user.email", "test@example.invalid")
            (root / "go.mod").write_text("module example.com/dummy\n\ngo 1.23\n")
            (root / "internal").mkdir()
            (root / "internal" / "a.go").write_text("package internal\n")
            self.run_git(root, "add", ".")
            self.run_git(root, "commit", "-qm", "initial")
            first = module.source_digest(root)
            (root / "README.md").write_text("docs only")
            self.run_git(root, "add", ".")
            self.run_git(root, "commit", "-qm", "docs")
            second = module.source_digest(root)
            self.assertEqual(first["source_digest"], second["source_digest"])
            (root / ".github" / "workflows").mkdir(parents=True)
            (root / ".github" / "workflows" / "ci.yml").write_text("name: test")
            self.run_git(root, "add", ".")
            self.run_git(root, "commit", "-qm", "ci")
            ci = module.source_digest(root)
            self.assertNotEqual(second["source_digest"], ci["source_digest"])
            (root / "skills" / "cloud-agent-handoff").mkdir(parents=True)
            (root / "skills" / "cloud-agent-handoff" / "SKILL.md").write_text("name: pinned")
            self.run_git(root, "add", ".")
            self.run_git(root, "commit", "-qm", "skill")
            skill = module.source_digest(root)
            self.assertNotEqual(ci["source_digest"], skill["source_digest"])
            (root / "internal" / "a.go").write_text("package internal\n// changed\n")
            self.run_git(root, "add", ".")
            self.run_git(root, "commit", "-qm", "code")
            self.assertNotEqual(second["source_digest"], module.source_digest(root)["source_digest"])
    def test_bounded_hash_matches_reference_and_rejects_truncation(self):
        data = (b"source-digest" * 200000) + b"ending"
        class BoundedStream(io.BytesIO):
            max_read = 0
            def read(self, n=-1):
                self.max_read = max(self.max_read, n)
                if n < 0 or n > module.CHUNK_SIZE:
                    raise AssertionError("hash tried to allocate an oversized buffer")
                return super().read(n)
        stream = BoundedStream(data)
        digest = module.hash_stream(stream, len(data))
        self.assertEqual(digest, hashlib.sha256(data).digest())
        self.assertEqual(stream.max_read, module.CHUNK_SIZE)
        self.assertEqual(stream.tell(), len(data))
        with self.assertRaises(module.InvalidSource):
            module.hash_stream(io.BytesIO(b"short"), 128)
        with self.assertRaises(module.InvalidSource):
            module.hash_stream(io.BytesIO(), module.MAX_FILE + 1)

    def test_large_committed_file_keeps_source_identity(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            self.run_git(root, "init", "-q")
            self.run_git(root, "config", "user.name", "Test")
            self.run_git(root, "config", "user.email", "test@example.invalid")
            go_mod = b"module example.com/large\\n\\ngo 1.26\\n"
            payload = b"package large\\n/*" + b"data" * 400000 + b"*/\\n"
            (root / "go.mod").write_bytes(go_mod)
            (root / "internal").mkdir()
            (root / "internal" / "large.go").write_bytes(payload)
            self.run_git(root, "add", ".")
            self.run_git(root, "commit", "-qm", "large fixture")
            result = module.source_digest(root)
            expected = hashlib.sha256(b"mcpshka-release-source/v1\0")
            for name, content in sorted((("go.mod", go_mod), ("internal/large.go", payload))):
                expected.update(name.encode() + b"\0" + hashlib.sha256(content).digest())
            self.assertEqual(result["source_digest"], expected.hexdigest())
            self.assertEqual(result["file_count"], 2)
            self.assertEqual(result["total_bytes"], len(go_mod)+len(payload))

    def test_uncommitted_changes_do_not_contaminate_digest(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            self.run_git(root, "init", "-q")
            self.run_git(root, "config", "user.name", "Test")
            self.run_git(root, "config", "user.email", "test@example.invalid")
            (root / "go.mod").write_text("module x\n\ngo 1.23\n")
            self.run_git(root, "add", ".")
            self.run_git(root, "commit", "-qm", "source")
            old = module.source_digest(root)["source_digest"]
            (root / "go.mod").write_text("changed")
            self.assertEqual(old, module.source_digest(root)["source_digest"])



    def test_stalled_archive_is_killed_and_reaped(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            runner = """import importlib.util, pathlib, subprocess, sys
sp = importlib.util.spec_from_file_location('digest', sys.argv[1])
m = importlib.util.module_from_spec(sp)
sp.loader.exec_module(m)
real_popen = subprocess.Popen
def stalled_archive(argv, **kwargs):
    assert argv[0] == 'git' and argv[3] == 'archive'
    child = real_popen([sys.executable, '-c', 'import time; time.sleep(30)'], **kwargs)
    pathlib.Path(sys.argv[2], 'archive.pid').write_text(str(child.pid))
    return child
m.subprocess.Popen = stalled_archive
m.ARCHIVE_TIMEOUT = 0.1
try:
    m.source_digest(sys.argv[2])
except m.InvalidSource:
    sys.exit(0)
sys.exit(1)
"""
            worker = subprocess.Popen([sys.executable, "-c", runner, str(Path(module.__file__)), d],
                                      stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                      start_new_session=True)
            try:
                _, errors = worker.communicate(timeout=3)
            except subprocess.TimeoutExpired:
                os.killpg(worker.pid, signal.SIGKILL)
                worker.communicate()
                self.fail("archive streaming exceeded deadline")
            self.assertEqual(worker.returncode, 0, errors.decode())
            pid = int((root / "archive.pid").read_text())
            with self.assertRaises(ProcessLookupError):
                os.kill(pid, 0)

if __name__ == "__main__":
    unittest.main()
