package mcpserver

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/idempotency"
)

func TestDelegatingToolsAreNarrowDispatchers(t *testing.T) {
	if len(delegatingTools) != 2 {
		t.Fatalf("delegating exemption grew: %v", delegatingTools)
	}
	for name := range delegatingTools {
		if _, own := mutationContracts[name]; own {
			t.Fatalf("%s has its own contract; drop the exemption", name)
		}
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "delegating-test", Version: "1"}, nil)
	r := &toolRegistry{server: server, store: idempotency.New(t.TempDir()), readers: map[string]probeHandler{}, tools: map[string]bool{}}
	registerToolInvoke(r)
	got := r.forgeCatalog(t.TempDir() + "/audit.log")["tool_invoke"]
	if !got.HasContract || got.ReadOnly {
		t.Fatalf("tool_invoke audit entry %+v", got)
	}
}
