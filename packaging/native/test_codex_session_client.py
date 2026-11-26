"""Offline session creation: no daemon, models, credentials or production writes."""
import importlib.util
from pathlib import Path
import unittest
import os
import stat

SOURCE = Path(__file__).parents[2] / 'internal/rootbroker/codex_session_client.py'


class SessionClientTests(unittest.TestCase):
    def setUp(self):
        self.assertTrue(SOURCE.exists(), 'native session-create client is absent')
        spec = importlib.util.spec_from_file_location('session_client', SOURCE)
        self.client = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.client)

    def test_peer_matches_selected_managed_image_without_executing_it(self):
        def image(mode=stat.S_IFREG | 0o755, inode=12):
            return os.stat_result((mode, inode, 10, 1, 1001, 1001, 1, 0, 0, 0))
        self.assertTrue(self.client.peer_matches(0, image(), image()))
        for uid, selected, peer in [(1001, image(), image()), (0, image(), image(inode=13)),
                                     (0, image(mode=stat.S_IFREG | 0o777), image()),
                                     (0, image(mode=stat.S_IFREG | 0o644), image()),
                                     (0, image(mode=stat.S_IFIFO | 0o755), image())]:
            self.assertFalse(self.client.peer_matches(uid, selected, peer))

    def test_new_session_disables_inherited_external_writers(self):
        calls = []
        class RPC:
            def call(_, method, args):
                calls.append((method, args))
                if method == 'config/read':
                    return {'config': {'mcp_servers': {'gitverse': {}},
                                       'plugins': {'plugin@market': {}},
                                       'apps': {'_default': None, 'app-id': {}}}}
                if method == 'thread/start':
                    return {'thread': {'id': 'thread_12345678', 'sessionId': 'thread_12345678',
                                       'cwd': '/root/repos/mcpshka', 'status': {'type': 'idle'}},
                            'cwd': '/root/repos/mcpshka', 'approvalPolicy': 'never',
                            'sandbox': {'type': 'readOnly'}}
                raise AssertionError(method)
        result = self.client.create(RPC())
        self.assertEqual(result['thread_id'], 'thread_12345678')
        method, args = calls[-1]
        self.assertEqual(method, 'thread/start')
        self.assertEqual(args['sandbox'], 'read-only')
        self.assertEqual(args['approvalPolicy'], 'never')
        for key in ['mcp_servers."gitverse".enabled', 'plugins."plugin@market".enabled',
                    'apps._default.enabled', 'apps."app-id".enabled']:
            self.assertIs(args['config'][key], False)
        self.assertFalse(any(method in ('turn/start', 'thread/queue/add') for method, _ in calls))

    def test_unknown_or_oversized_config_never_creates(self):
        for config in ({'mcp_servers': []}, {'plugins': []},
                       {'mcp_servers': {str(i): {} for i in range(129)}}):
            class RPC:
                def call(_, method, args):
                    self.assertEqual(method, 'config/read')
                    return {'config': config}
            with self.assertRaises(ValueError):
                self.client.create(RPC())

    def test_starting_missing_catalog_or_enabled_tools_never_prove_isolation(self):
        for status, tools, cursor, present in [
                ('disabled', {}, None, True), ('starting', {}, None, True),
                ('connected', {}, None, True), ('disabled', {'writer': {}}, None, True),
                ('disabled', {}, 'more', True), ('disabled', {}, None, False)]:
            class RPC:
                def call(_, method, args):
                    if method == 'thread/read':
                        return {'thread': {'id': 'thread_12345678', 'cwd': '/root/repos/mcpshka',
                                           'status': {'type': 'idle'}}}
                    self.assertEqual(method, 'mcpServerStatus/list')
                    self.assertEqual(args['threadId'], 'thread_12345678')
                    return {'data': [{'name': 'gitverse', 'runtimeStatus': status, 'tools': tools,
                                      'resources': [], 'resourceTemplates': []}] if present else [],
                            'nextCursor': cursor}
            if status == 'disabled' and not tools and not cursor and present:
                self.assertTrue(self.client.probe(RPC(), 'thread_12345678', ['gitverse'])['isolated'])
            else:
                with self.assertRaises(ValueError):
                    self.client.probe(RPC(), 'thread_12345678', ['gitverse'])

    def test_load_never_creates_or_resumes_busy_or_foreign_session(self):
        for state, identity in [('active', 'thread_12345678'), ('idle', 'thread_other123')]:
            class RPC:
                def call(_, method, args):
                    self.assertEqual(method, 'thread/read')
                    return {'thread': {'id': identity, 'cwd': '/root/repos/mcpshka', 'status': {'type': state}}}
            with self.assertRaises(ValueError):
                self.client.load(RPC(), 'thread_12345678')

    def test_incomplete_session_metadata_preserves_returned_thread_id(self):
        for session_id in (None, '', 'malformed value'):
            out = self.client.metadata({'thread': {'id': 'thread_12345678', 'sessionId': session_id},
                                        'cwd': '/root/repos/mcpshka', 'approvalPolicy': 'never',
                                        'sandbox': {'type': 'readOnly'}})
            self.assertEqual(out['thread_id'], 'thread_12345678')
            self.assertEqual(out['codex_session_id'], '')
            self.assertFalse(out['policy_verified'])

    def test_malformed_policy_keeps_valid_identity_quarantined(self):
        for sandbox in (None, [], 'readOnly', {'type': 'dangerFullAccess'},
                        {'type': 'readOnly', 'networkAccess': True}):
            out = self.client.metadata({'thread': {'id': 'thread_12345678', 'sessionId': 'thread_12345678',
                                                   'cwd': '/root/repos/mcpshka'},
                                        'cwd': '/root/repos/mcpshka', 'approvalPolicy': 'never',
                                        'sandbox': sandbox})
            self.assertEqual(out['thread_id'], 'thread_12345678')
            self.assertFalse(out['policy_verified'])

    def test_load_reapplies_readonly_without_global_config_write(self):
        calls = []
        class RPC:
            def call(_, method, args):
                calls.append((method, args))
                if method == 'thread/read':
                    return {'thread': {'id': 'thread_12345678', 'cwd': '/root/repos/mcpshka',
                                       'status': {'type': 'notLoaded'}}}
                if method == 'config/read':
                    return {'config': {'mcp_servers': {'gitverse': {}}, 'plugins': {}, 'apps': {}}}
                self.assertEqual(method, 'thread/resume')
                return {'thread': {'id': 'thread_12345678', 'sessionId': 'thread_12345678',
                                   'cwd': '/root/repos/mcpshka'},
                        'cwd': '/root/repos/mcpshka', 'approvalPolicy': 'never', 'sandbox': {'type': 'readOnly'}}
        self.assertTrue(self.client.load(RPC(), 'thread_12345678')['policy_verified'])
        args = calls[-1][1]
        self.assertEqual(args['threadId'], 'thread_12345678')
        self.assertEqual(args['sandbox'], 'read-only')
        self.assertIs(args['config']['mcp_servers."gitverse".enabled'], False)
        self.assertFalse(any(method.startswith(('turn/', 'config/value', 'config/batch')) for method, _ in calls))


if __name__ == '__main__':
    unittest.main()
