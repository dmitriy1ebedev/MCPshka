package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/filesystem"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
)

func toolInvokeSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	p := policy.Default()
	p.AllowedWorkdirs = []string{t.TempDir()}
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
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func callText(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if text, ok := c.(*mcp.TextContent); ok {
			b.WriteString(text.Text)
		}
	}
	return b.String(), res.IsError
}

func TestToolInvokeRunsTheSameWrappedTool(t *testing.T) {
	s := toolInvokeSession(t)
	direct, isErr := callText(t, s, "mcpshka_guide", map[string]any{"topic": "rules"})
	if isErr {
		t.Fatal(direct)
	}
	viaInvoke, isErr := callText(t, s, "tool_invoke", map[string]any{"name": "mcpshka_guide", "arguments": map[string]any{"topic": "rules"}})
	if isErr {
		t.Fatal(viaInvoke)
	}
	var got struct {
		Tool   string         `json:"tool"`
		Output map[string]any `json:"output"`
	}
	if err := json.Unmarshal([]byte(viaInvoke), &got); err != nil {
		t.Fatal(err, viaInvoke)
	}
	var want map[string]any
	if err := json.Unmarshal([]byte(direct), &want); err != nil {
		t.Fatal(err)
	}
	if got.Tool != "mcpshka_guide" || got.Output["text"] != want["text"] || want["text"] == nil {
		t.Fatalf("tool_invoke output differs: %s", viaInvoke)
	}
}

func TestToolInvokeKeepsMutationContract(t *testing.T) {
	s := toolInvokeSession(t)
	// A missing key fails schema validation on both paths.
	args := map[string]any{"path": t.TempDir(), "owner": "test"}
	via, isErr := callText(t, s, "tool_invoke", map[string]any{"name": "project_lease_acquire", "arguments": args})
	if !isErr || !strings.Contains(via, `missing properties: ["idempotency_key"]`) {
		t.Fatalf("tool_invoke accepted a mutation without retry key: %q", via)
	}
	// An empty key passes the schema and must be refused by the mutation
	// wrapper itself, word for word as on the direct path.
	args["idempotency_key"] = ""
	direct, isErr := callText(t, s, "project_lease_acquire", args)
	if !isErr {
		t.Fatalf("mutation with empty retry key accepted directly: %s", direct)
	}
	via, isErr = callText(t, s, "tool_invoke", map[string]any{"name": "project_lease_acquire", "arguments": args})
	if !isErr || via != direct {
		t.Fatalf("tool_invoke must fail exactly like the direct call:\n direct %q\n invoke %q", direct, via)
	}
}

func TestToolInvokeRejects(t *testing.T) {
	s := toolInvokeSession(t)
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"name": "no_such_tool"}, "unknown tool"},
		{map[string]any{"name": "tool_invoke", "arguments": map[string]any{"name": "mcpshka_guide"}}, "cannot call itself"},
		{map[string]any{"name": "mcpshka_guide", "arguments": map[string]any{"topic": "rules", "extra": true}}, "schema"},
	} {
		text, isErr := callText(t, s, "tool_invoke", tc.args)
		if !isErr || !strings.Contains(text, tc.want) {
			t.Fatalf("%v: got %q", tc.args, text)
		}
	}
}
