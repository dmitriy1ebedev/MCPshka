package mcpserver

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/agents"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/providers"
)

type ProvidersStatusResult struct {
	Agents      []agents.ProviderHealth `json:"agents"`
	Providers   []providers.Status      `json:"providers"`
	Unsupported []providers.Policy      `json:"unsupported"`
	// BrokerCircuit is the root-broker transport breaker (MCP-070): OPEN
	// means privileged calls fail fast with BROKER_CIRCUIT_OPEN.
	BrokerCircuit brokerclient.BreakerState `json:"broker_circuit"`
}

var providersSnapshot = providers.Snapshot
var agentProviderHealth = agents.Health

func registerProvidersTool(r *toolRegistry) {
	closed := false
	addTool(r, &mcp.Tool{
		Name:        "providers_status",
		Description: "Build/CI sites (GitHub, GitVerse, MosHub, GigaIDE, VPS, H96): role, free quota, the owner's rule (paid usage forbidden everywhere) and six separately observed readiness signals (network_reachable, configured, authenticated, authorized, api_verified, workflow_ready; HTTP 200 is never CONNECTED), unsupported integrations and the root-broker circuit state. Read before choosing where to test or build.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, ProvidersStatusResult, error) {
		ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		return nil, ProvidersStatusResult{Providers: providersSnapshot(ctx), Agents: agentProviderHealth(ctx, time.Now().UTC()), Unsupported: providers.Unsupported, BrokerCircuit: brokerclient.Breaker()}, nil
	})
}
