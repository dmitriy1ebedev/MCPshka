package codemode

import (
	"context"
	"testing"
)

// 26.11 audit (iPhone chat): «return await mcp.call(…)» was a SyntaxError.
func TestAwaitInScript(t *testing.T) {
	host := Host{Allowed: func(string) bool { return true },
		Invoke: func(context.Context, string, map[string]any) (any, error) {
			return map[string]any{"branch": "main"}, nil
		}}
	out, err := Run(context.Background(), Input{Script: "const s = await mcp.call('git_status', {}); return s.branch"}, host)
	if err != nil || out.Error != "" || out.Result != "main" {
		t.Fatalf("%+v %v", out, err)
	}
	out, _ = Run(context.Background(), Input{Script: "await null; throw new Error('boom')"}, host)
	if out.Error == "" {
		t.Fatal("a rejected async script must report its error")
	}
}
