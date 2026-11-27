// Package tooldef erases handler types once while retaining SDK schema and receipt semantics.
package tooldef

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/toolcontract"
)

type Handler func(context.Context, *mcp.CallToolRequest, any) (*mcp.CallToolResult, any, error)
type Invoker func(context.Context, map[string]any) (*mcp.CallToolResult, any, error)

type Def struct {
	Tool          *mcp.Tool
	InputType     reflect.Type
	Handler       Handler
	Decode        func(map[string]any) (any, error)
	Catalog       func() *mcp.Tool
	DefaultOutput func() any
	Install       func(*mcp.Server, Handler)
	Invoker       func(Handler) Invoker
	ReadReceipt   func(*idempotency.Store, string, string, string) (bool, string, any, idempotency.State, error)
	Zero          any
}

func New[In, Out any](tool *mcp.Tool, handler func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, Out, error)) Def {
	var zero Out
	return Def{
		Tool: tool, InputType: reflect.TypeFor[In](), Zero: zero,
		Handler: func(ctx context.Context, req *mcp.CallToolRequest, in any) (*mcp.CallToolResult, any, error) {
			var input In
			if in != nil {
				input = in.(In)
			}
			return handler(ctx, req, input)
		},
		Decode: func(input map[string]any) (any, error) {
			var in In
			raw, err := json.Marshal(input)
			if err != nil {
				return nil, err
			}
			if err := json.Unmarshal(raw, &in); err != nil {
				return nil, err
			}
			return in, nil
		},
		Catalog: func() *mcp.Tool {
			t := *tool
			t.InputSchema = Schema[In](tool.InputSchema, true)
			t.OutputSchema = Schema[Out](tool.OutputSchema, false)
			return &t
		},
		DefaultOutput: func() any { return defaultOutput[Out]() },
		Install:       func(server *mcp.Server, h Handler) { mcp.AddTool(server, tool, typed[In, Out](h)) },
		Invoker:       func(h Handler) Invoker { return invokerFor(typed[In, Out](h), tool) },
		ReadReceipt: func(store *idempotency.Store, key, operation, fingerprint string) (bool, string, any, idempotency.State, error) {
			var receipt toolcontract.Receipt[Out]
			state, err := store.ReadResult(key, operation, fingerprint, &receipt)
			return receipt.IsError, receipt.ErrorText, receipt.Output, state, err
		},
	}
}

func typed[In, Out any](h Handler) func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, Out, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		result, value, err := h(ctx, req, in)
		out, _ := value.(Out)
		return result, out, err
	}
}

// defaultOutputs keeps one inferred output schema per Go type. A stable
// pointer lets the SDK SchemaCache reuse its resolution; a fresh schema on
// every server build was re-resolved each time (0.85 s of New() on H96).
var defaultOutputs sync.Map // reflect.Type -> *jsonschema.Schema (nil: none)

func defaultOutput[Out any]() any {
	t := reflect.TypeFor[Out]()
	if v, ok := defaultOutputs.Load(t); ok {
		if s := v.(*jsonschema.Schema); s != nil {
			return s
		}
		return nil
	}
	var out *jsonschema.Schema
	if s, err := jsonschema.For[Out](&jsonschema.ForOptions{TypeSchemas: RawJSONSchemas}); err == nil && s.Type == "object" {
		out = s
	}
	v, _ := defaultOutputs.LoadOrStore(t, out)
	if s := v.(*jsonschema.Schema); s != nil {
		return s
	}
	return nil
}
