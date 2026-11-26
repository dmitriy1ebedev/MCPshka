package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/failure"
	"mcpshka/internal/readiness"
	"mcpshka/internal/toolforge"
	"mcpshka/internal/tools/forgeops"
)

type ToolForgeInput = forgeops.ToolForgeInput
type ToolForgeResult = forgeops.ToolForgeResult

var defaultAuditPath = forgeops.DefaultAuditPath
var auditCalls = forgeops.AuditCalls

// delegatingTools have no mutation of their own: every effect goes through a
// tool that carries its own mutation contract, retry key and receipt.
var delegatingTools = map[string]string{
	"integration_ship": "each step calls one integration_* tool under that tool's contract; retry keys derive from commit and step",
	"tool_invoke":      "dispatches to the named tool's own wrapped handler, contract and receipt",
}

func (r *toolRegistry) forgeCatalog(auditPath string) map[string]toolforge.Tool {
	calls := auditCalls(auditPath)
	view := r.snapshotRegistry()
	out := map[string]toolforge.Tool{}
	for name, t := range view.catalog {
		ro := view.tools[name]
		_, contract := view.contracts[name]
		// A dispatcher performs no effect of its own: each step runs under the
		// called tool's contract (ChatGPT audit 03.11.2026 counted these two).
		contract = contract || delegatingTools[name] != ""
		ready := readiness.Declared(name)
		n := calls[name]
		// Reads and mutations without a contract leave no audit record:
		// their usage is unknown, not zero.
		if ro || (!contract && n == 0) {
			n = -1
		}
		out[name] = toolforge.Tool{Name: name, Description: t.Description, ReadOnly: ro, HasContract: contract, HasReady: ready, Calls: n}
	}
	return out
}

// compositeInput retains dynamic arguments and the statically checked retry key.
type compositeInput struct {
	IdempotencyKey string         `json:"idempotency_key"`
	Arguments      map[string]any `json:"-"`
}

func (in *compositeInput) UnmarshalJSON(raw []byte) error {
	type plain compositeInput
	if err := json.Unmarshal(raw, (*plain)(in)); err != nil {
		return err
	}
	return json.Unmarshal(raw, &in.Arguments)
}

func (in compositeInput) MarshalJSON() ([]byte, error) {
	return json.Marshal(in.Arguments)
}

// registerComposite exposes an approved chain as a tool; every step runs
// through the step tool's own wrapped handler, so its gates still apply.
func (r *toolRegistry) registerComposite(c toolforge.Composite) {
	r.compositeMu.Lock()
	defer r.compositeMu.Unlock()
	if r.lookupTool(c.Name).tool != nil {
		return
	}
	props := map[string]any{"idempotency_key": map[string]any{"type": "string"}}
	for _, in := range c.Inputs {
		props[in] = map[string]any{}
	}
	schema := map[string]any{"type": "object", "properties": props}
	if !c.ReadOnly {
		schema["required"] = []string{"idempotency_key"}
	}
	tool := &mcp.Tool{Name: c.Name, Description: "[composite] " + c.Description, InputSchema: schema, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: c.ReadOnly}}
	addTool(r, tool, func(ctx context.Context, req *mcp.CallToolRequest, input compositeInput) (*mcp.CallToolResult, map[string]any, error) {
		in := input.Arguments
		var outputs []map[string]any
		hasEffect := false
		for i, s := range c.Steps {
			invoke := r.lookupTool(s.Tool).invoke
			if invoke == nil {
				return nil, nil, fmt.Errorf("step %d: %s is no longer registered", i, s.Tool)
			}
			args := toolforge.Expand(s.Args, in, outputs)
			if key, ok := in["idempotency_key"].(string); ok && key != "" {
				sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%s", c.Name, i, key)))
				args["idempotency_key"] = hex.EncodeToString(sum[:16])
			}
			res, out, err := invoke(ctx, args)
			if err != nil {
				if hasEffect {
					held := failure.NewHold(failure.FencedUnknown, "COMPOSITE_PARTIAL_EFFECT", "tool_forge", fmt.Sprintf("step %d (%s) failed after a completed mutation; reconcile the original chain", i, s.Tool), err)
					held.NextSafeAction = failure.Action{Tool: "gateway_reconcile", Arguments: map[string]any{"tool": c.Name, "key": in["idempotency_key"]}}
					err = held
				}
				return nil, map[string]any{"failed_step": i, "outputs": outputs}, fmt.Errorf("step %d (%s): %w", i, s.Tool, err)
			}
			if res != nil && res.IsError {
				return res, map[string]any{"failed_step": i, "failed_output": out, "outputs": outputs}, nil
			}
			hasEffect = hasEffect || !r.lookupTool(s.Tool).readOnly
			m, _ := object(out)
			outputs = append(outputs, m)
		}
		return nil, map[string]any{"steps": outputs}, nil
	})
}

func registerToolForge(r *toolRegistry, auditPath string) {
	forgeops.Register(r.install, forgeops.Deps{Catalog: r.forgeCatalog, RegisterComposite: r.registerComposite, Superseded: supersededTools, Hidden: hiddenTool, Readiness: func(n string) string { return r.cachedReadiness(n).State }, RequireAdmin: requireActiveAdmin, Call: r.call}, auditPath)
}
