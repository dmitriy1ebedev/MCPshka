import importlib.util
import json
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('cloud_results', Path(__file__).with_name('cloud-results.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)
SHA = 'a' * 40
PLAN = {'tier': 'RELEASE', 'head': SHA, 'dirty': False,
        'steps': [{'argv': ['go', 'test', './...']}, {'argv': ['go', 'vet', './...']}]}


def report(*items):
    return ('\n'.join(json.dumps(item) for item in items) + '\n').encode()


class Results(unittest.TestCase):
    def test_complete_report(self):
        result = m.summary(report(PLAN, {'argv': ['go', 'test', './...'], 'exit': 0, 'seconds': 3},
                                  {'argv': ['go', 'vet', './...'], 'exit': 0, 'seconds': 1}), SHA)
        self.assertEqual(result['state'], 'passed')
        self.assertEqual(result['seconds'], 4)
        self.assertFalse(result['deploy_authorized'])

    def test_incomplete_report_never_passes(self):
        self.assertEqual(m.summary(report(PLAN), SHA)['state'], 'unknown')

    def test_failed_test_is_retained(self):
        result = m.summary(report(PLAN, {'Action': 'fail', 'Package': 'mcpshka/internal/jobs', 'Test': 'TestCancel'}), SHA)
        self.assertEqual(result['state'], 'failed')
        self.assertEqual(result['failed_tests'], ['mcpshka/internal/jobs:TestCancel'])

    def test_stale_sha_and_dirty_checkout_rejected(self):
        for plan in (dict(PLAN, head='b' * 40), dict(PLAN, dirty=True)):
            with self.assertRaises(ValueError): m.summary(report(plan), SHA)

    def test_changed_check_order_never_passes(self):
        self.assertEqual(m.summary(report(PLAN, {'argv': ['go', 'vet', './...'], 'exit': 0},
                                          {'argv': ['go', 'test', './...'], 'exit': 0}), SHA)['state'], 'unknown')

    def test_command_output_is_not_exposed(self):
        result = m.summary(report(PLAN, {'Output': 'token=secret-value', 'Action': 'output'}), SHA)
        self.assertNotIn('secret-value', json.dumps(result))

    def test_multiple_plans_and_bad_exit_rejected(self):
        for raw in (report(PLAN, PLAN), report(PLAN, {'argv': [], 'exit': True})):
            with self.assertRaises(ValueError): m.summary(raw, SHA)

    def test_invalid_check_commands_rejected(self):
        for argv in (None, [], 'go test', [123], ['']):
            with self.subTest(argv=argv), self.assertRaises(ValueError):
                plan = dict(PLAN, steps=[{'argv': argv}])
                m.summary(report(plan, {'argv': argv, 'exit': 0}), SHA)

    def test_missing_identity_rejected(self):
        with self.assertRaises(ValueError): m.summary(b'garbage\n', SHA)


if __name__ == '__main__': unittest.main()
