#!/usr/bin/env python3
import importlib.util
import pathlib
import types
import unittest
from unittest import mock

MODULE_PATH = pathlib.Path(__file__).with_name("migrate-auth-registry.py")
spec = importlib.util.spec_from_file_location("migrate_auth_registry", MODULE_PATH)
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)


class MetadataTests(unittest.TestCase):
    def st(self, mode, uid=0, gid=0):
        return types.SimpleNamespace(st_mode=mode, st_uid=uid, st_gid=gid)

    def test_root_0600_allowed(self):
        mod.validate_auth_registry_metadata(self.st(0o100600))

    def test_root_mcpshka_0640_allowed(self):
        with mock.patch.object(mod.grp, "getgrnam", return_value=types.SimpleNamespace(gr_gid=994)):
            mod.validate_auth_registry_metadata(self.st(0o100640, uid=0, gid=994))

    def test_0640_wrong_group_rejected(self):
        with mock.patch.object(mod.grp, "getgrnam", return_value=types.SimpleNamespace(gr_gid=994)):
            with self.assertRaisesRegex(RuntimeError, "group mcpshka"):
                mod.validate_auth_registry_metadata(self.st(0o100640, uid=0, gid=0))

    def test_world_or_write_bits_rejected(self):
        for mode in (0o100644, 0o100660, 0o100666):
            with self.subTest(mode=oct(mode)):
                with self.assertRaises(RuntimeError):
                    mod.validate_auth_registry_metadata(self.st(mode))

    def test_non_root_owner_rejected(self):
        with self.assertRaisesRegex(RuntimeError, "owned by root"):
            mod.validate_auth_registry_metadata(self.st(0o100600, uid=1000, gid=1000))


if __name__ == "__main__":
    unittest.main()
