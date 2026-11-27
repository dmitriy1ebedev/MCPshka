package issuesnap

import (
	"context"
	"time"
)

// With binds Build to a tool registry: lookup returns a read-only tool by
// name or nil. The result is what issue_create appends to a new card.
func With(version string, lookup func(string) Reader) func(context.Context, string) string {
	return func(ctx context.Context, project string) string {
		readers := map[string]Reader{}
		for _, name := range []string{"health_snapshot", "gateway_status", "tool_usage", "project_context"} {
			if read := lookup(name); read != nil {
				readers[name] = read
			}
		}
		return Build(ctx, version, project, readers, time.Now())
	}
}
