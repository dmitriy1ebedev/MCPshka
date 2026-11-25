import importlib.util,pathlib,unittest
spec=importlib.util.spec_from_file_location('bridge',pathlib.Path(__file__).parents[1]/'native/codex-bridge.py');bridge=importlib.util.module_from_spec(spec);spec.loader.exec_module(bridge)
class RPC:
 def __init__(self,status='idle'):self.calls=[];self.status=status
 def call(self,method,params):
  self.calls.append((method,params))
  if method=='thread/read':return {'thread':{'id':'session_123','status':{'type':self.status},'turns':[]}}
  if method=='turn/start':return {'turn':{'id':'turn_123','status':'inProgress'}}
  if method=='thread/queue/add':return {'queuedSubmission':{'id':'queue_123'}}
  if method=='thread/turns/list':return {'data':[{'id':'turn_123','status':'completed','items':[{'type':'agentMessage','text':'Bearer abcdefghijklmnop and sk-abcdefghijklmnopqrst'}]}],'nextCursor':None}
  raise AssertionError(method)
class BridgeTests(unittest.TestCase):
 def test_submission_uses_existing_idle_session_and_read_only(self):
  r=RPC();out=bridge.task_submit(r,{'session_id':'session_123','message':'inspect','request_id':'a'*32,'mode':'start'})
  self.assertEqual(out['mode'],'read-only');self.assertEqual(r.calls[-1][1]['sandboxPolicy'],{'type':'readOnly'})
  self.assertFalse(any(method in ('thread/start','thread/resume') for method,_ in r.calls))
 def test_busy_and_unloaded_sessions_never_start(self):
  for status in ('active','notLoaded'):
   r=RPC(status)
   with self.assertRaises(ValueError):bridge.task_submit(r,{'session_id':'session_123','message':'inspect','request_id':'a'*32,'mode':'start'})
   self.assertEqual(len(r.calls),1)
 def test_history_is_bounded_redacted_and_untrusted(self):
  out=bridge.session_read(RPC(),{'session_id':'session_123','output_limit_bytes':1024})
  self.assertEqual(out['trust'],'UNTRUSTED');text=out['turns'][0]['items'][0]['text']
  self.assertNotIn('abcdefghijklmnop',text);self.assertIn('REDACTED',text)
 def test_unknown_fields_do_not_reach_rpc(self):
  r=RPC()
  with self.assertRaises(ValueError):bridge.task_submit(r,{'session_id':'session_123','message':'x','request_id':'a'*32,'shell':'rm'})
  self.assertEqual(r.calls,[])
if __name__=='__main__':unittest.main()
