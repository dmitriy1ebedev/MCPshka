package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mcpshka/internal/readiness"
	"mcpshka/internal/tools/approvedtask"
	"mcpshka/internal/tools/hatools"
	"mcpshka/internal/tools/opencoretools"
	"mcpshka/internal/tools/pluginops"
	"mcpshka/internal/tools/webosops"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/catalogdigest"
	"mcpshka/internal/tooldef"
	"mcpshka/internal/tools/capabilities"
	"mcpshka/internal/toolstats"
)

const capabilityInstructions = capabilities.Instructions

type CapabilityBootstrap = capabilities.CapabilityBootstrap
type ToolResolveInput = capabilities.ToolResolveInput
type CapabilityCandidate = capabilities.CapabilityCandidate
type ToolResolveResult = capabilities.ToolResolveResult

var toolHistory = toolstats.Default.History

func capabilitySchema[T any](provided any, input bool) any { return tooldef.Schema[T](provided, input) }

func captureCapability[In, Out any](r *toolRegistry, t *mcp.Tool) {
	r.catalogMu.Lock()
	defer r.catalogMu.Unlock()
	captureCapabilityLocked[In, Out](r, t)
	if r.catalogRevision != "" {
		r.catalogDirty = true
	}
}

func captureCapabilityLocked[In, Out any](r *toolRegistry, t *mcp.Tool) {
	if r.catalog == nil {
		r.catalog = map[string]*mcp.Tool{}
	}
	tt := *t
	tt.InputSchema = capabilitySchema[In](t.InputSchema, true)
	tt.OutputSchema = capabilitySchema[Out](t.OutputSchema, false)
	r.catalog[t.Name] = &tt
}

func capabilityHash(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Errorf("capability metadata: %w", err))
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

var capabilityBuild = sync.OnceValues(func() (string, error) {
	// /proc/self/exe pins this running image even while the installed pathname changes.
	f, err := os.Open("/proc/self/exe")
	if err != nil {
		return "", err
	}
	defer f.Close()
	const max = 128 << 20
	h := sha256.New()
	n, err := io.CopyBuffer(h, io.LimitReader(f, max+1), make([]byte, 64<<10))
	if err != nil {
		return "", err
	}
	if n > max {
		return "", errors.New("running image exceeds capability identity budget")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
})

func (r *toolRegistry) capabilityRevision(ctx context.Context) (string, string, error) {
	return r.capabilityRevisionFor(ctx, r.snapshotRegistry().revision)
}

func (r *toolRegistry) capabilityRevisionFor(ctx context.Context, catalogRevision string) (string, string, error) {
	build, err := capabilityBuild()
	if err != nil {
		return "", "", err
	}
	role := "trusted_in_process"
	var scopes []string
	if id, ok := authn.IdentityFromContext(ctx); ok {
		role = strings.ToLower(strings.TrimSpace(id.Role))
		if role == "" {
			role = "operator"
		}
		scopes = slices.Clone(id.Scopes)
		slices.Sort(scopes)
		scopes = slices.Compact(scopes)
	}
	return capabilityHash(struct {
		Catalog, Policy, Build, Version, Role string
		Scopes                                []string
		Broker                                bool
		UID                                   int
	}{catalogRevision, r.policyRevision, build, Version, role, scopes, brokerclient.Enabled(), os.Geteuid()}), build, nil
}

func (r *toolRegistry) capabilityAvailable(ctx context.Context, t *mcp.Tool) bool {
	return r.capabilityDenial(ctx, t) == nil
}

func (r *toolRegistry) capabilityNames() []string {
	return r.snapshotRegistry().names()
}

func registerCapabilityTools(r *toolRegistry, policy any) {
	r.policyRevision = capabilityHash(policy)
	addTool(r, &mcp.Tool{Name: "capabilities_bootstrap", Description: "Compact authorized MCPshka server capability revision and categories. Does not prove client exposure or backend readiness. Start discovery here.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, _ EmptyInput) (*mcp.CallToolResult, CapabilityBootstrap, error) {
		view := r.snapshotRegistry()
		revision, build, err := r.capabilityRevisionFor(ctx, view.revision)
		if err != nil {
			return nil, CapabilityBootstrap{}, err
		}
		out := CapabilityBootstrap{ServerVersion: Version, BuildSHA256: build, Revision: revision, RegisteredCount: len(view.catalog), Categories: map[string]int{}, ClientExposed: "unknown", BackendHealthy: "unknown", ProviderAuthenticated: "unknown", Instructions: capabilityInstructions, CapturedAt: time.Now().UTC().Format(time.RFC3339)}
		out.CatalogSHA256 = catalogdigest.Hash(view.catalog)
		out.Readiness = map[string]int{}
		var observed []CapabilityReadiness
		for _, name := range view.names() {
			t := view.catalog[name]
			if r.capabilityAvailable(ctx, t) {
				out.AuthorizedCount++
				category, _, _ := strings.Cut(t.Name, "_")
				out.Categories[category]++
				c := r.cachedReadiness(t.Name)
				out.Readiness[c.State]++
				observed = append(observed, c)
			}
		}
		inventory, err := r.deniedInventorySnapshot(ctx, CapabilityInventoryInput{Limit: 4}, view)
		if err != nil {
			return nil, out, err
		}
		out.DeniedCount, out.Denied = inventory.DeniedCount, inventory.Denied
		if out.DeniedCount > 0 {
			out.DeniedInventoryTool = "capability_inventory"
		}
		out.BackendHealthy = bootstrapBackendHealthy(out.Readiness)
		out.SetBootstrapReadiness(observed)
		if r.clients != nil && req != nil {
			out.ClientSnapshot = r.clients.Hint(req.Session, view.exposedNames(), view.schemas())
		}
		return nil, out, nil
	})
	addTool(r, &mcp.Tool{Name: "tool_resolve", Description: "Recommend up to three authorized existing native MCPshka tools for a short RU/EN goal; return exact schemas and constraints without executing actions. Optional expected_revision detects stale clients.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in ToolResolveInput) (*mcp.CallToolResult, ToolResolveResult, error) {
		if in.CatalogDelta {
			view := r.snapshotRegistry()
			catalog := map[string]*mcp.Tool{}
			aliases := map[string]string{}
			for name, tool := range view.catalog {
				if r.capabilityAvailable(ctx, tool) {
					catalog[name] = tool
					aliases[name] = capabilityAliases[name] + " " + toolAliasesRU[name]
				}
			}
			if in.Limit == 0 {
				in.Limit = 16
			}
			delta, err := catalogdigest.CatalogDelta(catalog, aliases, in.KnownTools, in.Cursor, in.Limit, time.Now(), in.CatalogSHA256)
			return nil, ToolResolveResult{Code: "CATALOG_DELTA", Catalog: &delta}, err
		}
		out, err := r.resolveCapabilities(ctx, in)
		if err == nil {
			names := []string{}
			for _, c := range out.Candidates {
				names = append(names, c.Name)
			}
			toolstats.Default.Resolution(names, time.Now())
		}
		return nil, out, err
	})
	registerCapabilityInventory(r)
	registerCapabilityReadiness(r)
	registerHostDiagnose(r)
	registerApprovedTask(r)
	registerAcceptance(r)
	registerMediaStream(r)
	webosops.Register(r.install, webosops.Deps{ApprovedCall: approvedtask.Call, RecordGate: recordGate})
	pluginops.Register(r.install, pluginops.Deps{Hub: pluginHub, RequireAdmin: requireActiveAdmin})
	registerWebOSTest(r)
	hatools.Register(r.install, requireActiveAdmin)
	opencoretools.Register(r.install, r.store, requireActiveAdmin)
	registerToolForge(r, defaultAuditPath())
	registerAdminCoverage(r)
	registerToolInvoke(r)
	registerCodeRun(r)
	registerGatewayTools(r)
	r.catalogMu.Lock()
	r.catalogDirty = true // hashed on first snapshot, not on every server build
	r.catalogMu.Unlock()
}

var capabilityAliases = capabilities.Aliases
var hostEnrollmentGoal = capabilities.HostEnrollmentGoal
var workflowRouteForGoal = capabilities.WorkflowRouteForGoal
var firstSentence = capabilities.FirstSentence
var capabilityWords = capabilities.Words
var capabilityScore = capabilities.Score
var sameStem = capabilities.SameStem
var changeGoal = capabilities.ChangeGoal
var allReadOnly = capabilities.AllReadOnly

func (r *toolRegistry) resolveCapabilities(ctx context.Context, in ToolResolveInput) (ToolResolveResult, error) {
	return capabilities.Resolve(ctx, capabilities.Deps{
		Snapshot: func() capabilities.Snapshot {
			v := r.snapshotRegistry()
			return capabilities.Snapshot{Catalog: v.catalog, ReadOnly: v.tools, Contracts: v.contracts, Names: v.names(), Revision: v.revision}
		},
		Revision: r.capabilityRevisionFor, Denial: r.capabilityDenial, Available: r.capabilityAvailable, Readiness: r.cachedReadiness,
		Superseded:      func(name string) bool { _, ok := supersededTools[name]; return ok },
		CategoryAliases: categoryAliases, History: func(name string) (int, int) { return toolHistory(name) }, InvokeHint: toolInvokeCompatHint,
	}, in)
}

// categoryAliases gives every tool the Russian words of its guide category,
// so a Russian goal reaches tools that have no alias of their own.
func categoryAliases(name string) string {
	for _, c := range guideCategories {
		for _, p := range strings.Fields(c.prefixes) {
			if strings.HasPrefix(name, p) {
				return c.name + " " + c.words
			}
		}
	}
	return ""
}

// bootstrapBackendHealthy is true only when every authorized capability with
// declared dependencies is ready, false when any is degraded or blocked.
func bootstrapBackendHealthy(counts map[string]int) string {
	switch {
	case counts[readiness.Degraded]+counts[readiness.Blocked] > 0:
		return "false"
	case counts[readiness.Preflight] > 0 || counts[readiness.Ready] == 0:
		return "unknown"
	}
	return "true"
}
