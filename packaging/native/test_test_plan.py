"""Contracts for dependency selection and immutable release gates."""
import importlib.util
from pathlib import Path
import unittest
from unittest import mock
import os
import subprocess
import sys
import tempfile
import time

class TestSelection(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        spec=importlib.util.spec_from_file_location('test_plan',Path(__file__).with_name('test-plan.py'))
        cls.plan=importlib.util.module_from_spec(spec);spec.loader.exec_module(cls.plan)

    def graph(self):
        return {'internal/state':[], 'internal/queue':['internal/state'],
                'internal/server':['internal/queue'], 'internal/unrelated':[]}

    def test_transitive_consumers_are_selected(self):
        packages,reasons=self.plan.select(['internal/state/store.go'],self.graph())
        self.assertEqual(packages,['internal/queue','internal/server','internal/state'])
        self.assertIn('dependency:internal/state',reasons['internal/queue'])
        self.assertIn('dependency:internal/queue',reasons['internal/server'])

    def test_changed_test_selects_package_and_consumers(self):
        packages,_=self.plan.select(['internal/queue/queue_test.go'],self.graph())
        self.assertEqual(packages,['internal/queue','internal/server'])

    def test_toolchain_or_planner_change_selects_every_package(self):
        for path in ('go.mod','go.sum','packaging/native/test-plan.py',
                     '.github/workflows/cloud.yml','.gitverse/workflows/cloud.yml'):
            packages,_=self.plan.select([path],self.graph())
            self.assertEqual(packages,sorted(self.graph()))

    def test_workflow_changes_include_native_integration_contracts(self):
        for path in ('.github/workflows/cloud.yml', '.gitverse/workflows/cloud.yml'):
            packages,reasons=self.plan.select([path],self.graph())
            steps=self.plan.commands('INTEGRATION',packages,reasons,[path])
            self.assertTrue(any('test_*.py' in step['argv'] for step in steps))

    def test_removed_package_still_selects_existing_consumer(self):
        packages,_=self.plan.select(['internal/deleted/old.go'],{'internal/server':['internal/deleted']})
        self.assertEqual(packages,['internal/server'])

    def test_docs_do_not_start_go_tests(self):
        self.assertEqual(self.plan.select(['docs/example.md'],self.graph()),([],{}))

    def test_unknown_go_change_cannot_silently_skip_tests(self):
        packages,reasons=self.plan.select(['platform/new.go'],self.graph())
        self.assertEqual(packages,sorted(self.graph()))
        self.assertTrue(all('unmapped_go_change:conservative_full_suite' in r for r in reasons.values()))

    def test_packaging_contracts_select_go_checks(self):
        for path in ('packaging/systemd/mcpshka-broker.service','packaging/native/install-broker.sh'):
            packages,_=self.plan.select([path],self.graph())
            self.assertEqual(packages,sorted(self.graph()))

    def test_completed_leader_cannot_leave_running_descendants(self):
        with tempfile.TemporaryDirectory() as directory:
            receipt=Path(directory)/'child'
            code="""import os,signal,time,pathlib
receipt=pathlib.Path(%r)
child=os.fork()
if child==0:
 signal.signal(signal.SIGTERM,signal.SIG_IGN)
 receipt.write_text(str(os.getpid()))
 time.sleep(60)
else:
 while not receipt.exists():time.sleep(0.01)
""" % str(receipt)
            leader=subprocess.Popen([sys.executable,'-I','-c',code],start_new_session=True)
            self.assertEqual(self.plan.wait_bounded(leader,5),0)
            child=int(receipt.read_text())
            deadline=time.monotonic()+1
            while True:
                try:status=Path('/proc')/str(child)/'stat';state=status.read_text().split(') ',1)[1][0]
                except FileNotFoundError:state='gone'
                if state in ('Z','gone','X') or time.monotonic()>=deadline:break
                time.sleep(0.01)
            self.assertIn(state,('Z','gone','X'))

    def test_timeout_stops_the_command(self):
        process=subprocess.Popen([sys.executable,'-I','-c','import time; time.sleep(60)'],start_new_session=True)
        with self.assertRaises(subprocess.TimeoutExpired):self.plan.wait_bounded(process,0.1)
        self.assertIsNotNone(process.poll())

    def test_host_budget_moves_expensive_go_compile_to_cloud(self):
        with mock.patch('platform.machine',return_value='aarch64'):
            with mock.patch('builtins.open',mock.mock_open(read_data='MemTotal:       3853884 kB\\n')):
                self.assertTrue(self.plan.constrained_arm_host())
            with mock.patch('builtins.open',mock.mock_open(read_data='MemTotal:       8388608 kB\\n')):
                self.assertFalse(self.plan.constrained_arm_host())
        with mock.patch('platform.machine',return_value='x86_64'):
            self.assertFalse(self.plan.constrained_arm_host())

    def test_release_always_has_full_uncached_gate(self):
        plan=self.plan.commands('RELEASE',[],{},[])
        self.assertIn(['go','test','-count=1','-p','1','-json','./...'],[s['argv'] for s in plan])
        self.assertIn(['go','vet','-p','1','./...'],[s['argv'] for s in plan])
        self.assertTrue(any('test_*.py' in s['argv'] for s in plan))
        self.assertFalse(any('-race' in s['argv'] for s in plan), 'race has its own mandatory cloud job')

if __name__=='__main__':unittest.main()
