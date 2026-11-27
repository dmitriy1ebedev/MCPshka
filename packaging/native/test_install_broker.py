import os
import json
import sys
from pathlib import Path
import tempfile
import unittest


class InstallerInputTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.script = Path(__file__).with_name("install-broker.sh").read_text()
        code = cls.script.split("<<'RELEASE_INPUTS'\n", 1)[1].split("\nRELEASE_INPUTS", 1)[0]
        namespace = {}
        exec(code.rsplit("\n_verify_inputs(sys.argv[1:], 0)", 1)[0], namespace)
        cls.verify = staticmethod(namespace["_verify_inputs"])

    def test_preflight_precedes_all_installs_and_checks_service_uid(self):
        self.assertLess(self.script.index("RELEASE_INPUTS\n"), self.script.index("groupadd --system"))
        self.assertIn('_verify_inputs(sys.argv[1:], 0)', self.script)
        self.assertIn('"$BROKER_TARGET" call systemctl show', self.script)
        self.assertNotIn('runuser -u "$MCPSHKA_USER"', self.script)
        self.assertIn('other PIDs are denied', self.script)
        for name in ("release-probe.py", "state-store.py", "codex-bridge.py", "rescue.py"):
            self.assertIn(name, self.script.split("RELEASE_INPUTS\n", 1)[0])

    def test_rescue_helper_is_preflighted_and_installed_without_enabling_timer(self):
        self.assertTrue(Path(__file__).with_name("rescue.py").is_file())
        preflight=self.script.split("RELEASE_INPUTS\n",1)[0]
        self.assertIn('"$RESCUE_SOURCE"',preflight)
        self.assertIn('RESCUE_TARGET="/usr/local/libexec/mcpshka-rescue.py"',self.script)
        self.assertIn('install -o root -g root -m 0755 "$RESCUE_SOURCE" "$RESCUE_TARGET"',self.script)
        self.assertNotIn('enable mcpshka-rescue.timer',self.script)
        self.assertNotIn('start mcpshka-rescue.timer',self.script)

    def test_release_gate_and_guarded_helper_have_distinct_installed_paths(self):
        self.assertIn('SELF_DEPLOY_SOURCE="$ROOT_DIR/packaging/native/release-gate.py"', self.script)
        self.assertIn('SELF_DEPLOY_TARGET="/usr/local/libexec/mcpshka-self-deploy"', self.script)
        self.assertIn('GUARDED_DEPLOY_SOURCE="$ROOT_DIR/packaging/native/self-deploy.sh"', self.script)
        self.assertIn('GUARDED_DEPLOY_TARGET="/usr/local/libexec/mcpshka-self-deploy-guarded"', self.script)
        self.assertIn('"$SELF_DEPLOY_SOURCE" "$GUARDED_DEPLOY_SOURCE"', self.script.split('RELEASE_INPUTS\n', 1)[0])
        self.assertIn('install -o root -g root -m 0755 "$GUARDED_DEPLOY_SOURCE" "$GUARDED_DEPLOY_TARGET"', self.script)

    def test_real_inputs_reject_aliases_links_permissions_and_bad_python(self):
        with tempfile.TemporaryDirectory(dir="/var/tmp") as tmp:
            root = Path(tmp)
            source = root / "helper.py"
            source.write_text("pass\n")
            self.verify([str(source)], os.getuid())
            source.chmod(0o666)
            with self.assertRaises(ValueError):
                self.verify([str(source)], os.getuid())
            source.chmod(0o600)
            alias = root / "alias.py"
            alias.symlink_to(source)
            with self.assertRaises(ValueError):
                self.verify([str(alias)], os.getuid())
            alias.unlink()
            os.link(source, alias)
            with self.assertRaises(ValueError):
                self.verify([str(source)], os.getuid())
            alias.unlink()
            root.chmod(0o777)
            with self.assertRaises(ValueError):
                self.verify([str(source)], os.getuid())
            root.chmod(0o700)
            source.write_text("broken python (\n")
            with self.assertRaises(SyntaxError):
                self.verify([str(source)], os.getuid())
            with self.assertRaises(FileNotFoundError):
                self.verify([str(root / "missing")], os.getuid())
            if os.getuid() != 0:
                source.write_text("pass\n")
                with self.assertRaises(ValueError):
                    self.verify([str(source)], 0)

    def test_docker_scopes_are_persistent_exact_and_separate(self):
        code = self.script.split('python3 - "$ENV_FILE" "$config_tmp" <<\'PY\'\n', 1)[1].split('\nPY', 1)[0]
        with tempfile.TemporaryDirectory(dir="/var/tmp") as tmp:
            env = Path(tmp) / "env"
            out = Path(tmp) / "broker.json"
            env.write_text("MCPSHKA_DOCKER_READ_CONTAINERS=homeassistant,torrserver\nMCPSHKA_DOCKER_TERMINAL_CONTAINERS=homeassistant,torrserver\n")
            previous = sys.argv
            try:
                sys.argv = ["fixture", str(env), str(out)]
                exec(compile(code, "installer-config", "exec"), {})
                config = json.loads(out.read_text())
                self.assertEqual(config["docker_read"], ["homeassistant", "torrserver"])
                self.assertEqual(config["docker_terminal"], ["homeassistant", "torrserver"])
                self.assertEqual(config["docker_mutation"], [])
                self.assertEqual(config["docker_restart"], [])
                env.write_text("")
                exec(compile(code, "installer-config", "exec"), {})
                config = json.loads(out.read_text())
                self.assertEqual(config["docker_read"], [])
                self.assertEqual(config["docker_terminal"], [])
            finally:
                sys.argv = previous


if __name__ == "__main__":
    unittest.main()
