"""Offline regression checks for the packaged daemon bridge."""
import importlib.util
from pathlib import Path
import py_compile
import tempfile
import unittest

SOURCE = Path(__file__).with_name('codex-bridge.py')
spec = importlib.util.spec_from_file_location('daemon_bridge', SOURCE)
bridge = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bridge)

class BridgeTests(unittest.TestCase):
    def test_py_compile(self):
        with tempfile.TemporaryDirectory() as directory:
            py_compile.compile(str(SOURCE), cfile=directory+'/bridge.pyc', doraise=True)

    def test_null_text_and_unfinished_command(self):
        self.assertEqual(bridge.redact(None), '')
        self.assertEqual(bridge.summary({'id':'session_12345','name':None,'preview':None})['name'], '')
        class RPC:
            def call(self, method, params):
                if method == 'thread/read':
                    return {'thread':{'id':'session_12345'}}
                return {'data':[{'items':[
                    {'type':'userMessage','content':[{'type':'text','text':None}]},
                    {'type':'agentMessage','text':None},
                    {'type':'commandExecution','status':None,'exitCode':None,'command':None,'aggregatedOutput':None},
                    {'type':'commandExecution','status':'completed','exitCode':0,'command':'go test','aggregatedOutput':''},
                ]}]}
        result = bridge.session_read(RPC(), {'session_id':'session_12345'})
        texts = [item['text'] for item in result['turns'][0]['items']]
        self.assertEqual(texts[:2], ['', ''])
        self.assertNotIn('None', '\n'.join(texts))
        self.assertIn('exit=0', texts[3])
