package approvedtask

import (
	"strings"
	"testing"

	"github.com/dop251/goja"
)

func TestApprovalCardReadsOriginalTaskAfterLostStatus(t *testing.T) {
	rt := goja.New()
	runJS := func(source string) {
		t.Helper()
		if _, err := rt.RunString(source); err != nil {
			t.Fatal(err)
		}
	}
	runJS(`
const messages = [], handlers = {}, pendingTimers = new Map();
const nodes = {status: {}, run: {}, cancel: {}};
const parent = {postMessage: (message) => messages.push(message)};
const document = {hidden: false, getElementById: id => nodes[id],
                  addEventListener: (name, fn) => handlers[name] = fn};
const window = {addEventListener: (name, fn) => handlers[name] = fn};
function setTimeout(fn, delay) {
  const id = Symbol();
  pendingTimers.set(id, {fn, delay});
  return id;
}
function clearTimeout(id) {pendingTimers.delete(id)}
function fire(delay) {
  for (const [id, timer] of pendingTimers) {
    if (timer.delay !== delay) continue;
    pendingTimers.delete(id);
    timer.fn();
    return;
  }
  throw new Error("missing timer " + delay);
}
function deliver(params) {handlers.message({source:parent, data:{jsonrpc:"2.0", ...params}})}
function check(ok, label) {if (!ok) throw new Error(label)}
class URL {
  constructor(href) {
    this.href = href;
    this.protocol = "https:";
    this.pathname = "/approval/01234567";
  }
}
`)
	_, after, ok := strings.Cut(reviewHTML, "<script>")
	if !ok {
		t.Fatal("approval resource has no script")
	}
	script, _, _ := strings.Cut(after, "</script>")
	runJS(script)
	runJS(`
const id = "01234567890123456789012345678901";
deliver({id:1, result:{}});
deliver({method:"ui/notifications/tool-result", params:{structuredContent:{
  id, state:"AWAITING_FACE_ID", approval_state:"awaiting_face_id",
  approval_url:"https://h96-host.example/approval/01234567"
}}});
check(!nodes.run.disabled, "prepared task is not ready");
nodes.run.onclick();
nodes.run.onclick();
check(messages.filter(x => x.method === "ui/open-link").length === 1, "duplicate portal open");
fire(1100);
const first = messages[messages.length - 1];
check(first.method === "tools/call" && first.params.name === "get_tool_result" &&
      first.params.arguments.call_id === id, "poll did not read the original task");
fire(6000);
fire(2500);
const second = messages[messages.length - 1];
check(second.id !== first.id && second.params.arguments.call_id === id, "status retry changed task");
deliver({id:first.id, result:{structuredContent:{id, state:"FAILED", effect:"none"}}});
deliver({id:second.id, result:{structuredContent:{call_id:id, state:"DONE", effect:"applied", output:"private"}}});
check(nodes.status.textContent.includes(id) && !nodes.status.textContent.includes("private"), "wrong final display");
const contextUpdates = messages.filter(x => x.method === "ui/update-model-context");
check(contextUpdates.length === 1 && !contextUpdates[0].params.content[0].text.includes("private"),
      "terminal result leaked output or updated the chat twice");
handlers.online();
handlers.visibilitychange();
check(pendingTimers.size === 0, "terminal task restarted after reconnect");
`)
}
