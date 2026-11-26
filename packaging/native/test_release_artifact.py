import importlib.util,io,json,struct,tempfile,unittest,zipfile
from pathlib import Path
spec=importlib.util.spec_from_file_location('release_artifact',Path(__file__).with_name('release-artifact.py'))
module=importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
class ReleaseArtifactTests(unittest.TestCase):
 def archive(self, extra=None, binary=None):
  binary=binary or b'\x7fELF'+bytes([2,1])+b'\0'*12+struct.pack('<H',183)+b'\0'*64
  entries={'checks.log':b'PASS','mcpshka-linux-arm64':binary,'release-arm64-buildinfo.txt':b'GOARCH=arm64\nCGO_ENABLED=1\n','release-arm64-sha256.txt':(module.sha(binary)+'  /home/runner/work/_temp/mcpshka-results/mcpshka-linux-arm64\n').encode(),'source-sha.txt':b'a'*40+b'\n','source-tree.txt':b'd'*40+b'\n','source-identity.json':json.dumps({'schema':'mcpshka.source-identity/v1','source_digest':'b'*64,'file_count':1,'total_bytes':10}).encode(),'toolchain.txt':b'go'}
  entries.update(extra or {})
  raw=io.BytesIO()
  with zipfile.ZipFile(raw,'w',zipfile.ZIP_DEFLATED) as z:
   for key,value in entries.items(): z.writestr(key,value)
  return raw.getvalue()
 def test_verified_archive_and_identity(self):
  raw=self.archive()
  self.assertTrue(module.unpack(raw,'a'*40,'b'*64,'d'*40)[0].startswith(b'\x7fELF'))
  with self.assertRaises(ValueError): module.unpack(raw,'a'*40,'b'*64,'e'*40)
  for source,digest in [('c'*40,'b'*64),('a'*40,'c'*64)]:
   with self.assertRaises(ValueError): module.unpack(raw,source,digest,'d'*40)
 def test_archive_rejects_extra_duplicate_and_wrong_arch(self):
  for raw in [self.archive({'../escape':b'x'}),self.archive(binary=b'not-an-elf')]:
   with self.assertRaises(ValueError): module.unpack(raw,'a'*40,'b'*64,'d'*40)
  raw=io.BytesIO(self.archive())
  with zipfile.ZipFile(raw,'a') as z: z.writestr('checks.log',b'override')
  with self.assertRaises(ValueError): module.unpack(raw.getvalue(),'a'*40,'b'*64,'d'*40)
 def test_paired_broker_from_same_tree(self):
  controller=b'\x7fELF'+bytes([2,1])+b'\0'*12+struct.pack('<H',183)+b'\0'*64
  broker=controller+b'broker'
  entries={
   'mcpshka-broker-linux-arm64':broker,
   'broker-arm64-sha256.txt':(module.sha(broker)+'  mcpshka-broker-linux-arm64\n').encode(),
   'broker-arm64-buildinfo.txt':b'GOARCH=arm64\nCGO_ENABLED=1\n'
  }
  unpacked=module.unpack(self.archive(entries),'a'*40,'b'*64,'d'*40)
  self.assertEqual(unpacked[2],broker)
  self.assertIsNone(module.unpack(self.archive(),'a'*40,'b'*64,'d'*40)[2])
  entries['broker-arm64-sha256.txt']=b'0'*64+b'  mcpshka-broker-linux-arm64\n'
  with self.assertRaises(ValueError):
   module.unpack(self.archive(entries),'a'*40,'b'*64,'d'*40)
 def test_network_boundary(self):
  module.validate_url('https://files.oaiusercontent.com/file?sig=private')
  for url in ['http://files.oaiusercontent.com/f','https://127.0.0.1/f','https://evil.example/f','https://u@files.oaiusercontent.com/f','https://files.oaiusercontent.com:444/f','https://files.oaiusercontent.com/f#x']:
   with self.assertRaises(ValueError): module.validate_url(url)
 def test_write_never_overwrites_existing_files(self):
  with tempfile.TemporaryDirectory() as d:
   p=Path(d)/'candidate'
   module.write_new(p,b'good')
   with self.assertRaises(FileExistsError): module.write_new(p,b'bad')
   self.assertEqual(p.read_bytes(),b'good')
 def test_resume_only_identical_owned_regular_files(self):
  with tempfile.TemporaryDirectory() as d:
   p=Path(d)/'candidate'
   module.write_verified(p,b'good')
   module.write_verified(p,b'good')
   with self.assertRaises(ValueError): module.write_verified(p,b'bad')
   p.unlink()
   p.symlink_to(Path(d)/'other')
   with self.assertRaises(OSError): module.write_verified(p,b'good')
if __name__=='__main__': unittest.main()
