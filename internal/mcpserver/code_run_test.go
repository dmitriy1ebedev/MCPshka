package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/idempotency"
)

type echoIn struct {
	N int `json:"n"`
}
type echoOut struct {
	Double int `json:"double"`
}

func codeRegistry(t *testing.T) *toolRegistry {
	server := mcp.NewServer(&mcp.Implementation{Name: "code-test", Version: "1"}, nil)
	r := &toolRegistry{server: server, store: idempotency.New(t.TempDir()), readers: map[string]probeHandler{}, tools: map[string]bool{}}
	addTool(r, &mcp.Tool{Name: "echo_double", Description: "test", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, _ *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, echoOut, error) {
		return nil, echoOut{Double: in.N * 2}, nil
	})
	registerToolInvoke(r)
	registerCodeRun(r)
	return r
}

func TestCodeRunChainsReadOnlyCalls(t *testing.T) {
	r := codeRegistry(t)
	out, err := r.codeRun(context.Background(), CodeRunInput{Script: `let s = 0; for (let i = 1; i <= 3; i++) { s += mcp.call('echo_double', {n: i}).double } console.log('sum', s); return {sum: s}`})
	if err != nil || out.Error != "" {
		t.Fatalf("%v %+v", err, out)
	}
	if m, _ := out.Result.(map[string]any); m["sum"] != float64(12) || len(out.Calls) != 3 || len(out.Logs) != 1 {
		t.Fatalf("%+v", out)
	}
	// 0.10.0 shipped code_run outside addTool: tool_invoke said "unknown tool".
	res, err := r.invoke(context.Background(), ToolInvokeInput{Name: "code_run", Arguments: map[string]any{"script": "return 1"}})
	if err != nil || res.IsError || r.toolEffect("code_run") != "read_only" {
		t.Fatalf("code_run via tool_invoke: %v %+v effect %s", err, res, r.toolEffect("code_run"))
	}
}

func TestCodeRunRefusesAndLimits(t *testing.T) {
	r := codeRegistry(t)
	for script, want := range map[string]string{
		`return mcp.call('fs_write', {path: '/tmp/x'})`:      "POLICY_DENIED",
		`return mcp.call('tool_invoke', {name: 'fs_write'})`: "POLICY_DENIED",
		`for (;;) {}`: "TIMEOUT",
		`return typeof ArrayBuffer + typeof Uint8Array`:               "",
		`for (let i = 0; i < 3; i++) mcp.call('echo_double', {n: i})`: "LIMIT",
	} {
		out, err := r.codeRun(context.Background(), CodeRunInput{Script: script, TimeoutSeconds: 1, MaxCalls: 2})
		if err != nil {
			t.Fatal(err)
		}
		if want == "" {
			if out.Result != "undefinedundefined" {
				t.Fatalf("typed arrays left: %+v", out)
			}
			continue
		}
		if !strings.Contains(out.Error, want) {
			t.Fatalf("%s: want %s, got %+v", script, want, out)
		}
	}
}
