"""Additional offline boundary checks against the pinned 107 bridge snapshot."""
import importlib.util
import pathlib
import unittest

SPEC = importlib.util.spec_from_file_location("bridge", pathlib.Path(__file__).with_name("codex-bridge.py"))
BRIDGE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(BRIDGE)
SESSION = "session_12345"
REQUEST = "a" * 32


class RPC:
    def __init__(self, queue=None, turns=None, session=SESSION):
        self.queue = queue or []
        self.turns = turns or []
        self.session = session
        self.calls = []

    def call(self, name, params):
        self.calls.append((name, params))
        if name == "thread/read":
            return {"thread": {"id": self.session, "status": {"type": "active"}}}
        if name == "thread/queue/list":
            return {"data": self.queue}
        if name == "thread/turns/list":
            return {"data": self.turns}
        raise AssertionError("probe attempted mutation: " + name)


class BoundaryChecks(unittest.TestCase):
    def probe(self, rpc, **extra):
        return BRIDGE.task_status(rpc, dict(session_id=SESSION, request_id=REQUEST, **extra))

    def test_invalid_probe_rejected_before_daemon_call(self):
        base = dict(session_id=SESSION, request_id=REQUEST)
        for delta in ({"session_id": "../session"}, {"session_id": 123},
                      {"request_id": "b" * 31}, {"request_id": "B" * 32},
                      {"queue_id": "../queue"}, {"turn_id": 123},
                      {"queue_cursor": "x" * 2049}, {"turn_cursor": 123},
                      {"approval_token": "owner-only"}):
            with self.subTest(delta=delta):
                rpc = RPC()
                with self.assertRaises(ValueError):
                    BRIDGE.task_status(rpc, dict(base, **delta))
                self.assertEqual(rpc.calls, [])

    def test_wrong_session_cannot_supply_task_evidence(self):
        rpc = RPC(session="session_foreign")
        with self.assertRaises(ValueError):
            self.probe(rpc)
        self.assertEqual([name for name, _ in rpc.calls], ["thread/read"])

    def test_queue_and_request_must_identify_same_submission(self):
        rpc = RPC(queue=[{"id": "queue_abcdefgh", "clientUserMessageId": "b" * 32},
                         {"id": "queue_foreign", "clientUserMessageId": REQUEST}])
        self.assertEqual(self.probe(rpc, queue_id="queue_abcdefgh")["state"], "unknown")

    def test_ambiguous_submission_fails_closed(self):
        rpc = RPC(queue=[{"id": "queue_abcdefgh", "clientUserMessageId": REQUEST},
                         {"id": "queue_duplicate", "clientUserMessageId": REQUEST}])
        with self.assertRaises(ValueError):
            self.probe(rpc)

    def test_unrecognized_turn_state_never_becomes_completed(self):
        rpc = RPC(turns=[{"id": "turn_abcdefgh", "status": "futureState",
                          "items": [{"type": "userMessage", "clientId": REQUEST}]}])
        self.assertEqual(self.probe(rpc)["state"], "unknown")

    def test_newest_unicode_output_stays_within_byte_and_item_budget(self):
        items = [{"type": "agentMessage", "text": "старое " + str(i)} for i in range(100)]
        items.append({"type": "agentMessage", "text": "последнее " * 1000})
        rpc = RPC(turns=[{"id": "turn_abcdefgh", "status": "inProgress", "items": items}])
        out = BRIDGE.session_read(rpc, dict(session_id=SESSION, output_limit_bytes=1024))
        shown = out["turns"][0]["items"]
        self.assertLessEqual(len(shown), 64)
        self.assertLessEqual(sum(len(i["text"].encode()) for i in shown), 1024)
        self.assertIn("последнее", shown[-1]["text"])
        self.assertTrue(out["truncated"])


if __name__ == "__main__":
    unittest.main()
