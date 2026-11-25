package mcpserver

import (
	"context"
	"maps"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/failure"
)

// The SDK retains the original server error even though the wire carries text.
func failureMetadata(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
		result, err := next(ctx, method, request)
		if method != "tools/call" || err != nil {
			return result, err
		}
		out, ok := result.(*mcp.CallToolResult)
		if !ok || out == nil {
			return result, err
		}
		proof := failure.ForWire(out.GetError())
		if proof == nil && out.Meta[failure.MetadataKey] == nil {
			return result, err
		}
		copy := *out
		copy.Meta = maps.Clone(out.Meta)
		delete(copy.Meta, failure.MetadataKey)
		if out.IsError && proof != nil {
			if copy.Meta == nil {
				copy.Meta = mcp.Meta{}
			}
			copy.Meta[failure.MetadataKey] = proof
		}
		return &copy, nil
	}
}
