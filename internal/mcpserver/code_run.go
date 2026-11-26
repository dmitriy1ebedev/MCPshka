package mcpserver

import (
	"context"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/codemode"
	"mcpshka/internal/llmgateway"
)

type CodeRunInput = codemode.Input
type CodeRunResult = codemode.Result

func registerCodeRun(r *toolRegistry) {
	closed := false
	tool := &mcp.Tool{
		Name:        "code_run",
		Description: "Code Mode: run one short JavaScript that chains READ-ONLY MCPshka tools and returns only the final value — many calls, one round trip, intermediate results stay out of the chat. mcp.call(name, args) → output (throws on error); mcp.all([[name, args], …]) — parallel reads; store.set/get/keys — values kept between code_run calls (2 h), so fetch once and filter later; llm.models() starts a model catalog job; llm.ask(question, data, {model, timeout_seconds, budget_seconds}) starts a job returning {text, model, redactions, redaction_types}; llm.compare(question, data, {count:3}) starts a comparison job; llm.scope(card, project, root) skips other repositories; a result over 64 KiB is saved as store 'spill:N' with a preview. mcp.mutate(name, args) — one mutating tool with a mutation contract, only with its idempotency_key (else POLICY_DENIED), same Face ID/lease/receipts as a direct call; UNKNOWN → repeat with the same key; up to 10 per run. LLM calls return a background job; use job_wait then job_output for its JSON result. Limits: timeout_seconds up to 600 s (default 20; LLM runs in its own bounded job), 50 calls, no network/fs/shell. Schemas: tool_resolve.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}
	addTool(r, tool, func(ctx context.Context, req *mcp.CallToolRequest, in CodeRunInput) (*mcp.CallToolResult, CodeRunResult, error) {
		out, err := r.codeRun(invocationContext(ctx, req), in)
		return &mcp.CallToolResult{IsError: out.Error != ""}, out, err
	})
}

func (r *toolRegistry) codeRun(ctx context.Context, in CodeRunInput) (CodeRunResult, error) {
	if in.Async {
		in.Async = false
		seconds := min(max(in.TimeoutSeconds, 1), 600) + 30
		job, err := r.receipts.StartCode(ctx, seconds, func(c context.Context) (any, error) { return r.codeRun(c, in) })
		return CodeRunResult{Calls: []codemode.Call{}, Result: map[string]any{"job": job.ID, "next": "job_wait, then job_output"}}, err
	}
	return codemode.Run(ctx, in, codemode.Host{
		Store:    codemode.Shared,
		LLM:      llmgateway.Shared,
		AsyncLLM: r.receipts.AsyncLLM,
		Allowed: func(name string) bool {
			if name == "code_run" || name == "tool_invoke" {
				return false
			}
			r.catalogMu.RLock()
			defer r.catalogMu.RUnlock()
			t := r.catalog[name]
			return t != nil && t.Annotations != nil && t.Annotations.ReadOnlyHint
		},
		Invoke: func(ctx context.Context, name string, args map[string]any) (any, error) {
			res, err := r.invoke(withRoute(ctx, "code"), ToolInvokeInput{Name: name, Arguments: args})
			if err == nil && res.IsError {
				err = errors.New(strings.Join(res.Content, " "))
			}
			return res.Output, err
		},
	})
}
