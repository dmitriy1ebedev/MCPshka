#!/usr/bin/env python3
"""Offline guard tests; no Codex daemon or real model turns."""
import importlib.util
import pathlib
import unittest

SCRIPT = pathlib.Path(__file__).with_name("codex-bridge.py")
SPEC = importlib.util.spec_from_file_location("mcpshka_codex_bridge", SCRIPT)
BRIDGE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(BRIDGE)


class FakeRPC:
    def __init__(self, loaded="idle"):
        self.loaded = loaded
        self.calls = []

    def call(self, name, params):
        self.calls.append((name, params))
        if name == "model/list":
            return {"data": [{
                "model": "sample-model",
                "supportedReasoningEfforts": [{"reasoningEffort":"low"},{"reasoningEffort":"high"}],
            }]}
        if name == "thread/read":
            return {"thread": {"status":{"type":self.loaded}}}
        if name == "thread/queue/add":
            return {"queuedSubmission":{"id":"queue_abcdefgh"}}
        if name == "turn/start":
            return {"turn":{"id":"turn_abcdefgh","status":"inProgress"}}
        raise AssertionError("unexpected RPC "+name)


BASE = {
    "session_id":"session_12345",
    "message":"read only",
    "request_id":"a"*32,
}

class BridgePolicyTests(unittest.TestCase):
    def status(self, queued=None, turns=None, flags=None, cursor=None, **extra):
        class StatusRPC:
            def call(self,name,params):
                if name=='thread/read':return {'thread':{'id':BASE['session_id'],'status':{'type':'active','activeFlags':flags or []}}}
                if name=='thread/queue/list':return {'data':queued or [],'nextCursor':cursor}
                if name=='thread/turns/list':return {'data':turns or [],'nextCursor':None}
                raise AssertionError(name)
        return BRIDGE.task_status(StatusRPC(),dict({'session_id':BASE['session_id'],'request_id':BASE['request_id']},**extra))

    def turn(self,status='inProgress',client=None):
        return {'id':'turn_abcdefgh','status':status,'items':[{'type':'userMessage','clientId':client or BASE['request_id']}]}

    def test_request_identity_finds_queued_task_after_lost_reply(self):
        out=self.status(queued=[{'id':'queue_abcdefgh','clientUserMessageId':BASE['request_id']}])
        self.assertEqual(out['state'],'queued')
        self.assertEqual(out['queue_id'],'queue_abcdefgh')

    def test_turn_probe_uses_bounded_summary_items(self):
        seen=[]
        class SummaryRPC:
            def call(self,name,params):
                if name=='thread/read':return {'thread':{'id':BASE['session_id'],'status':{'type':'notLoaded'}}}
                if name=='thread/queue/list':return {'data':[],'nextCursor':None}
                if name=='thread/turns/list':
                    seen.append(params.get('itemsView'))
                    return {'data':[{'id':'turn_abcdefgh','status':'completed','items':[{'type':'userMessage','clientId':BASE['request_id']}]}],'nextCursor':None}
                raise AssertionError(name)
        out=BRIDGE.task_status(SummaryRPC(),{'session_id':BASE['session_id'],'request_id':BASE['request_id']})
        self.assertEqual(seen,['summary'])
        self.assertEqual(out['state'],'completed')

    def test_failure_reports_only_exception_class(self):
        self.assertEqual(BRIDGE.failure_class(ValueError('secret /root/path')),'ValueError')

    def test_queue_absence_does_not_prove_completion(self):
        self.assertEqual(self.status()['state'],'unknown')
        self.assertIsNone(self.status(cursor='more')['queued'])
        self.assertIsNone(self.status(queue_cursor='last-page')['queued'])

    def test_exact_client_identity_recovers_turn_lifecycle(self):
        for status,state in (('inProgress','running'),('completed','completed'),('failed','failed'),('interrupted','blocked')):
            out=self.status(turns=[self.turn(status)])
            self.assertEqual(out['state'],state)
            self.assertEqual(out['turn_id'],'turn_abcdefgh')
        self.assertEqual(self.status(turns=[self.turn('completed','b'*32)])['state'],'unknown')

    def test_approval_wait_is_bound_to_active_task(self):
        self.assertEqual(self.status(turns=[self.turn()],flags=['waitingOnApproval'])['state'],'blocked')
        self.assertEqual(self.status(turns=[self.turn('completed')],flags=['waitingOnApproval'])['state'],'completed')

    def test_newest_linked_continuation_prevents_false_completion(self):
        continuation={'id':'turn_continued','rootTurnId':'turn_abcdefgh','status':'inProgress','items':[]}
        self.assertEqual(self.status(turns=[continuation,self.turn('completed')])['state'],'running')
        self.assertEqual(self.status(turns=[continuation,self.turn('completed')],turn_id='turn_continued')['state'],'running')
        self.assertEqual(self.status(turns=[continuation,self.turn('completed')],turn_id='turn_foreign_123')['state'],'unknown')

    def test_old_cursor_rechecks_latest_continuation(self):
        initial=self.turn('completed')
        latest={'id':'turn_continued','rootTurnId':initial['id'],'status':'inProgress','items':[]}
        class HistoryRPC:
            def call(self,name,params):
                if name=='thread/read':return {'thread':{'id':BASE['session_id'],'status':{'type':'active','activeFlags':[]}}}
                if name=='thread/queue/list':return {'data':[]}
                if name=='thread/turns/list':return {'data':[initial] if params.get('cursor') else [latest]}
                raise AssertionError(name)
        out=BRIDGE.task_status(HistoryRPC(),{'session_id':BASE['session_id'],'request_id':BASE['request_id'],'turn_cursor':'old'})
        self.assertEqual(out['state'],'running')

    def read(self, items):
        class ReadRPC:
            def call(self, name, params):
                if name=='thread/read':
                    return {'thread':{'id':BASE['session_id'],'status':{'type':'active'}}}
                if name=='thread/turns/list':
                    return {'data':[{'id':'turn_latest','status':'inProgress','items':items}]}
                raise AssertionError(name)
        return BRIDGE.session_read(ReadRPC(),{'session_id':BASE['session_id'],'output_limit_bytes':1024})

    def test_latest_activity_survives_long_initial_prompt(self):
        out=self.read([{'type':'userMessage','content':[{'type':'text','text':'initial '*1000}]},
                       {'type':'agentMessage','text':'latest proven checkpoint'}])
        self.assertTrue(any('latest proven checkpoint' in i['text'] for i in out['turns'][0]['items']))
        self.assertTrue(out['truncated'])

    def test_running_command_has_redacted_identity_and_status(self):
        out=self.read([{'type':'commandExecution','command':'go test ./... api_key=private-fixture',
                       'status':'inProgress','exitCode':None,'aggregatedOutput':''}])
        text=out['turns'][0]['items'][0]['text']
        self.assertIn('go test',text)
        self.assertIn('inProgress',text)
        self.assertNotIn('private-fixture',text)

    def test_command_credentials_remain_private(self):
        for command in ('curl --user owner:private-fixture', 'tool --password private-fixture',
                        'tool token=private-fixture', 'curl -u "owner:private-fixture"',
                        'go test private-fixture', 'PRIVATE_DATA=private-fixture python3 script.py'):
            out=self.read([{'type':'commandExecution','command':command,'status':'inProgress'}])
            self.assertNotIn('private-fixture',out['turns'][0]['items'][0]['text'])

    def test_existing_owner_queue_still_works(self):
        rpc=FakeRPC()
        out=BRIDGE.task_submit(rpc,dict(BASE,mode="queue"))
        self.assertEqual(out["state"],"queued")
        self.assertEqual([x[0] for x in rpc.calls],["thread/read","thread/queue/add"])

    def test_selected_model_not_silently_discarded(self):
        rpc=FakeRPC()
        with self.assertRaisesRegex(ValueError,"override is not enabled"):
            BRIDGE.task_submit(rpc,dict(BASE,mode="start",model="sample-model",reasoning_effort="high"))
        self.assertEqual([x[0] for x in rpc.calls],["model/list"])
        self.assertFalse(any(name=="turn/start" for name,_ in rpc.calls))

    def test_unsupported_effort_fails_before_turn(self):
        rpc=FakeRPC()
        with self.assertRaisesRegex(ValueError,"effort unsupported"):
            BRIDGE.task_submit(rpc,dict(BASE,mode="start",model="sample-model",reasoning_effort="xhigh"))
        self.assertFalse(any(name=="turn/start" for name,_ in rpc.calls))

    def test_queue_cannot_accept_overrides(self):
        rpc=FakeRPC()
        with self.assertRaises(ValueError):
            BRIDGE.task_submit(rpc,dict(BASE,mode="queue",model="sample-model"))
        self.assertEqual(rpc.calls,[])

    def test_existing_idle_start_remains_read_only(self):
        rpc=FakeRPC()
        out=BRIDGE.task_submit(rpc,dict(BASE,mode="start"))
        self.assertEqual(out["mode"],"read-only")
        self.assertEqual([name for name,_ in rpc.calls],["thread/read","turn/start"])
        self.assertEqual(rpc.calls[-1][1]["sandboxPolicy"],{"type":"readOnly"})

    def test_existing_active_start_rejected(self):
        rpc=FakeRPC(loaded="active")
        with self.assertRaisesRegex(ValueError,"busy"):
            BRIDGE.task_submit(rpc,dict(BASE,mode="start"))
        self.assertEqual([name for name,_ in rpc.calls],["thread/read"])

class ListDaemon:
    """Mimics app-server thread/list: without sourceKinds only interactive sessions."""
    def __init__(self, threads):
        self.threads = threads
        self.calls = []

    def call(self, name, params):
        self.calls.append((name, params))
        if name != "thread/list":
            raise AssertionError("unexpected RPC "+name)
        kinds = params.get("sourceKinds") or ["cli", "vscode"]
        data = [t for t in self.threads if t["source"] in kinds]
        if "cwd" in params:
            data = [t for t in data if t["cwd"] == params["cwd"]]
        return {"data": data[:params["limit"]], "nextCursor": None}


def listed(i, source, cwd="/root/repos/mcpshka-codex-197"):
    return {"id":"01a178ca-69a5-7042-aec2-%012d" % i,"source":source,"cwd":cwd,
            "preview":"Роль: ревьюер token=abc123secret","updatedAt":i,"status":{"type":"notLoaded"}}


class SessionsListTests(unittest.TestCase):
    def test_agent_task_exec_sessions_are_listed(self):
        # agent_task runs `codex exec`; its sessions were hidden and the list was empty.
        rpc = ListDaemon([listed(1,"exec"),listed(2,"exec"),listed(3,"cli")])
        out = BRIDGE.sessions_list(rpc, {"limit":10})
        self.assertEqual([s["origin"] for s in out["sessions"]], ["exec","exec","cli"])
        self.assertTrue({"cli","vscode","exec","appServer"} <= set(rpc.calls[0][1]["sourceKinds"]))

    def test_cwd_filter_and_page_bounds(self):
        rpc = ListDaemon([listed(i,"exec") for i in range(40)]+[listed(99,"exec","/root/repos/other")])
        out = BRIDGE.sessions_list(rpc, {"limit":30,"cwd":"/root/repos/other"})
        self.assertEqual([s["session_id"][-2:] for s in out["sessions"]], ["99"])
        self.assertEqual(len(BRIDGE.sessions_list(rpc, {"limit":30})["sessions"]), 30)
        for bad in ({"limit":0},{"limit":31},{"limit":"5"},{"source_kinds":["x"]}):
            with self.assertRaises(ValueError):
                BRIDGE.sessions_list(rpc, bad)

    def test_summary_is_redacted_and_untrusted(self):
        s = BRIDGE.sessions_list(ListDaemon([listed(1,"exec")]), {})["sessions"][0]
        self.assertNotIn("abc123secret", s["name"])
        self.assertEqual((s["trust"], s["source"]), ("UNTRUSTED", "local_codex"))


if __name__ == "__main__":
    unittest.main()
