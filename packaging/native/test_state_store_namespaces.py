"""New runtime namespaces survive validation, export and failed downgrade."""
import importlib.util
import pathlib
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("store", pathlib.Path(__file__).with_name("state-store.py"))
store = importlib.util.module_from_spec(spec)
spec.loader.exec_module(store)


class NamespaceTest(unittest.TestCase):
    def test_pipeline_records_survive_activation_and_export(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp) / "state"
            root.mkdir(mode=0o700)
            store.activate(root)
            key = "pipelines/v1/maintenance_cursor"
            store.write(root, key, b"canary")
            self.assertEqual(store.activate(root)["records"], 1)
            self.assertEqual(store.status(root)["records"], 1)
            destination = pathlib.Path(tmp) / "export"
            store.export(root, destination)
            self.assertEqual((destination / key).read_bytes(), b"canary")
            with self.assertRaises(ValueError):
                store.deactivate(root)
            self.assertTrue((root / "state.mode").exists())
            self.assertEqual(store.status(root)["records"], 1)

    def test_large_authority_status_preserves_validation(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            store.activate(root)
            payload = b"canary"
            digest = store.hashlib.sha256(payload).hexdigest()
            with store.connect(root / "state.db") as db:
                db.executemany("INSERT INTO records VALUES(?,?,?)",
                               ((f"jobs/{i:05d}", payload, digest) for i in range(10001)))
            self.assertEqual(store.status(root)["records"], 10001)
            self.assertEqual(store.activate(root)["records"], 10001)
            self.assertEqual(store.status(root)["payload_bytes"], 60006)
            with store.connect(root / "state.db") as db:
                # Production already holds >16k records; export must work.
                self.assertEqual(len(store.records(db)), 10001)
                limit = store.MAX_RECORDS
                store.MAX_RECORDS = 10000
                try:
                    with self.assertRaisesRegex(ValueError, "record budget"):
                        store.records(db)
                finally:
                    store.MAX_RECORDS = limit
                db.execute("UPDATE records SET sha256='invalid' WHERE path='jobs/10000'")
            with self.assertRaisesRegex(ValueError, "invalid record"):
                store.status(root)
            with self.assertRaisesRegex(ValueError, "invalid record"):
                store.activate(root)

    def test_deadline_scales_with_database_size(self):
        mib = 1 << 20
        self.assertEqual(store.deadline_seconds(0), 15)
        self.assertEqual(store.deadline_seconds(1), 17)
        self.assertEqual(store.deadline_seconds(32 * mib), 79)
        self.assertEqual(store.deadline_seconds(64 * mib), 143)
        self.assertEqual(store.deadline_seconds(store.MAX_BYTES), store.MAX_SECONDS)
        self.assertEqual(store.MAX_BYTES, 256 * mib)
        self.assertEqual(store.deadline_seconds(1 << 40), store.MAX_SECONDS)

    def test_unsafe_keys_fail(self):
        for key in ("", ".", "../x", "/x", "x/../y", "x//y", "x\x00y"):
            self.assertFalse(store.key_ok(key), key)


if __name__ == "__main__":
    unittest.main()
