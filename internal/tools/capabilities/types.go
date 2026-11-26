package capabilities

import (
	"mcpshka/internal/agentguide"
	"mcpshka/internal/catalogdigest"
	"mcpshka/internal/failure"
	"mcpshka/internal/toolcontract"
)

const Instructions = agentguide.DevelopmentInstructions + "\nБольше двух чтений/проверок — одним code_run (mcp.all параллельно). Каталог по умолчанию: 40 инструментов; полный: HTTP ?tools=all или MCPSHKA_TOOL_CATALOG=all. AI agents: read mcpshka_guide topic=rules once per session (Russian operating rules: do not stop for progress reports, take verdicts from phase_gate/host_triage, retry lost replies with the same idempotency_key, wait for tests with job_wait). Start with capabilities_bootstrap for a compact authorized server catalog, then tool_resolve(goal) for exact schemas. These tools do not execute a plan. Server registration does not prove client exposure, provider authentication or approval; ready/backend_healthy come from observed health and self-tests (capability_selftest, capability_preflight before a roadmap step). If a needed tool is missing from this client's tools/list (stale or truncated snapshot, CLIENT_SCHEMA_STALE), call it through tool_invoke(name, arguments) with the schema from tool_resolve; if tool_invoke is missing too, call exec_start command=mcpshka args=[\"tool-invoke\",\"<name>\",\"<arguments JSON>\"] and read the result with job_output (also args [\"guide\",\"rules\"] and [\"resolve\",\"<goal>\"]); refreshing the connector is optional."

type CapabilityBootstrap struct {
	CatalogSHA256       string             `json:"catalog_sha256"`
	DeniedCount         int                `json:"denied_count"`
	Denied              []CapabilityDenied `json:"denied,omitempty"`
	DeniedInventoryTool string             `json:"denied_inventory_tool,omitempty"`
	ServerVersion       string             `json:"server_version"`
	BuildSHA256         string             `json:"build_sha256"`
	Revision            string             `json:"revision"`
	RegisteredCount     int                `json:"registered_count"`
	AuthorizedCount     int                `json:"authorized_count"`
	Categories          map[string]int     `json:"categories"`
	ClientExposed       string             `json:"client_exposed"`
	BackendHealthy      string             `json:"backend_healthy"`
	Ready               bool               `json:"ready"`
	Blocker             string             `json:"blocker,omitempty"`
	NextSafeAction      *failure.Action    `json:"next_safe_action,omitempty"`
	// Readiness counts authorized capabilities per readiness state from
	// existing evidence; capability_selftest refreshes it.
	Readiness             map[string]int `json:"readiness,omitempty"`
	ProviderAuthenticated string         `json:"provider_authenticated"`
	Instructions          string         `json:"instructions"`
	CapturedAt            string         `json:"captured_at"`
	// ClientSnapshot names tools the calling client's own last tools/list lacks.
	ClientSnapshot string `json:"client_snapshot,omitempty"`
}

type ToolResolveInput struct {
	CatalogSHA256    string            `json:"catalog_sha256,omitempty"`
	CatalogDelta     bool              `json:"catalog_delta,omitempty" jsonschema:"Return bounded authorized capability metadata; goal=catalog. known_tools maps IDs to tool_hash; catalog_sha256 pins continuation."`
	KnownTools       map[string]string `json:"known_tools,omitempty"`
	Cursor           int               `json:"cursor,omitempty"`
	Limit            int               `json:"limit,omitempty"`
	Goal             string            `json:"goal" jsonschema:"Tool name or short RU/EN goal; 1..256 UTF-8 bytes. Recommendations only, never execution."`
	ExpectedRevision string            `json:"expected_revision,omitempty" jsonschema:"Optional SHA-256 from capabilities_bootstrap. A mismatch requires client schema refresh."`
	Compact          bool              `json:"compact,omitempty" jsonschema:"true: only name, effect, input schema, mutation contract and a one-sentence description (no output schema) — for clients that must save tokens."`
	Format           string            `json:"format,omitempty" jsonschema:"ts: each candidate carries a TypeScript declaration of its arguments (field ts) instead of JSON Schemas — about a third fewer tokens, enough to write the call."`
}

type CapabilityCandidate struct {
	Name                  string                 `json:"name"`
	Description           string                 `json:"description,omitempty"`
	InputSchema           any                    `json:"input_schema,omitempty"`
	TS                    string                 `json:"ts,omitempty"`
	OutputSchema          any                    `json:"output_schema,omitempty"`
	Confidence            string                 `json:"confidence"`
	Why                   string                 `json:"why,omitempty"`
	Effect                string                 `json:"effect"`
	RequiredPermission    string                 `json:"required_permission"`
	PermissionGranted     bool                   `json:"permission_granted"`
	ServerCapability      string                 `json:"server_capability,omitempty"`
	ClientExposed         string                 `json:"client_exposed,omitempty"`
	ProviderAuthenticated string                 `json:"provider_authenticated,omitempty"`
	BackendHealthy        string                 `json:"backend_healthy,omitempty"`
	Ready                 string                 `json:"ready,omitempty"`
	Constraints           string                 `json:"constraints,omitempty"`
	Mutation              *toolcontract.Contract `json:"mutation,omitempty"`
	FailureHistory        string                 `json:"failure_history,omitempty"` // failed in half of 20+ calls: a fact for the planner, never a ban
}

// toolHistory is the lifetime calls/errors of a tool (swapped in tests).
type ToolResolveResult struct {
	Catalog            *catalogdigest.Delta  `json:"catalog,omitempty"`
	Denied             []CapabilityDenied    `json:"denied,omitempty"`
	Revision           string                `json:"revision"`
	Code               string                `json:"code"`
	Candidates         []CapabilityCandidate `json:"candidates"`
	EstimatedToolCalls int                   `json:"estimated_tool_calls"`
	Instructions       string                `json:"instructions,omitempty"`
	// CallVia tells a client with a stale tools/list how to reach a candidate.
	CallVia string `json:"call_via,omitempty"`
}

type CapabilityDenied struct {
	Name               string `json:"name"`
	ReasonCode         string `json:"reason_code"`
	RequiredPermission string `json:"required_permission"`
	NextSafeAction     string `json:"next_safe_action"`
}
