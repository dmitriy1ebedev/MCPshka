"""Exercise the actual deploy health functions with bounded dependency fixtures."""
from pathlib import Path
import subprocess
import unittest

SOURCE = Path(__file__).with_name("self-deploy.sh").read_text()

def function(name):
    return name + "() {" + SOURCE.split(name + "() {", 1)[1].split("\n}\n", 1)[0] + "\n}\n"

class DeployHealthTests(unittest.TestCase):
    def test_tunnel_waits_for_matching_controller_health(self):
        for version in ('0.1.109', '0.1.108'):
            with self.subTest(version=version):
                script = '''
TUNNEL=mcpshka-tunnel.service
HTTP_READY=no
sleep() { HTTP_READY=yes; }
curl() {
  [[ "$HTTP_READY" == yes ]] || return 7
  echo '{"status":"ok","version":"''' + version + '''"}'
}
systemctl() {
  [[ "$HTTP_READY" == yes && "$*" == 'start mcpshka-tunnel.service' ]] || exit 99
  echo TUNNEL_STARTED
}
'''
                script += function('start_tunnel_after_http') + 'start_tunnel_after_http 0.1.109\n'
                result = subprocess.run(['/bin/bash', '-c', script], capture_output=True, timeout=3)
                self.assertEqual(result.returncode, 0 if version == '0.1.109' else 1, result.stderr.decode())
                self.assertEqual(result.stdout.strip(), b'TUNNEL_STARTED' if version == '0.1.109' else b'')

    def test_external_outage_does_not_mask_local_or_tunnel_result(self):
        for ready in (True, False):
            with self.subTest(ready=ready):
                script = r'''
SERVICE=mcpshka.service
TUNNEL=mcpshka-tunnel.service
systemctl() { return 0; }
sleep() { :; }
timeout() { return 1; }
curl() {
  case "${*: -1}" in
    http://127.0.0.1:8765/healthz) echo '{"status":"ok","version":"0.1.99"}' ;;
    http://127.0.0.1:8768/readyz) echo "$READY" ;;
    https://api.openai.com/) return 1 ;;
    *) return 99 ;;
  esac
}
'''
                script += "READY=" + ("ready" if ready else "unavailable") + "\n"
                script += function("observe_external_connectivity") + function("verify_runtime")
                script += 'observe_external_connectivity\n[[ "$EXTERNAL_DNS" == unavailable && "$EXTERNAL_HTTPS" == unavailable ]] || exit 99\nverify_runtime 0.1.99\n'
                result = subprocess.run(["/bin/bash", "-c", script], capture_output=True, timeout=3)
                self.assertEqual(result.returncode, 0 if ready else 1, result.stderr.decode())

    def test_hash_pin_rejects_same_version_replaced_bytes(self):
        import hashlib
        import tempfile
        with tempfile.TemporaryDirectory() as tmp:
            image = Path(tmp) / 'candidate'
            image.write_bytes(b'approved')
            expected = hashlib.sha256(b'approved').hexdigest()
            script = 'set -eu\n' + function('verify_hash_pin')
            good = subprocess.run(['/bin/bash', '-c', script + 'verify_hash_pin "$1" "$2"', 'test',
                                   str(image), expected], capture_output=True, timeout=3)
            self.assertEqual(good.returncode, 0, good.stderr.decode())
            image.write_bytes(b'same-version-evil')
            changed = subprocess.run(['/bin/bash', '-c', script + 'verify_hash_pin "$1" "$2"', 'test',
                                      str(image), expected], capture_output=True, timeout=3)
            self.assertNotEqual(changed.returncode, 0)

if __name__ == "__main__":
    unittest.main()
