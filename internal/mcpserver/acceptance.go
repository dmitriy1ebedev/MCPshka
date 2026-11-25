package mcpserver

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/evidence"
	"mcpshka/internal/gitops"
)

type AcceptanceInput struct {
	RequiredGates []string `json:"required_gates,omitempty" jsonschema:"Additional gates to require, unioned with ROADMAP criteria; cannot remove or waive existing gates."`
	Path          string   `json:"path" jsonschema:"repository root under /root/repos"`
	Step          string   `json:"step" jsonschema:"ROADMAP step id or heading text, e.g. P12 or T1-GATE"`
	Version       string   `json:"version,omitempty" jsonschema:"expected version; deploy/release evidence for another version is stale"`
}

type AcceptanceResult struct {
	Step string `json:"step"`
	Done bool   `json:"done"`
	// Verdict: DONE, FAILED, NOT_RELEASED (release/delivery missing) or BLOCKED.
	Verdict string               `json:"verdict"`
	Gates   []evidence.GateState `json:"gates"`
	Inputs  evidence.Inputs      `json:"inputs"`
	Note    string               `json:"note,omitempty"`
}

func gitRev(ctx context.Context, dir, rev string) string {
	out, _ := gitops.HeadIdentity(dir, rev, func() (string, error) {
		raw, err := exec.CommandContext(ctx, "git", "-c", "safe.directory=*", "-C", dir, "rev-parse", rev).Output()
		return strings.TrimSpace(string(raw)), err
	})
	return out
}

func registerAcceptance(r *toolRegistry) {
	addTool(r, &mcp.Tool{Name: "acceptance_status", Description: "Strict acceptance of one ROADMAP step: reads its machine criteria line «Gates: build, docker, device, …» (engineering, build, packaging, native, docker, device, release, deploy, acceptance, browser = browser/gameplay smoke, delivery = artifact delivered with SHA) and checks each gate's recorded evidence against the current HEAD, tree and version. Evidence of changed inputs is STALE; done only when every gate is PASS; verdict DONE/FAILED/NOT_RELEASED/BLOCKED. phase_gate PASS alone is not DONE. Read-only.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in AcceptanceInput) (*mcp.CallToolResult, AcceptanceResult, error) {
		out := AcceptanceResult{Step: in.Step}
		root := filepath.Clean(in.Path)
		gates, err := evidence.RequiredGates(filepath.Join(root, "ROADMAP.md"), in.Step)
		if err != nil {
			return nil, out, err
		}
		if len(gates) == 0 {
			gates, out.Note = []string{"engineering", "build"}, "step has no «Gates:» line; defaulting to engineering, build — add machine criteria under the step"
		}
		if len(in.RequiredGates) > len(evidence.Gates) {
			return nil, out, fmt.Errorf("too many required gates")
		}
		for _, gate := range in.RequiredGates {
			if !slices.Contains(evidence.Gates, gate) {
				return nil, out, fmt.Errorf("unknown required gate %q", gate)
			}
			if !slices.Contains(gates, gate) {
				gates = append(gates, gate)
			}
		}
		out.Inputs = evidence.Inputs{Head: gitRev(ctx, root, "HEAD"), Tree: gitRev(ctx, root, "HEAD^{tree}"), Version: in.Version}
		out.Gates, out.Done = evidence.Evaluate(root, gates, out.Inputs)
		out.Verdict = evidence.Verdict(out.Gates)
		return nil, out, nil
	})
}

// recordGate stores evidence a tool has just observed; failures to record
// never fail the tool itself.
func recordGate(project, gate string, pass bool, in evidence.Inputs, source, ref string) {
	result := "FAIL"
	if pass {
		result = "PASS"
	}
	_ = evidence.Put(evidence.Record{Project: filepath.Clean(project), Gate: gate, Result: result, Inputs: in, Source: source, Ref: ref})
}
