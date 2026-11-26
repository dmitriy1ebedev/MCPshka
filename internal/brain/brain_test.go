package brain

import (
	"context"
	"strings"
	"testing"

	"mcpshka/internal/llmgateway"
)

func TestRunResolvesInvokesAndAnswers(t *testing.T) {
	script := []llmgateway.Turn{
		{Call: &llmgateway.FunctionCall{Name: "tool_resolve", Arguments: map[string]any{"goal": "здоровье хоста"}}},
		{Call: &llmgateway.FunctionCall{Name: "invoke", Arguments: map[string]any{"name": "host_health", "arguments_json": `{"admin_session_id":"stolen"}`}}},
		{Call: &llmgateway.FunctionCall{Name: "invoke", Arguments: map[string]any{"name": "agent_task", "arguments": map[string]any{}}}},
		{Text: "Хост здоров.", Model: "GigaChat-2-Max"},
	}
	var calls []string
	i := 0
	llm := func(_ context.Context, msgs []llmgateway.Message, funcs []llmgateway.Function) (llmgateway.Turn, error) {
		if i > 0 && msgs[len(msgs)-1].Role != "function" {
			t.Fatalf("step %d: last message %+v", i, msgs[len(msgs)-1])
		}
		i++
		return script[i-1], nil
	}
	call := func(_ context.Context, name string, args map[string]any) (string, error) {
		if _, ok := args["admin_session_id"]; ok {
			t.Fatal("owner session forwarded")
		}
		calls = append(calls, name)
		return `{"ok":true}`, nil
	}
	out, err := Run(context.Background(), llm, call, "как хост?", 5)
	if err != nil || out.Answer != "Хост здоров." || !out.Done || len(out.Steps) != 3 {
		t.Fatalf("%+v %v", out, err)
	}
	if strings.Join(calls, ",") != "tool_resolve,host_health" || !strings.Contains(out.Steps[2].Error, "REFUSED") {
		t.Fatalf("calls %v steps %+v", calls, out.Steps)
	}
}

func TestRunStopsAtStepBudget(t *testing.T) {
	llm := func(_ context.Context, _ []llmgateway.Message, funcs []llmgateway.Function) (llmgateway.Turn, error) {
		if funcs == nil {
			return llmgateway.Turn{Text: "итог"}, nil
		}
		return llmgateway.Turn{Call: &llmgateway.FunctionCall{Name: "memory_recall", Arguments: map[string]any{"text": "x"}}}, nil
	}
	out, err := Run(context.Background(), llm, func(context.Context, string, map[string]any) (string, error) { return strings.Repeat("я", 9000), nil }, "задача", 2)
	if err != nil || out.Answer != "итог" || len(out.Steps) != 2 || len(out.Steps[0].Result) > maxTrace+40 {
		t.Fatalf("%+v %v", out, err)
	}
}
