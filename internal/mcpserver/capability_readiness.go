package mcpserver

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/readiness"
)

type CapabilityDependency = readiness.CapabilityDependency
type CapabilityReadiness = readiness.CapabilityReadiness
type CapabilitySelfTestInput = readiness.CapabilitySelfTestInput
type CapabilitySelfTestResult = readiness.CapabilitySelfTestResult
type CapabilityPreflightInput = readiness.CapabilityPreflightInput
type PreflightStage = readiness.PreflightStage
type CapabilityPreflightResult = readiness.CapabilityPreflightResult

func (r *toolRegistry) toolEffect(name string) string {
	if r.lookupTool(name).readOnly {
		return "read_only"
	}
	return "gated_mutation"
}

func (r *toolRegistry) readinessEngine() readiness.Engine {
	return readiness.Engine{
		Health: r.health, Cache: &r.readiness, Effect: r.toolEffect, CompactError: firstSentence,
		Available: func(ctx context.Context, name string) (bool, bool) {
			t := r.lookupTool(name).tool
			return t != nil, t != nil && r.capabilityAvailable(ctx, t)
		},
	}
}

func (r *toolRegistry) cachedReadiness(name string) CapabilityReadiness {
	e := r.readinessEngine()
	return e.Cached(name)
}

func (r *toolRegistry) preflight(ctx context.Context, in CapabilityPreflightInput) (CapabilityPreflightResult, error) {
	e := r.readinessEngine()
	return e.Preflight(ctx, in)
}

func registerCapabilityReadiness(r *toolRegistry) {
	addTool(r, &mcp.Tool{Name: "capability_selftest", Description: "Check named capabilities now: health sources plus each capability's own readiness probe, executed as the UID that serves it (e.g. docker_smoke writes and removes a probe file in its state directory, checks the worker helper, the transient-unit launcher and dockerd). Returns state registered/preflight/ready/degraded/blocked with contract: effect, privilege, directories, limits, recovery and dependencies. Results are cached 10 min for tool_resolve and capabilities_bootstrap. No repairs.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in CapabilitySelfTestInput) (*mcp.CallToolResult, CapabilitySelfTestResult, error) {
		out := CapabilitySelfTestResult{Capabilities: []CapabilityReadiness{}}
		if err := readiness.ValidNames(in.Names, 16); err != nil {
			return nil, out, err
		}
		var names []string
		for _, n := range in.Names {
			if t := r.lookupTool(n).tool; t != nil && r.capabilityAvailable(ctx, t) {
				names = append(names, n)
			} else {
				out.Unknown = append(out.Unknown, n)
			}
		}
		engine := r.readinessEngine()
		caps, err := engine.Probe(ctx, names)
		if err != nil {
			return nil, out, err
		}
		out.Capabilities = caps
		return nil, out, nil
	})
	addTool(r, &mcp.Tool{Name: "capability_preflight", Description: "Dependency preflight for one roadmap step before implementing it: capabilities registered → rights for this caller → backend dependencies and readiness probes → pending recovery. Verdict ready/degraded/blocked with the first blocker and a read-only next_safe_action to repair it the standard way. Never executes the step itself.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in CapabilityPreflightInput) (*mcp.CallToolResult, CapabilityPreflightResult, error) {
		out, err := r.preflight(ctx, in)
		return nil, out, err
	})
}
