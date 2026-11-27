package mcpserver

import (
	"context"
	"encoding/json"
	"mcpshka/internal/tools/execroute"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/failure"
	"mcpshka/internal/filesystem"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
)

// staleSnapshotClient is ChatGPT with a tools/list frozen before tool_invoke,
// mcpshka_guide and agent_task_status existed: it calls only exec_start and
// job_output.
type staleSnapshotClient struct {
	t       *testing.T
	session *mcp.ClientSession
	workdir string
}

func newStaleSnapshotClient(t *testing.T) staleSnapshotClient {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	p := policy.Default()
	p.AllowedWorkdirs = []string{dir}
	manager, err := jobs.New(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	server := New(manager, filesystem.New(p))
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { serverSession.Close() })
	session, err := mcp.NewClient(&mcp.Implementation{Name: "chatgpt-stale", Version: "1"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return staleSnapshotClient{t: t, session: session, workdir: dir}
}

// run calls exec_start command=mcpshka and returns job_output's stdout.
func (c staleSnapshotClient) run(key string, args ...string) (string, string) {
	c.t.Helper()
	out, isErr := callText(c.t, c.session, "exec_start", map[string]any{"command": "mcpshka", "args": args, "workdir": c.workdir, "idempotency_key": key})
	if isErr {
		return "", out
	}
	var job struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(out), &job); err != nil || job.ID == "" {
		c.t.Fatal("exec_start did not return a job", out, err)
	}
	text, isErr := callText(c.t, c.session, "job_output", map[string]any{"job_id": job.ID})
	if isErr {
		c.t.Fatal(text)
	}
	var o struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(text), &o); err != nil {
		c.t.Fatal(err, text)
	}
	return o.Text, ""
}

func TestStaleSnapshotClientReachesEveryToolThroughExecStart(t *testing.T) {
	c := newStaleSnapshotClient(t)
	guide, errText := c.run("guide-1", "guide", "rules")
	if errText != "" || !strings.Contains(guide, "tool_invoke") {
		t.Fatal("guide via exec_start", guide, errText)
	}
	resolved, errText := c.run("resolve-1", "resolve", "agent task status")
	if errText != "" || !strings.Contains(resolved, "agent_task_status") || !strings.Contains(resolved, "tool-invoke") {
		t.Fatal("resolve via exec_start", resolved, errText)
	}
	boot, errText := c.run("invoke-1", "tool-invoke", "capabilities_bootstrap", "{}")
	if errText != "" || !strings.Contains(boot, `"tool":"capabilities_bootstrap"`) || !strings.Contains(boot, "server_version") {
		t.Fatal("tool-invoke via exec_start", boot, errText)
	}
	// Arguments are checked against the target's own schema.
	if _, errText = c.run("invoke-2", "tool-invoke", "mcpshka_guide", `{"topic":"rules","bogus":1}`); errText == "" {
		t.Fatal("unknown argument accepted")
	}
	if _, errText = c.run("invoke-3", "tool-invoke", "no_such_tool"); !strings.Contains(errText, "unknown tool") {
		t.Fatal("unknown tool", errText)
	}
	if _, errText = c.run("invoke-4", "tool-invoke", "tool_invoke", `{"name":"mcpshka_guide"}`); errText == "" {
		t.Fatal("tool_invoke recursion accepted")
	}
	// A refused ordinary command names the typed path.
	if errText, isErr := callText(t, c.session, "exec_start", map[string]any{"command": "bash", "args": []string{"-c", "id"}, "workdir": c.workdir, "idempotency_key": "refused-1"}); !isErr || !strings.Contains(errText, `"tool-invoke"`) {
		t.Fatal("exec_start refusal has no tool-invoke hint", errText)
	}
	if _, errText = c.run("invoke-5", "tool-invoke", "mcpshka_guide", "[1]"); !strings.Contains(errText, "JSON object") {
		t.Fatal("non-object arguments", errText)
	}
}

func TestMCP255FixesReviewListsWithoutPathAndReplays(t *testing.T) {
	session := fullCatalogClient(t)
	args := map[string]any{"action": "list", "idempotency_key": "free-review-list"}
	for _, call := range []struct {
		name string
		args map[string]any
	}{
		{"fixes_review", args},
		{"tool_invoke", map[string]any{"name": "fixes_review", "arguments": args}},
	} {
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: call.name, Arguments: call.args})
		if err != nil || res.IsError {
			t.Fatalf("%s failed: %+v, %v", call.name, res, err)
		}
		raw, err := json.Marshal(res.StructuredContent)
		if err != nil || !strings.Contains(string(raw), `"fixes":[]`) {
			t.Fatalf("%s lost the empty queue: %s, %v", call.name, raw, err)
		}
	}
}

func TestMCP242LsRoutesToNativeFilesystemTool(t *testing.T) {
	for _, tc := range []struct {
		command string
		args    []string
		workdir string
		want    string
	}{
		{"ls", nil, "/root/repos/mcpshka", "/root/repos/mcpshka"},
		{"/usr/bin/ls", []string{"-lah", "internal"}, "/root/repos/mcpshka", "/root/repos/mcpshka/internal"},
		{"ls", []string{"/root/repos/lane"}, "/root/repos/mcpshka", "/root/repos/lane"},
	} {
		err := execroute.FileList(tc.command, tc.args, tc.workdir)
		if err == nil || !strings.Contains(err.Error(), "fs_list") {
			t.Fatalf("ls must name bounded fs_list: %v", err)
		}
		if !strings.Contains(err.Error(), "directory listings") {
			t.Fatal(err)
		}
		d := failure.Details(err)
		if d == nil || !d.NoEffect || d.NextSafeAction.Tool != "fs_list" ||
			d.NextSafeAction.Arguments["path"] != tc.want {
			t.Fatalf("wrong safe native route for %q: %+v", tc.want, d)
		}
	}
	if err := execroute.FileList("git", []string{"status"}, "/root/repos/mcpshka"); err != nil {
		t.Fatal("unrelated CLI must not be rerouted", err)
	}
	c := newStaleSnapshotClient(t)
	errText, isErr := callText(t, c.session, "exec_start",
		map[string]any{"command": "/usr/bin/ls", "args": []string{"-lah"},
			"workdir": c.workdir, "idempotency_key": "mcp242-ls-route"})
	if !isErr || !strings.Contains(errText, "fs_list") {
		t.Fatal("MCP client must receive usable typed route, no ls process", errText, isErr)
	}
}

func TestMCP245GrepUsesTypedBoundedSearchOrExplicitFallback(t *testing.T) {
	for _, tc := range []struct {
		args                            []string
		wantTool, wantPath, wantPattern string
		wantRegex                       bool
	}{
		{[]string{"-Rni", "timeout", "internal"}, "fs_find", "/root/repos/mcpshka/internal", "(?i)timeout", true},
		{[]string{"-F", "a.b", "/root/repos/lane"}, "fs_find", "/root/repos/lane", "a.b", false},
		{[]string{"-Fi", "Ab.c", "docs"}, "fs_find", "/root/repos/mcpshka/docs", "(?i)Ab\\.c", true},
		{[]string{"-v", "error", "internal"}, "tool_resolve", "", "", false},
		{[]string{"stream-only"}, "tool_resolve", "", "", false},
		{[]string{"-R", "one", "dir1", "dir2"}, "tool_resolve", "", "", false},
	} {
		err := execroute.FileContentSearch("/usr/bin/grep", tc.args, "/root/repos/mcpshka")
		d := failure.Details(err)
		if d == nil || !d.NoEffect || d.NextSafeAction.Tool != tc.wantTool {
			t.Fatalf("unsafe grep route for %v: %v / %+v", tc.args, err, d)
		}
		if tc.wantTool == "fs_find" {
			args := d.NextSafeAction.Arguments
			if args["path"] != tc.wantPath || args["pattern"] != tc.wantPattern || args["regex"] != tc.wantRegex {
				t.Fatalf("wrong native search args for %v: %v", tc.args, args)
			}
		}
	}
	if err := execroute.FileContentSearch("rg", []string{"x", "dir"}, "/root/repos/mcpshka"); err != nil {
		t.Fatal("unrelated CLI changed", err)
	}
	c := newStaleSnapshotClient(t)
	errText, isErr := callText(t, c.session, "exec_start", map[string]any{
		"command": "grep", "args": []string{"-R", "target", "."},
		"workdir": c.workdir, "idempotency_key": "mcp245-grep-route"})
	if !isErr || !strings.Contains(errText, "fs_find") {
		t.Fatal("MCP caller must receive native fs_find route with no grep process", isErr, errText)
	}
}

func TestMCP227GitAddRoutesToAtomicProjectCommit(t *testing.T) {
	for _, tc := range []struct {
		args        []string
		wantReroute bool
	}{
		{[]string{"add", "internal/mcpserver/tools_jobs.go"}, true},
		{[]string{"-C", "/root/repos/mcpshka", "add", "--", "README.md"}, true},
		{[]string{"-c", "core.safecrlf=false", "add", "foo"}, true},
		{[]string{"status", "--short"}, false},
		{[]string{"diff", "--stat"}, false},
	} {
		err := execroute.GitAdd("/usr/bin/git", tc.args)
		if !tc.wantReroute {
			if err != nil {
				t.Fatalf("non-staging git command refused: %v", err)
			}
			continue
		}
		d := failure.Details(err)
		if d == nil || !d.NoEffect || d.NextSafeAction.Tool != "tool_resolve" ||
			!strings.Contains(err.Error(), "project_commit") {
			t.Fatalf("git add must route to native lease-guarded project_commit: %v / %+v", err, d)
		}
	}
	if err := execroute.GitAdd("other", []string{"add"}); err != nil {
		t.Fatal("non-Git executable changed", err)
	}
	c := newStaleSnapshotClient(t)
	errText, isErr := callText(t, c.session, "exec_start",
		map[string]any{"command": "git", "args": []string{"add", "README.md"},
			"workdir": c.workdir, "idempotency_key": "mcp227-stage-route"})
	if !isErr || !strings.Contains(errText, "project_commit") {
		t.Fatal("MCP caller did not receive native stage+commit route", isErr, errText)
	}
}

func TestMCP244JQUsesNativeReadOnlyCodeRun(t *testing.T) {
	for _, tc := range []struct {
		args                  []string
		wantTool, wantSnippet string
	}{
		{[]string{"-r", ".deep.value", "fixture.json"}, "code_run", `const keys=["deep","value"]`},
		{[]string{".[1]", "fixture.json"}, "code_run", "const keys=[1]"},
		{[]string{".", "fixture.json"}, "code_run", "const keys=[]"},
		{[]string{".[]", "fixture.json"}, "tool_resolve", ""},
		{[]string{"--arg", "x", "value", ".deep.value", "fixture.json"}, "tool_resolve", ""},
		{[]string{".deep.value"}, "tool_resolve", ""},
	} {
		err := execroute.JQJSON("/usr/bin/jq", tc.args, "/root/repos/mcpshka")
		d := failure.Details(err)
		if d == nil || !d.NoEffect || d.NextSafeAction.Tool != tc.wantTool {
			t.Fatalf("unexpected jq native route for %v: %v / %+v", tc.args, err, d)
		}
		if tc.wantTool == "code_run" {
			script, ok := d.NextSafeAction.Arguments["script"].(string)
			if !ok || !strings.Contains(script, tc.wantSnippet) ||
				!strings.Contains(script, "JSON.parse(content)") ||
				!strings.Contains(script, "mcp.call('fs_read'") ||
				strings.Contains(script, "exec_start") {
				t.Fatalf("invalid bounded read-only jq adapter: %q", script)
			}
		}
	}
	if err := execroute.JQJSON("git", []string{".foo", "file.json"}, "/root/repos/mcpshka"); err != nil {
		t.Fatal("non-jq executable changed", err)
	}
	c := newStaleSnapshotClient(t)
	file := filepath.Join(c.workdir, "fixture.json")
	if err := os.WriteFile(file, []byte(`{"deep":{"value":"native-json-ok"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	blocked, isErr := callText(t, c.session, "exec_start", map[string]any{
		"command": "jq", "args": []string{".deep.value", file},
		"workdir": c.workdir, "idempotency_key": "mcp244-jq-route"})
	if !isErr || !strings.Contains(blocked, "code_run") {
		t.Fatal("jq should resolve natively without starting a process", isErr, blocked)
	}
	d := failure.Details(execroute.JQJSON("jq", []string{".deep.value", file}, c.workdir))
	script := d.NextSafeAction.Arguments["script"].(string)
	result, failed := callText(t, c.session, "code_run",
		map[string]any{"script": script, "max_calls": 2, "timeout_seconds": 20})
	if failed || !strings.Contains(result, "native-json-ok") {
		t.Fatal("typed native code_run failed to extract JSON", failed, result)
	}
}

func TestToolInvokeCompatRequestParsing(t *testing.T) {
	for _, bad := range []ExecStartInput{
		{Args: []string{"tool-invoke"}},
		{Args: []string{"tool-invoke", "x", "{}", "extra"}},
		{Args: []string{"guide", "a", "b"}},
		{Args: []string{"resolve"}},
		{Args: []string{"tool-invoke", "x"}, Env: map[string]string{"A": "b"}},
		{Args: []string{"tool-invoke", "x"}, LeaseToken: "t"},
	} {
		if _, err := toolInvokeCompatRequest(bad); err == nil {
			t.Fatal("accepted", bad)
		}
	}
	got, err := toolInvokeCompatRequest(ExecStartInput{Args: []string{"tool-invoke", "agent_task_status", `{"id":"3a192659e7736268"}`}})
	if err != nil || got.Name != "agent_task_status" || got.Arguments["id"] != "3a192659e7736268" {
		t.Fatal(got, err)
	}
}
