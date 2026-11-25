package mcpserver

import (
	"context"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/healthlayer"
	"mcpshka/internal/heavyqueue"
	"mcpshka/internal/notify"
	"mcpshka/internal/ownerweb"
	"mcpshka/internal/portal"
	"mcpshka/internal/statestore"
)

type OwnerPortalResult struct {
	Events      []notify.Event     `json:"events"`
	EventsState string             `json:"events_state"`
	Indicators  []portal.Indicator `json:"indicators"`
	Links       []portal.Link      `json:"links"`
	CapturedAt  time.Time          `json:"captured_at"`
}

// MCP-072/078: one-screen owner readiness and valid entry links.
func registerOwnerPortal(r *toolRegistry, collector *healthlayer.Collector, db *statestore.DB) {
	closed := false
	addTool(r, &mcp.Tool{
		Name:        "owner_portal",
		Description: "Owner's one-screen readiness for the phone (готовность одним экраном, ссылки портала): up to 9 indicators READY/DEGRADED/BLOCKED/UNKNOWN (tunnel, MCP API, broker, jobs, storage, knowledge, LANE, providers, home Zigbee/MQTT) with the next tool for each non-ready one, plus valid entry links (Funnel/Tailnet, and the «Подключения» settings page for tokens, clouds and agent sign-in) without tokens. Missing evidence stays UNKNOWN. Read-only.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, OwnerPortalResult, error) {
		out := OwnerPortalResult{CapturedAt: time.Now().UTC()}
		var eventErr error
		out.Events, eventErr = notify.Open(ctx, db)
		out.EventsState = "OBSERVED"
		if eventErr != nil {
			out.EventsState = "UNKNOWN"
		}
		var sources []portal.SourceState
		if collector != nil {
			if rep, err := collector.Read(ctx); err == nil {
				for _, it := range rep.Items {
					sources = append(sources, portal.SourceState{Name: it.Name, State: it.State})
				}
				out.CapturedAt = rep.CapturedAt
			}
		}
		out.Indicators = portal.Indicators(sources)
		raw, err := os.ReadFile(heavyqueue.HomeStatusFile)
		out.Indicators = append(out.Indicators, portal.Home(raw, err, time.Now()))
		out.Links = portal.Links(ctx)
		if origin, err := ownerweb.OriginConfigured(); err == nil {
			// «Подключения»: tokens, clouds and agent sign-in from the phone.
			out.Links = append(out.Links, portal.Link{Mode: "PUBLIC", URL: origin + "/connections", Label: "Подключения: токены, облака, вход агентов (Face ID)"})
		}
		return nil, out, nil
	})
}
