import pathlib
import unittest
import yaml
ROOT=pathlib.Path(__file__).resolve().parents[2]
class DistributedWorkflows(unittest.TestCase):
 def test_dispatch_only_and_pinned_inputs(self):
  for site in ("github","gitverse"):
   data=yaml.safe_load((ROOT/f".{site}/workflows/distributed.yml").read_text())
   trigger=data.get("on",data.get(True))
   self.assertEqual(set(trigger),{"workflow_dispatch"})
   fields=trigger["workflow_dispatch"]["inputs"]
   self.assertEqual(set(fields),{"shard","source_commit","source_tree","snapshot","plan_hash"})
   job=data["jobs"]["shard"]
   self.assertIn("inputs.shard",job["name"])
   self.assertIn("distributed_shard.sh",job["steps"][-1]["run"])
   self.assertFalse(data["concurrency"]["cancel-in-progress"])
 def test_moshub_runs_exactly_one_shard_and_preserves_runner_rules(self):
  data=yaml.safe_load((ROOT/".gitlab-ci.yml").read_text())
  for n in range(4):
   job=data[f"distributed-shard-{n}"]
   self.assertEqual(job["extends"],".distributed")
   self.assertIn(f'$CI_SHARD_SHARD == "{n}"',job["rules"][0]["if"])
   self.assertIn('$MCPSHKA_DISTRIBUTED == "1"',job["rules"][0]["if"])
  self.assertIn("MCPSHKA_RUNNER_TOKEN",data["workflow"]["rules"][0]["if"])
 def test_sourcecraft_shard_report(self):
  data=yaml.safe_load((ROOT/".sourcecraft/ci.yaml").read_text())
  self.assertIn("distributed",data["workflows"])
  task=next(t for t in data["tasks"] if t["name"]=="distributed-shard")
  self.assertIn("report.json",task["cubes"][0]["artifacts"]["paths"])
  self.assertTrue(any("ci-shard sourcecraft" in s for c in task["cubes"] for s in c["script"]))
 def test_offline_image_has_workflow_dependencies(self):
  for rel in ("packaging/moshub/ci-image/Dockerfile","packaging/moshub/ci-image/assemble.sh"):
   text=(ROOT/rel).read_text()
   for package in ("python3-yaml","sqlite3","tini"):
    self.assertIn(package,text)
 def test_cloud_installs_yaml_for_runner_contracts(self):
  self.assertIn("PyYAML==",(ROOT/"packaging/native/requirements-cloud.txt").read_text())
  self.assertIn("-r packaging/native/requirements-cloud.txt",(ROOT/".github/workflows/cloud.yml").read_text())
if __name__=="__main__":unittest.main()
