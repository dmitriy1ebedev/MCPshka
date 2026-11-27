import json
import pathlib
import subprocess
import sys
import unittest
import yaml

ROOT=pathlib.Path(__file__).resolve().parent/'project-ci'

class ProjectDistributedCI(unittest.TestCase):
 def test_workflows(self):
  subprocess.run(['sh','-n',str(ROOT/'.ci/distributed.sh')],check=True)
  shell=(ROOT/'.ci/distributed.sh').read_text()
  self.assertIn('mcpshka-revision',shell)
  self.assertIn('"${#revision}" -eq 40',shell)
  self.assertIn('go build -p 1',shell)
  self.assertIn('as_ci "$worker_dir/ci-shard"',shell)
  self.assertIn('go1.26.0',shell)
  for site in ('github','gitverse'):
   data=yaml.safe_load((ROOT/f'.{site}/workflows/distributed.yml').read_text())
   trigger=data.get('on',data.get(True))
   self.assertEqual(set(trigger),{'workflow_dispatch'})
   self.assertEqual(set(trigger['workflow_dispatch']['inputs']),{'shard','source_commit','source_tree','snapshot','plan_hash'})
   step=data['jobs']['shard']['steps'][1]
   self.assertIn(site+'.run_id',step['env']['CI_RUN_ID'])
   self.assertIn('sh .ci/distributed.sh '+site,step['run'])
   self.assertEqual(data['jobs']['shard']['steps'][2]['with']['name'],'project-build')
  data=yaml.safe_load((ROOT/'.gitlab-ci.yml').read_text())
  for n in range(4):
   job=data[f'distributed-shard-{n}']
   self.assertIn(f'$CI_SHARD_SHARD == "{n}"',job['rules'][0]['if'])
  data=yaml.safe_load((ROOT/'.sourcecraft/ci.yaml').read_text())
  cubes=data['tasks'][0]['cubes']
  self.assertEqual(cubes[1]['needs'],['identity'])
  self.assertIn('out/distributed/',cubes[1]['artifacts']['paths'])

if __name__=='__main__':
 if len(sys.argv)>1:
  ROOT=pathlib.Path(sys.argv.pop(1))
 unittest.main()
