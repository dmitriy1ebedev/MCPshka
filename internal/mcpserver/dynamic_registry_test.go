package mcpserver

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/toolforge"
)

func dynamicRegistryFixture(t *testing.T) *toolRegistry {
	t.Helper()
	r := &toolRegistry{server: mcp.NewServer(&mcp.Implementation{Name: "dynamic-registry", Version: "1"}, nil), store: idempotency.New(t.TempDir()), readers: map[string]probeHandler{}, tools: map[string]bool{}, catalogRevision: "initial-fixture", policyRevision: "fixture-policy"}
	addTool(r, &mcp.Tool{Name: "fixture_read", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, map[string]any, error) {
		return nil, map[string]any{"ok": true}, nil
	})
	return r
}

func TestDynamicCatalogChangesRevisionAndKeepsResolverCurrent(t *testing.T) {
	r := dynamicRegistryFixture(t)
	ctx := context.Background()
	before, _, err := r.capabilityRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r.registerComposite(toolforge.Composite{Name: "x_dynamic_read", ReadOnly: true, Description: "A read-only fixture composite", Steps: []toolforge.Step{{Tool: "fixture_read"}}})
	after, _, err := r.capabilityRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("runtime catalog changed without a revision change")
	}
	stale, err := r.resolveCapabilities(ctx, ToolResolveInput{Goal: "x_dynamic_read", ExpectedRevision: before, Compact: true})
	if err != nil || stale.Code != "CLIENT_SCHEMA_STALE" || len(stale.Candidates) != 0 {
		t.Fatal("old revision accepted", stale, err)
	}
	fresh, err := r.resolveCapabilities(ctx, ToolResolveInput{Goal: "x_dynamic_read", ExpectedRevision: after, Compact: true})
	if err != nil || fresh.Code != "OK" || len(fresh.Candidates) != 1 || fresh.Candidates[0].Name != "x_dynamic_read" {
		t.Fatal("new capability unavailable", fresh, err)
	}
}

func TestDynamicContractsNeverMutateSharedStaticTable(t *testing.T) {
	r := dynamicRegistryFixture(t)
	addTool(r, &mcp.Tool{Name: "service_action"}, func(context.Context, *mcp.CallToolRequest, ServiceActionInput) (*mcp.CallToolResult, handlerOutput, error) {
		return nil, handlerOutput{}, nil
	})
	r.registerComposite(toolforge.Composite{Name: "x_dynamic_mutation", Description: "A mutation fixture composite", Steps: []toolforge.Step{{Tool: "service_action"}}})
	if _, ok := mutationContracts["x_dynamic_mutation"]; ok {
		t.Fatal("one registry modified the process-wide static contracts")
	}
}

func TestDynamicMutationReceiptsStayScopedAndReconcilable(t *testing.T) {
	r := dynamicRegistryFixture(t)
	r.readers["service_status"] = func(context.Context, map[string]any) (any, error) { return map[string]any{"state": "observed"}, nil }
	r.readers["state_status"] = func(context.Context, map[string]any) (any, error) { return map[string]any{"state": "observed"}, nil }
	calls := 0
	addTool(r, &mcp.Tool{Name: "service_action"}, func(context.Context, *mcp.CallToolRequest, ServiceActionInput) (*mcp.CallToolResult, handlerOutput, error) {
		calls++
		return nil, handlerOutput{Value: "applied"}, nil
	})
	r.registerComposite(toolforge.Composite{Name: "x_dynamic_receipt", Description: "A scoped mutation fixture", Steps: []toolforge.Step{{Tool: "service_action", Args: map[string]any{"service": "fixture.service", "action": "restart"}}}})
	args := map[string]any{"idempotency_key": "dynamic-receipt"}
	for range 2 {
		if _, err := r.invoke(context.Background(), ToolInvokeInput{Name: "x_dynamic_receipt", Arguments: args}); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatal("dynamic effect replayed", calls)
	}
	out, err := r.probeMutation(context.Background(), MutationProbeInput{Tool: "x_dynamic_receipt", Key: "dynamic-receipt"})
	if err != nil || out["outcome"] != "handler_completed" {
		t.Fatal("dynamic receipt cannot reconcile", out, err)
	}
	other := dynamicRegistryFixture(t)
	if _, err := other.probeMutation(context.Background(), MutationProbeInput{Tool: "x_dynamic_receipt", Key: "dynamic-receipt"}); err == nil {
		t.Fatal("foreign registry's dynamic contract accepted")
	}
}

func TestDynamicCatalogConcurrentDiscoveryAndInvocation(t *testing.T) {
	r := dynamicRegistryFixture(t)
	ctx := context.Background()
	audit := filepath.Join(t.TempDir(), "audit.jsonl")
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Go(func() {
			for range 30 {
				r.capabilityNames()
				r.capabilitySchemas()
				if _, err := r.resolveCapabilities(ctx, ToolResolveInput{Goal: "fixture_read", Compact: true}); err != nil {
					t.Error(err)
					return
				}
				if _, err := r.invoke(ctx, ToolInvokeInput{Name: "fixture_read", Arguments: map[string]any{}}); err != nil {
					t.Error(err)
					return
				}
				r.forgeCatalog(audit)
			}
		})
	}
	wg.Go(func() {
		for i := 0; i < 12; i++ {
			r.registerComposite(toolforge.Composite{Name: fmt.Sprintf("x_dynamic_%d", i), ReadOnly: true, Description: "A concurrent read-only fixture", Steps: []toolforge.Step{{Tool: "fixture_read"}}})
		}
	})
	wg.Wait()
}
