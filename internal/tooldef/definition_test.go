package tooldef

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDefinitionPreservesTypedInputAndWrapper(t *testing.T) {
	type input struct {
		Count int `json:"count"`
	}
	type output struct {
		Count int `json:"count"`
	}
	d := New(&mcp.Tool{Name: "test"}, func(_ context.Context, req *mcp.CallToolRequest, in input) (*mcp.CallToolResult, output, error) {
		if req == nil || req.Params.Name != "test" {
			t.Fatal("request lost")
		}
		return nil, output{in.Count}, nil
	})
	calls := 0
	invoke := d.Invoker(func(ctx context.Context, req *mcp.CallToolRequest, in any) (*mcp.CallToolResult, any, error) {
		calls++
		return d.Handler(ctx, req, in)
	})
	_, value, err := invoke(context.Background(), map[string]any{"count": 7})
	if err != nil || value != (output{7}) || calls != 1 {
		t.Fatalf("value=%v err=%v calls=%d", value, err, calls)
	}
	if _, _, err := invoke(context.Background(), map[string]any{"count": "invalid"}); err == nil || calls != 1 {
		t.Fatalf("invalid input reached handler: %v", err)
	}
}

func TestDefinitionPreservesNilInterfaceInput(t *testing.T) {
	d := New(&mcp.Tool{Name: "nil"}, func(_ context.Context, _ *mcp.CallToolRequest, in any) (*mcp.CallToolResult, any, error) {
		if in != nil {
			t.Fatal("nil input changed")
		}
		return nil, nil, nil
	})
	input, err := d.Decode(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.Handler(context.Background(), nil, input); err != nil {
		t.Fatal(err)
	}
}
