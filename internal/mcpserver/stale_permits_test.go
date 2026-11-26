package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/idempotency"
)

func TestStalePermitToolsExposeReadOnlyPreviewAndMutationContract(t *testing.T) {
	store := idempotency.New(t.TempDir())
	server := mcp.NewServer(&mcp.Implementation{Name: "stale-test", Version: "1"}, nil)
	r := &toolRegistry{server: server, store: store, readers: map[string]probeHandler{}, tools: map[string]bool{}}
	registerStalePermitTools(r)
	if r.catalog["executor_admin_stale_preview"] == nil || !r.catalog["executor_admin_stale_preview"].Annotations.ReadOnlyHint {
		t.Fatal("preview not read-only")
	}
	if r.catalog["executor_admin_stale_reconcile"] == nil || r.catalog["executor_admin_stale_reconcile"].Annotations.ReadOnlyHint {
		t.Fatal("apply not a mutation")
	}
	contract, ok := mutationContracts["executor_admin_stale_reconcile"]
	if !ok || contract.Validate() != nil || contract.Probe != "executor_admin_stale_preview" || contract.Key != "idempotency_key" || !contract.ReplayPending {
		t.Fatal("missing mutation contract")
	}
	for _, name := range []string{"executor_admin_stale_preview", "executor_admin_stale_reconcile"} {
		if toolAliasesRU[name] == "" || capabilityAliases[name] == "" {
			t.Fatal("missing aliases", name)
		}
	}
	// The reader must require authenticated ownership before the root broker.
	if _, err := r.readers["executor_admin_stale_preview"](context.Background(), map[string]any{"session_id": "invalid"}); err == nil {
		t.Fatal("unbound admin preview admitted")
	}
}
