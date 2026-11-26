package mcpserver

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/filesystem"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
)

func TestCoreSurfaceIsCompactAndComplete(t *testing.T) {
	ctx := context.Background()
	p := policy.Default()
	p.AllowedWorkdirs = []string{t.TempDir()}
	manager, err := jobs.New(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	_, core := newServers(manager, filesystem.New(p), nil, newWorkflowCoordinator(manager))
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ss, err := core.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })

	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, tool := range list.Tools {
		listed[tool.Name] = true
	}
	if len(list.Tools) != len(coreTools) {
		t.Fatalf("core lists %d tools, want every one of %d: %v", len(list.Tools), len(coreTools), listed)
	}

	// A core tool runs its full wrapper and returns readable text.
	text, isErr := callText(t, session, "tool_invoke", map[string]any{"name": "mcpshka_guide", "arguments": map[string]any{"topic": "rules"}})
	if isErr || text == "" {
		t.Fatalf("mcpshka_guide on core: err=%v %q", isErr, text)
	}
	// Anything outside the core stays reachable through tool_invoke.
	text, isErr = callText(t, session, "tool_invoke", map[string]any{"name": "fs_list", "arguments": map[string]any{"path": p.AllowedWorkdirs[0]}})
	if isErr || !strings.Contains(text, "fs_list") {
		t.Fatalf("tool_invoke fs_list on core: err=%v %q", isErr, text)
	}
	if _, isErr = callText(t, session, "tool_invoke", map[string]any{"name": "no_such_tool"}); !isErr {
		t.Fatal("unknown tool must fail")
	}
}

func TestPickServerByQuery(t *testing.T) {
	full, core := &mcp.Server{}, &mcp.Server{}
	pick := pickServer(full, core)
	for url, want := range map[string]*mcp.Server{"/mcp": full, "/mcp?tools=core": core, "/mcp?tools=all": full} {
		req, _ := http.NewRequest("POST", url, nil)
		if pick(req) != want {
			t.Fatalf("%s picked the wrong surface", url)
		}
	}
}

func TestCoreNotifiesClientWhenToolCatalogChanges(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Empty registry deliberately tests HasTools even when no tool exists at Connect.
	core := newCoreServer(&toolRegistry{catalog: map[string]*mcp.Tool{}, invokers: map[string]toolInvoker{}})
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ss, err := core.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	changed := make(chan struct{}, 1)
	c := mcp.NewClient(&mcp.Implementation{Name: "live-client", Version: "1"}, &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) { changed <- struct{}{} },
	})
	session, err := c.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	mcp.AddTool(core, &mcp.Tool{Name: "new_tool"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, nil, nil
	})
	select {
	case <-changed:
	case <-ctx.Done():
		t.Fatal("connected client did not receive tools/list_changed", ctx.Err())
	}
	page, err := session.ListTools(ctx, nil)
	if err != nil || len(page.Tools) != 1 || page.Tools[0].Name != "new_tool" {
		t.Fatalf("live client could not refresh changed catalog: %+v %v", page, err)
	}
}
