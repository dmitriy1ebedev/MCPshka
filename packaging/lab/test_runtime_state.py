#!/usr/bin/env python3
import importlib.util,pathlib,tempfile,unittest,sqlite3,json
spec=importlib.util.spec_from_file_location('state',pathlib.Path(__file__).parents[1]/'native/state-store.py');state=importlib.util.module_from_spec(spec);spec.loader.exec_module(state)
class RuntimeState(unittest.TestCase):
 def test_migration_latest_export_and_clean_restore(self):
  with tempfile.TemporaryDirectory() as td:
   root=pathlib.Path(td);state.atomic(root/'jobs/a/metadata.json',b'{"id":"a","state":"done"}\n');state.atomic(root/'leases/a.json',b'{"owner":"a"}')
   baseline=state.legacy(root);state.activate(root);self.assertEqual(state.status(root)['records'],2)
   state.migrate(root,2);state.migrate(root,1)
   with state.connect(root/'state.db') as db:self.assertEqual(state.records(db),baseline)
   state.export(root,root/'restored');self.assertEqual(state.legacy(root/'restored'),baseline)
   state.write(root,'jobs/a/metadata.json',b'{"id":"a","state":"failed"}')
   with state.connect(root/'state.db') as db:db.execute("DELETE FROM records WHERE path='leases/a.json'")
   state.deactivate(root);self.assertEqual(state.status(root)['mode'],'legacy');self.assertFalse((root/'leases/a.json').exists())
   self.assertEqual((root/'jobs/a/metadata.json').read_bytes(),b'{"id":"a","state":"failed"}')
   state.activate(root);self.assertEqual(state.status(root)['records'],1)
 def test_corruption_and_schema_fail_closed(self):
  with tempfile.TemporaryDirectory() as td:
   root=pathlib.Path(td);state.atomic(root/'idempotency/a.json',b'{}');state.activate(root)
   db=sqlite3.connect(root/'state.db');db.execute("UPDATE records SET payload=x'00'");db.commit();db.close()
   with self.assertRaises(ValueError):state.deactivate(root)
   self.assertTrue((root/'state.mode').exists());self.assertEqual((root/'idempotency/a.json').read_bytes(),b'{}')
 def test_transaction_crash_preserves_previous_state(self):
  with tempfile.TemporaryDirectory() as td:
   root=pathlib.Path(td);state.atomic(root/'idempotency/a.json',b'{}');state.activate(root)
   import subprocess,sys
   code="import sqlite3,os;d=sqlite3.connect(__import__('sys').argv[1]);d.execute('BEGIN IMMEDIATE');d.execute('DELETE FROM records');os._exit(7)"
   self.assertEqual(subprocess.run([sys.executable,'-c',code,str(root/'state.db')]).returncode,7)
   self.assertEqual(state.status(root)['records'],1)
 def test_symlinks_and_unknown_schema_are_refused(self):
  with tempfile.TemporaryDirectory() as td:
   root=pathlib.Path(td);state.atomic(root/'deploys/a.json',b'{}');state.activate(root)
   db=sqlite3.connect(root/'state.db');db.execute('PRAGMA user_version=99');db.commit();db.close()
   with self.assertRaises(ValueError):state.status(root)
   (root/'link').symlink_to(root/'state.db')
   with self.assertRaises(ValueError):state.safe_path(root/'link')
if __name__=='__main__':unittest.main()
