package mcpserver

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"testing"
)

func TestProjectContextLineageThroughTool(t *testing.T) {
	m, _, in := yieldFixture(t)
	r := &toolRegistry{server: mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil), tools: map[string]bool{}, readers: map[string]probeHandler{}}
	registerProjectContext(r, m)
	ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: "reader", Role: "reader", Scopes: []string{"project_context"}})
	v, err := r.readers["project_context"](ctx, map[string]any{"path": in.Path})
	if err != nil {
		t.Fatal(err)
	}
	c := v.(ProjectContext).SourceChain
	if c.Source.Identity != in.ExpectedRevision || c.CI.State != "UNKNOWN" || c.Installed.State != "UNKNOWN" {
		t.Fatal(c)
	}
}
