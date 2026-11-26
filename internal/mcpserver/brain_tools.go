package mcpserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"mcpshka/internal/brain"
	"mcpshka/internal/llmgateway"
	"mcpshka/internal/tools/braintools"
)

func registerBrain(r *toolRegistry, stateDir string) {
	runner := &brain.Runner{
		Dir: filepath.Join(stateDir, "brain"),
		LLM: func(model string) brain.LLM {
			return func(ctx context.Context, msgs []llmgateway.Message, funcs []llmgateway.Function) (llmgateway.Turn, error) {
				return llmgateway.Shared.Converse(ctx, "brain", msgs, funcs, model)
			}
		},
		Call: func(ctx context.Context, name string, args map[string]any) (string, error) {
			res, err := r.invoke(ctx, ToolInvokeInput{Name: name, Arguments: args})
			if err != nil {
				return "", err
			}
			if res.Output != nil {
				raw, _ := json.Marshal(res.Output)
				return string(raw), nil
			}
			return strings.Join(res.Content, "\n"), nil
		},
	}
	braintools.Register(r.install, runner)
	if stateDir != "" {
		runner.Resume()
	}
}
