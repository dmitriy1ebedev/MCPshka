package tooldef

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolInvoker runs one registered tool through its complete wrapper:
// scope check, admission gate, retry key, receipts and probes. tool_invoke
// adds nothing and skips nothing, so it never widens what a caller may do.

type invocationRequestKey struct{}

func InvocationContext(ctx context.Context, req *mcp.CallToolRequest) context.Context {
	if req == nil {
		return ctx
	}
	return context.WithValue(ctx, invocationRequestKey{}, req)
}

func InvocationRequest(ctx context.Context) *mcp.CallToolRequest {
	req, _ := ctx.Value(invocationRequestKey{}).(*mcp.CallToolRequest)
	return req
}

func invokerFor[In, Out any](wrapped func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, Out, error), tool *mcp.Tool) Invoker {
	name := tool.Name
	resolved := resolvedInput[In](tool)
	return func(ctx context.Context, args map[string]any) (*mcp.CallToolResult, any, error) {
		if args == nil {
			args = map[string]any{}
		}
		// Match SDK defaults and validation on a private JSON value.
		raw, err := json.Marshal(args)
		if err != nil {
			return nil, nil, err
		}
		var normalized any
		if err := json.Unmarshal(raw, &normalized); err != nil {
			return nil, nil, err
		}
		if err := resolved.ApplyDefaults(&normalized); err != nil {
			return nil, nil, fmt.Errorf("applying tool schema defaults: %w", err)
		}
		if err := resolved.Validate(&normalized); err != nil {
			return nil, nil, fmt.Errorf("arguments do not match the tool schema: %w; %s", err, SchemaHint(resolved.Schema(), args))
		}
		raw, err = json.Marshal(normalized)
		if err != nil {
			return nil, nil, err
		}
		var in In
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			return nil, nil, fmt.Errorf("arguments do not match the tool schema: %w; %s", err, SchemaHint(resolved.Schema(), args))
		}
		request := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: name, Arguments: raw}}
		if original, ok := ctx.Value(invocationRequestKey{}).(*mcp.CallToolRequest); ok && original != nil {
			params := request.Params
			*request = *original
			request.Params = params
			if original.Params != nil {
				params := *original.Params
				params.Name, params.Arguments = name, raw
				request.Params = &params
			}
		}
		return wrapped(ctx, request, in)
	}
}

// resolvedInputs caches resolved input schemas inferred from a Go type: they
// are immutable once resolved, and re-resolving them for every tool on every
// server build cost a third of New() (0.18.0 sealed test).
var resolvedInputs sync.Map // reflect.Type -> *jsonschema.Resolved

func resolvedInput[In any](tool *mcp.Tool) *jsonschema.Resolved {
	if tool.InputSchema == nil {
		if r, ok := resolvedInputs.Load(reflect.TypeFor[In]()); ok {
			return r.(*jsonschema.Resolved)
		}
	}
	rawSchema, err := json.Marshal(Schema[In](tool.InputSchema, true))
	if err != nil {
		panic(fmt.Errorf("tool %s input schema: %w", tool.Name, err))
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(rawSchema, &schema); err != nil {
		panic(fmt.Errorf("tool %s input schema: %w", tool.Name, err))
	}
	resolved, err := schema.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
	if err != nil {
		panic(fmt.Errorf("tool %s input schema: %w", tool.Name, err))
	}
	if tool.InputSchema == nil {
		resolvedInputs.Store(reflect.TypeFor[In](), resolved)
	}
	return resolved
}
