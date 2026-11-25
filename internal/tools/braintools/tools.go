// Package braintools exposes the LLM brain (internal/brain): brain_ask
// for a short task in the call, background runs for long work.
package braintools

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/brain"
	"mcpshka/internal/tooldef"
)

// AskInput is one task for the brain.
type AskInput struct {
	Task           string `json:"task" jsonschema:"Задача своими словами, как владелец сказал бы её в чате."`
	Model          string `json:"model,omitempty" jsonschema:"Модель любого настроенного ИИ из llm.json (GigaChat-2-Max, модель FreeLLM, OpenAI-совместимый Claude/GPT); пусто — brain_model."`
	Background     bool   `json:"background,omitempty" jsonschema:"true — фоновая задача: сразу вернуть id, ход смотреть brain_status; переживает перезапуск сервера."`
	MaxSteps       int    `json:"max_steps,omitempty" jsonschema:"Сколько вызовов инструментов можно сделать: 1..30 в вызове, до 200 в фоне (по умолчанию 12)."`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"Ключ повтора: тот же ключ не запускает задачу дважды."`
}

// AskResult is an answer (in the call) or a background run's summary.
type AskResult struct {
	brain.Result
	Run *brain.Summary `json:"run,omitempty"`
}

// StatusInput selects one run; empty lists them.
type StatusInput struct {
	ID    string `json:"id,omitempty" jsonschema:"id фоновой задачи мозга; пусто — список."`
	Trace bool   `json:"trace,omitempty" jsonschema:"true — со списком шагов."`
}

// StatusResult is one run (optionally with steps) or the list.
type StatusResult struct {
	Run   *brain.Summary  `json:"run,omitempty"`
	Steps []brain.Step    `json:"steps,omitempty"`
	Runs  []brain.Summary `json:"runs,omitempty"`
}

// CancelInput stops one run.
type CancelInput struct {
	ID             string `json:"id" jsonschema:"id фоновой задачи мозга."`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"Ключ повтора."`
}

// Register adds the brain tools. r.Call is the tool_invoke path, so policy
// and approvals apply unchanged.
func Register(add func(tooldef.Def), r *brain.Runner) {
	add(tooldef.New(&mcp.Tool{Name: "brain_ask", Description: "Отдать задачу «мозгу» MCPshka — любой модели с вызовом функций из llm.json (GigaChat, FreeLLM, OpenAI-совместимые Claude/GPT): она сама находит инструменты (tool_resolve) и вызывает их тем же путём, что tool_invoke. background=true — долгая работа в фоне (brain_status, brain_cancel), переживает перезапуск. Одобрения, Face ID и агенты — только владелец.", Annotations: &mcp.ToolAnnotations{}}, func(ctx context.Context, _ *mcp.CallToolRequest, in AskInput) (*mcp.CallToolResult, AskResult, error) {
		if in.Background {
			s, err := r.Start(in.IdempotencyKey, in.Task, in.Model, in.MaxSteps)
			return nil, AskResult{Run: &s}, err
		}
		if in.MaxSteps > 30 {
			return nil, AskResult{}, errors.New("max_steps over 30 needs background=true")
		}
		out, err := brain.Run(ctx, r.LLM(in.Model), r.Call, in.Task, in.MaxSteps)
		return nil, AskResult{Result: out}, err
	}))
	add(tooldef.New(&mcp.Tool{Name: "brain_status", Description: "Ход фоновых задач мозга MCPshka: состояние, модель, шаги, ответ; без id — список.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(_ context.Context, _ *mcp.CallToolRequest, in StatusInput) (*mcp.CallToolResult, StatusResult, error) {
		if in.ID == "" {
			return nil, StatusResult{Runs: r.List()}, nil
		}
		rec, err := r.Load(in.ID)
		if err != nil {
			return nil, StatusResult{}, err
		}
		s := rec.Summary()
		out := StatusResult{Run: &s}
		if in.Trace {
			out.Steps = rec.Steps
		}
		return nil, out, nil
	}))
	add(tooldef.New(&mcp.Tool{Name: "brain_cancel", Description: "Остановить фоновую задачу мозга MCPshka.", Annotations: &mcp.ToolAnnotations{}}, func(_ context.Context, _ *mcp.CallToolRequest, in CancelInput) (*mcp.CallToolResult, brain.Summary, error) {
		out, err := r.Cancel(in.ID)
		return nil, out, err
	}))
}
