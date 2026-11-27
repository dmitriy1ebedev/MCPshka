package mcpserver

import (
	"context"
	"sort"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/distributedci"
	"mcpshka/internal/statestore"
	"mcpshka/internal/toolstats"
	"mcpshka/internal/workmetrics"
)

type toolRouteKey struct{}

// withRoute marks how a call arrived: direct (tools/list), tool_invoke or
// the exec_start compatibility bridge used by clients with stale schemas.
func withRoute(ctx context.Context, route string) context.Context {
	if _, ok := ctx.Value(toolRouteKey{}).(string); ok {
		return ctx
	}
	return context.WithValue(ctx, toolRouteKey{}, route)
}

func routeOf(ctx context.Context) string {
	if r, ok := ctx.Value(toolRouteKey{}).(string); ok {
		return r
	}
	return "direct"
}

type ToolUsageInput struct {
	Spans int `json:"spans,omitempty" jsonschema:"Recent payload-free spans to include (0..200, default 20)."`
	Limit int `json:"limit,omitempty" jsonschema:"Tools to list, busiest first (default 60, max 400)."`
}

type ToolUsageResult struct {
	CI              []distributedci.ProviderTiming `json:"ci_providers,omitempty"`
	HTTP            []toolstats.HTTPUsage          `json:"http_routes"`
	WindowStartedAt time.Time                      `json:"window_started_at"`
	Window          string                         `json:"window"`
	Tools           []toolstats.Usage              `json:"tools"`
	Spans           []toolstats.Span               `json:"spans"`
	Never           int                            `json:"never_observed"`
	LedgerSince     time.Time                      `json:"ledger_since,omitempty"`
	NeverEver       int                            `json:"never_called_since_ledger"`
	Note            string                         `json:"note"`
	Overall         toolstats.Overall              `json:"overall"`
	Chains          toolstats.Chains               `json:"chains"` // runs of calls ≤60 s apart over the span ring
	Source          string                         `json:"source"`
	Complete        bool                           `json:"complete"`
	Work            workmetrics.Work               `json:"work"` // P23: missions, stranded leases, stuck UNKNOWN
}

// MCP-065/084/087: process-local telemetry without arguments or results.
func registerToolUsage(r *toolRegistry, db *statestore.DB) {
	closed := false
	addTool(r, &mcp.Tool{
		Name:        "tool_usage",
		Description: "Privacy-safe per-tool telemetry since this server process started, plus lifetime totals (total_calls/total_errors since ledger_since, kept across restarts): calls, errors, compatibility-route calls (tool_invoke / exec bridge), latency buckets and p50/p95/p99, last use, status ACTIVE/RARE/NEVER_OBSERVED, exact success_rate and recovery_rate (failures whose next call succeeded) with their counts, overall totals with the unreliable tools (20+ calls, half failed), recent payload-free spans and work (missions accepted/started, stranded leases, operations stuck pending, each with source and completeness). HTTP routes report request and handler-union time separately (totals and p50/p95 in fractional milliseconds); OAuth is funnel/claude, OpenAI UA or ?route=openai-tunnel selects openai-tunnel, otherwise local. HTTP time ends when the handler returns and excludes the external network; percentiles cover the last 256 requests per route. No arguments or results are recorded; NEVER_OBSERVED in a short window is not evidence that a tool is obsolete.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ToolUsageInput) (*mcp.CallToolResult, ToolUsageResult, error) {
		spans := in.Spans
		if spans <= 0 || spans > 200 {
			spans = 20
		}
		limit := in.Limit
		if limit <= 0 || limit > 400 {
			limit = 60
		}
		view := r.snapshotRegistry()
		names := make([]string, 0, len(view.tools))
		for n := range view.tools {
			names = append(names, n)
		}
		sort.Strings(names)
		started, usage, recent := toolstats.Default.Report(names, -1)
		chains := toolstats.ChainsOf(recent, time.Minute)
		recent = recent[max(0, len(recent)-spans):]
		out := ToolUsageResult{WindowStartedAt: started, Window: time.Since(started).Round(time.Second).String(), Spans: recent,
			LedgerSince: toolstats.Default.LedgerSince(), Chains: chains,
			Note: "calls/status are the window since this start; total_* survive restarts. A tool with total_calls 0 over weeks is a removal candidate (check the catalog lint and guides first)."}
		out.Source, out.Complete = "handler outcome of each MCPshka tool call (tool response), no arguments or results; it does not show whether the owner's task succeeded", !out.LedgerSince.IsZero()
		for _, u := range usage {
			if u.Status == "NEVER_OBSERVED" {
				out.Never++
			}
			if u.TotalCalls == 0 {
				out.NeverEver++
			}
		}
		out.Overall = toolstats.Summarize(usage)
		wctx, cancel := context.WithTimeout(ctx, 4*time.Second)
		out.Work = workmetrics.Compute(wctx, db, time.Now())
		out.Work.Discoverability = toolstats.Default.Discovery(time.Now())
		if r.clients != nil {
			h := r.clients.Health(view.exposedNames(), view.schemas(), view.revision)
			out.Work.Discoverability.SchemaStale = h.ClientRefreshNeeded
		}
		cancel()
		if len(usage) > limit {
			usage = usage[:limit]
		}
		out.HTTP = toolstats.Default.HTTPReport()
		out.CI = distributedci.ReadMetrics()
		out.Tools = usage
		if out.Spans == nil {
			out.Spans = []toolstats.Span{}
		}
		return nil, out, nil
	})
}
