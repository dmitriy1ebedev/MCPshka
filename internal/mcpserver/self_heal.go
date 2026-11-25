package mcpserver

import (
	"context"
	"errors"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/selfheal"
)

type SelfHealStatusInput struct {
	Cursor int `json:"cursor,omitempty" jsonschema:"Container offset; at most four container histories per page."`
}
type SelfHealStatusResult struct {
	selfheal.Status
	NextCursor int  `json:"next_cursor,omitempty"`
	Truncated  bool `json:"truncated"`
}

func selfHealPage(ctx context.Context, s *selfheal.Service, cursor int) (SelfHealStatusResult, error) {
	if cursor < 0 {
		return SelfHealStatusResult{}, errors.New("cursor must be nonnegative")
	}
	st, e := s.Status(ctx)
	if e != nil {
		return SelfHealStatusResult{}, e
	}
	names := make([]string, 0, len(st.Containers))
	for name := range st.Containers {
		names = append(names, name)
	}
	sort.Strings(names)
	page := map[string]selfheal.Container{}
	end := cursor
	for end < len(names) && end-cursor < 4 {
		page[names[end]] = st.Containers[names[end]]
		end++
	}
	st.Containers = page
	return SelfHealStatusResult{Status: st, NextCursor: end, Truncated: end < len(names)}, nil
}
func registerSelfHealTool(r *toolRegistry, s *selfheal.Service) {
	addTool(r, &mcp.Tool{Name: "self_heal_status", Description: "Read-only self-healing configuration, exact container restart allowlist, derived local index-health repairs, failure fingerprints, repair history, hourly limits and latest 128 durable incidents. Four container histories per page; continue with next_cursor when truncated. Escalated repairs stay blocked until observed healthy. Does not poll Docker or trigger repairs.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in SelfHealStatusInput) (*mcp.CallToolResult, SelfHealStatusResult, error) {
		out, e := selfHealPage(ctx, s, in.Cursor)
		return nil, out, e
	})
}
