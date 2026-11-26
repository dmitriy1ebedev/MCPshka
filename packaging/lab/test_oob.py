"""OOB forced command: fixed commands only, every inbound call is evidence."""
import json,pathlib,tempfile,time,types,unittest
SOURCE=pathlib.Path(__file__).parents[1]/'native/oob.py'
class OOBTests(unittest.TestCase):
 def module(self):
  m=types.ModuleType('oob');exec(compile(SOURCE.read_text(),str(SOURCE),'exec'),m.__dict__)
  self.dir=tempfile.TemporaryDirectory();m.EVIDENCE=pathlib.Path(self.dir.name)/'oob'
  started=[]
  m.rescue=lambda:types.SimpleNamespace(snapshot=lambda:{'main':'active'},start=lambda i,mode:started.append(mode) or {'id':i,'mode':mode,'state':'queued'},receipt_status=lambda i:{'id':i,'state':'done'})
  m.started=started;m.client_allowed=lambda p,c:True;return m
 def tearDown(self):
  if hasattr(self,'dir'):self.dir.cleanup()
 def test_fixed_commands_record_evidence(self):
  m=self.module()
  self.assertEqual(m.summary()['verified_independent_oob'],0)
  out=m.serve('vps','probe '+'a'*32)
  self.assertEqual((out['nonce'],out['path']),('a'*32,'vps'))
  self.assertEqual(m.serve('tailscale','')['rescue'],{'main':'active'})
  self.assertEqual(m.serve('vps','restart')['state'],'queued');self.assertEqual(m.started,['restart'])
  s=m.summary();self.assertEqual(s['verified_independent_oob'],2);self.assertEqual(sorted(s['paths']),['tailscale','vps'])
 def test_unknown_commands_never_run(self):
  m=self.module()
  for line in ('restart; rm -rf /','sh','probe xyz','rollback now','status extra'):
   with self.assertRaises(ValueError):m.serve('vps',line)
  with self.assertRaises(ValueError):m.serve('../x','status')
  self.assertEqual(m.started,[]);self.assertFalse(m.EVIDENCE.exists())
 def test_stale_evidence_does_not_count(self):
  m=self.module();m.serve('vps','status')
  e=json.loads((m.EVIDENCE/'vps.json').read_text());e['at']-=m.MAX_AGE+1;(m.EVIDENCE/'vps.json').write_text(json.dumps(e))
  self.assertEqual(m.summary()['verified_independent_oob'],0)
 def test_paths_accept_only_their_network(self):
  m=types.ModuleType('oob');exec(compile(SOURCE.read_text(),str(SOURCE),'exec'),m.__dict__)
  self.assertTrue(m.client_allowed('vps','127.0.0.1'));self.assertFalse(m.client_allowed('vps','192.168.0.5'))
  self.assertTrue(m.client_allowed('tailscale','100.77.1.18'));self.assertFalse(m.client_allowed('tailscale','127.0.0.1'))
  self.assertFalse(m.client_allowed('tailscale',''))
if __name__=='__main__':unittest.main()
