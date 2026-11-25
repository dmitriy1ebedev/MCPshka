package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/dop251/goja"
	"mcpshka/internal/freellm"
	"mcpshka/internal/llmgateway"
)

func bindLLM(ctx context.Context, vm *goja.Runtime, gateway *llmgateway.Gateway, out *Result, maxCalls int, async ...func(context.Context, func(context.Context) (any, error)) (any, error)) {
	llm := vm.NewObject()
	call := func(name string, run func() (any, error)) goja.Value {
		if len(out.Calls) >= maxCalls {
			panic(vm.NewGoError(errors.New("LIMIT: too many LLM calls")))
		}
		if gateway == nil {
			panic(vm.NewGoError(errors.New("LLM_UNAVAILABLE: no outside model configured")))
		}
		start := time.Now()
		result, err := run()
		c := Call{Tool: name, MS: time.Since(start).Milliseconds()}
		if err != nil {
			c.Error = err.Error()[:min(len(err.Error()), 300)]
		}
		out.Calls = append(out.Calls, c)
		if err != nil {
			panic(vm.NewGoError(err))
		}
		// JSON names, rather than Go field names, are the public JS contract.
		raw, _ := json.Marshal(result)
		var value any
		_ = json.Unmarshal(raw, &value)
		return vm.ToValue(value)
	}
	ask := func(question string, data goja.Value, opts map[string]any, compare bool) goja.Value {
		text := ""
		if data != nil && !goja.IsUndefined(data) && !goja.IsNull(data) {
			if s, ok := data.Export().(string); ok {
				text = s
			} else {
				raw, _ := json.Marshal(data.Export())
				text = string(raw)
			}
		}
		var options llmgateway.Options
		raw, _ := json.Marshal(opts)
		if err := json.Unmarshal(raw, &options); err != nil {
			panic(vm.NewGoError(err))
		}
		name := "llm.ask"
		if compare {
			name = "llm.compare"
		}
		user := question + "\n\nДанные:\n" + text
		run := func(ctx context.Context) (any, error) {
			if compare {
				return gateway.Compare(ctx, "code_run", llmgateway.DigestSystem, user, options)
			}
			return gateway.AskDetailed(ctx, "code_run", llmgateway.DigestSystem, user, options)
		}
		if len(async) > 0 && async[0] != nil {
			return call(name, func() (any, error) { return async[0](ctx, run) })
		}
		return call(name, func() (any, error) { return run(ctx) })
	}
	_ = llm.Set("ask", func(q string, d goja.Value, o map[string]any) goja.Value { return ask(q, d, o, false) })
	_ = llm.Set("compare", func(q string, d goja.Value, o map[string]any) goja.Value { return ask(q, d, o, true) })
	_ = llm.Set("models", func() goja.Value {
		run := func(ctx context.Context) (any, error) { return gateway.Models(ctx) }
		if len(async) > 0 && async[0] != nil {
			return call("llm.models", func() (any, error) { return async[0](ctx, run) })
		}
		return call("llm.models", func() (any, error) { return gateway.Models(ctx) })
	})
	_ = llm.Set("scope", func(card map[string]any, project, root string) goja.Value {
		raw, _ := json.Marshal(card)
		var c freellm.Card
		if err := json.Unmarshal(raw, &c); err != nil {
			panic(vm.NewGoError(err))
		}
		selected := freellm.Select(c, project, root)
		return vm.ToValue(map[string]any{"eligible": selected.Eligible, "requires_other_project": selected.RequiresOtherProject, "reason": selected.Reason})
	})
	_ = vm.Set("llm", llm)
}
