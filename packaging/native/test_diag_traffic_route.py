"""MCP-153: hard byte cap, MAC binding during capture, idempotent stop with a cleanup receipt."""
import argparse
import importlib.util
import json
import subprocess
import tempfile
from pathlib import Path
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("trafficroute", Path(__file__).with_name("diag-traffic-route-observe.py"))
tr = importlib.util.module_from_spec(spec)
spec.loader.exec_module(tr)

ARP = "IP address HW type Flags HW address Mask Device\n192.168.0.50 0x1 0x2 {mac} * eth0\n"


class TrafficRouteTest(unittest.TestCase):
    def test_bounds(self):
        self.assertEqual(tr.MAX_DURATION, 300)
        self.assertEqual(tr.PCAP_BYTE_CAP, 32 * 1024 * 1024)
        src = Path(tr.__file__).read_text()
        self.assertNotIn('"-W"', src)
        self.assertIn("limit=PCAP_BYTE_CAP", src)
        args = argparse.Namespace(dry_run=True, duration=301, client="192.168.77.50", mac=None, root="/tmp")
        with patch("os.geteuid", return_value=0):
            self.assertEqual(tr.start(args), 2)

    def test_mac_mismatch_only_on_other_device(self):
        def arp(mac):
            return patch.object(tr.Path, "read_text", return_value=ARP.format(mac=mac))
        with arp("aa:bb:cc:dd:ee:ff"):
            self.assertEqual(tr.arp_mismatch("192.168.77.50", "aa:bb:cc:dd:ee:ff"), "")
        with arp("11:22:33:44:55:66"):
            self.assertEqual(tr.arp_mismatch("192.168.77.50", "aa:bb:cc:dd:ee:ff"), "11:22:33:44:55:66")
        with arp("00:00:00:00:00:00"):
            self.assertEqual(tr.arp_mismatch("192.168.77.50", "aa:bb:cc:dd:ee:ff"), "")

    def test_stop_forces_cleanup_and_is_idempotent(self):
        with tempfile.TemporaryDirectory() as root:
            run = Path(root) / "20261109T000000Z-abcdefgh"
            run.mkdir()
            (run / "traffic.pcap").write_bytes(b"x" * 10)
            dead = subprocess.Popen(["true"])
            dead.wait()
            tr.write_state(run, state="RUNNING", pid=dead.pid, observer_pids={"tcpdump": dead.pid})
            args = argparse.Namespace(root=root, run=run.name, json=True)
            with patch.object(tr, "STOP_WAIT_SECONDS", 0):
                self.assertEqual(tr.stop(args), 0)
                first = json.loads((run / "state.json").read_text())
                self.assertEqual(first["state"], "CANCELLED")
                self.assertTrue(first["cleanup"]["all_stopped"])
                self.assertEqual(first["pcap_bytes"], 10)
                self.assertEqual(tr.stop(args), 0)
                self.assertEqual(json.loads((run / "state.json").read_text())["finished_at"], first["finished_at"])

    def test_stop_request_ends_worker_loop(self):
        src = Path(tr.__file__).read_text()
        self.assertIn('ended = "cancelled"', src)
        self.assertIn("cleanup = cleanup_receipt(observers)", src)


if __name__ == "__main__":
    unittest.main()
