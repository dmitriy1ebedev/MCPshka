// Package codemode runs Code Mode scripts (Cloudflare Code Mode, ROADMAP
// S5.CM): one short JavaScript chains tool calls and returns only its final
// value, so intermediate results stay out of the model's context. The engine
// is goja (pure Go, no Node, no cloud). goja has no heap cap, so a run is
// bounded from outside: one run at a time, a heap watchdog, time, call and
// output limits, and no ArrayBuffer/typed arrays (the 2026 goja ArrayBuffer
// RCE class and the cheapest way to allocate big). What a script may call is
// the caller's decision (Allowed); calls go through the caller's dispatcher.
package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/metrics"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	"mcpshka/internal/llmgateway"
	"mcpshka/internal/toolcontract"
)

type Input struct {
	Script         string `json:"script" jsonschema:"JavaScript body; return the result. mcp.call(name, args) returns the tool's output object or throws; mcp.all([[name, args], ...]) runs calls in parallel and returns [{ok, value} | {ok:false, error}]; mcp.mutate(name, args) runs one mutating tool only with its idempotency_key (else POLICY_DENIED; UNKNOWN → same call, same key); store.set(key, v)/store.get(key)/store.keys() keep values between code_run calls (2 h); llm.models(), llm.ask(question, data, {model, timeout_seconds, budget_seconds}) and llm.compare(question, data, {count:3}) return background jobs: job_wait then job_output; llm.scope(card, project, root) checks repository scope; console.log(...) is kept. A result over 64 KiB is saved as store 'spill:N' with a preview. Example: const s = mcp.call('git_status', {path: '/root/repos/lane'}); return s.branch"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"1..600, default 20; LLM calls return background jobs"`
	MaxCalls       int    `json:"max_calls,omitempty" jsonschema:"1..50 tool calls, default 20"`
	Async          bool   `json:"async,omitempty" jsonschema:"Run as a background job that survives the chat (CI wait + logs, up to timeout_seconds): returns {job}; job_wait then job_output give the usual result"`
}

type Call struct {
	Tool  string `json:"tool"`
	MS    int64  `json:"ms"`
	Error string `json:"error,omitempty"`
	// Key is the retry key of an mcp.mutate call: the same call with the
	// same key returns the original receipt; UNKNOWN is reconciled with it.
	Key string `json:"idempotency_key,omitempty"`
}

type Result struct {
	Result any      `json:"result,omitempty"`
	Logs   []string `json:"logs,omitempty"`
	Calls  []Call   `json:"calls"`
	Error  string   `json:"error,omitempty"`
	// Spill names the store key holding the whole result when it was over
	// the output limit; Result then holds a preview.
	Spill *Spill `json:"spill,omitempty"`
}

type Spill struct {
	Key     string `json:"key"`
	Bytes   int    `json:"bytes"`
	Preview string `json:"preview"`
	Hint    string `json:"hint"`
}

// Host is what a script reaches: Allowed says whether a tool may be called
// at all, Invoke calls it and returns its output.
type Host struct {
	Allowed func(name string) bool
	// Mutable admits one mcp.mutate call (S5.CM.6) and returns its retry
	// key, or says why not (no mutation contract, no key, approval-only
	// tool). Nil means toolcontract.ScriptMutable.
	Mutable func(name string, args map[string]any) (string, error)
	Invoke  func(ctx context.Context, name string, args map[string]any) (any, error)
	// Store keeps values between runs; nil disables store.* and spilling.
	Store *Store
	// LLM reaches only the configured, filtered outside gateway.
	LLM      *llmgateway.Gateway
	AsyncLLM func(context.Context, func(context.Context) (any, error)) (any, error)
}

const (
	parallel     = 6
	previewBytes = 4 << 10
)

const (
	maxScript   = 32 << 10
	maxOutput   = 64 << 10
	maxLogs     = 64
	heapGrowth  = 128 << 20
	stackFrames = 256
	maxMutations = 10
)

var slot = make(chan struct{}, 1)
var toolSlots = make(chan struct{}, parallel)

var removed = []string{"ArrayBuffer", "SharedArrayBuffer", "DataView", "Int8Array", "Uint8Array", "Uint8ClampedArray", "Int16Array", "Uint16Array", "Int32Array", "Uint32Array", "Float32Array", "Float64Array", "BigInt64Array", "BigUint64Array", "Atomics", "WebAssembly"}

func Run(ctx context.Context, in Input, host Host) (Result, error) {
	out := Result{Calls: []Call{}}
	if strings.TrimSpace(in.Script) == "" || len(in.Script) > maxScript {
		return out, fmt.Errorf("script must be 1..%d bytes", maxScript)
	}
	timeout := time.Duration(clamp(in.TimeoutSeconds, 1, 600, 20)) * time.Second
	maxCalls := clamp(in.MaxCalls, 1, 50, 20)
	select {
	case slot <- struct{}{}:
		defer func() { <-slot }()
	default:
		return out, errors.New("BUSY: another code_run is running; retry in a few seconds")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	vm := goja.New()
	vm.SetMaxCallStackSize(stackFrames)
	for _, name := range removed {
		_ = vm.GlobalObject().Delete(name)
	}
	console := vm.NewObject()
	_ = console.Set("log", func(call goja.FunctionCall) goja.Value {
		if len(out.Logs) < maxLogs {
			parts := make([]string, 0, len(call.Arguments))
			for _, a := range call.Arguments {
				parts = append(parts, a.String())
			}
			line := strings.Join(parts, " ")
			out.Logs = append(out.Logs, line[:min(len(line), 1024)])
		}
		return goja.Undefined()
	})
	_ = vm.Set("console", console)
	api := vm.NewObject()
	admit := func(name string) {
		if len(out.Calls) >= maxCalls {
			panic(vm.NewGoError(fmt.Errorf("LIMIT: more than %d calls", maxCalls)))
		}
		if !host.Allowed(name) {
			out.Calls = append(out.Calls, Call{Tool: name, Error: "POLICY_DENIED"})
			panic(vm.NewGoError(fmt.Errorf("POLICY_DENIED: %s is not a read-only tool; call it directly", name)))
		}
	}
	_ = api.Set("call", func(name string, args map[string]any) goja.Value {
		admit(name)
		c, v := timedInvoke(ctx, host, name, args)
		out.Calls = append(out.Calls, c)
		if c.Error != "" {
			panic(vm.NewGoError(fmt.Errorf("%s: %s", name, c.Error)))
		}
		return vm.ToValue(v)
	})
	// mcp.all runs independent reads concurrently in Go (the VM itself stays
	// single-threaded): the slowest call sets the time, not the sum.
	_ = api.Set("all", func(list [][]any) goja.Value {
		type job struct {
			name string
			args map[string]any
		}
		jobs := make([]job, 0, len(list))
		for _, item := range list {
			name, _ := item[0].(string)
			args := map[string]any{}
			if len(item) > 1 {
				if a, ok := item[1].(map[string]any); ok {
					args = a
				}
			}
			admit(name)
			out.Calls = append(out.Calls, Call{Tool: name})
			jobs = append(jobs, job{name, args})
		}
		base := len(out.Calls) - len(jobs)
		results := make([]map[string]any, len(jobs))
		calls := make([]Call, len(jobs))
		sem := make(chan struct{}, parallel)
		var wg sync.WaitGroup
		for i, j := range jobs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				c, v := timedInvoke(ctx, host, j.name, j.args)
				calls[i] = c
				if c.Error != "" {
					results[i] = map[string]any{"ok": false, "tool": j.name, "error": c.Error}
				} else {
					results[i] = map[string]any{"ok": true, "tool": j.name, "value": v}
				}
			}()
		}
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-ctx.Done():
			panic(vm.NewGoError(ctx.Err()))
		}
		copy(out.Calls[base:], calls)
		return vm.ToValue(results)
	})
	// mcp.mutate (stage 2): one mutating call at a time, never in mcp.all,
	// only with its retry key; Face ID, lease and receipts are the tool's own
	// through the same dispatcher as a direct call.
	mutations := 0
	_ = api.Set("mutate", func(name string, args map[string]any) goja.Value {
		if len(out.Calls) >= maxCalls || mutations >= maxMutations {
			panic(vm.NewGoError(fmt.Errorf("LIMIT: at most %d calls and %d mutations", maxCalls, maxMutations)))
		}
		admit := host.Mutable
		if admit == nil {
			admit = toolcontract.ScriptMutable
		}
		k, err := admit(name, args)
		if err != nil {
			out.Calls = append(out.Calls, Call{Tool: name, Error: "POLICY_DENIED"})
			panic(vm.NewGoError(fmt.Errorf("POLICY_DENIED: %s: %v", name, err)))
		}
		mutations++
		c, v := timedInvoke(ctx, host, name, args)
		c.Key = k
		if c.Error != "" && ctx.Err() != nil {
			c.Error = "UNKNOWN: run ended before the reply; repeat the same call with the same idempotency_key (or mutation_probe) before anything else"
		}
		out.Calls = append(out.Calls, c)
		if c.Error != "" {
			panic(vm.NewGoError(fmt.Errorf("%s: %s", name, c.Error)))
		}
		return vm.ToValue(v)
	})
	_ = vm.Set("mcp", api)
	if host.Store != nil {
		st := vm.NewObject()
		_ = st.Set("set", func(key string, v goja.Value) bool {
			raw, err := json.Marshal(v.Export())
			if err == nil {
				err = host.Store.Set(key, raw)
			}
			if err != nil {
				panic(vm.NewGoError(err))
			}
			return true
		})
		_ = st.Set("get", func(key string) goja.Value {
			v, ok := host.Store.Get(key)
			if !ok {
				return goja.Undefined()
			}
			return vm.ToValue(v)
		})
		_ = st.Set("keys", func() goja.Value { return vm.ToValue(host.Store.Keys()) })
		_ = st.Set("del", func(key string) bool { host.Store.Delete(key); return true })
		_ = vm.Set("store", st)
	}
	bindLLM(ctx, vm, host.LLM, &out, maxCalls, host.AsyncLLM)

	stop := watchHeap(ctx, vm)
	defer stop()
	go func() {
		<-ctx.Done()
		vm.Interrupt("TIMEOUT: " + timeout.String())
	}()

	v, err := vm.RunString("(function(){\n" + in.Script + "\n})()")
	if err != nil {
		var intr *goja.InterruptedError
		if errors.As(err, &intr) {
			out.Error = fmt.Sprint(intr.Value())
		} else {
			out.Error = err.Error()
		}
		out.Error = out.Error[:min(len(out.Error), 2048)]
		return out, nil
	}
	if v != nil && !goja.IsUndefined(v) && !goja.IsNull(v) {
		raw, err := json.Marshal(v.Export())
		if err != nil {
			out.Error = "result is not JSON: " + err.Error()
			return out, nil
		}
		if len(raw) > maxOutput {
			if host.Store == nil {
				out.Error = fmt.Sprintf("LIMIT: result %d bytes > %d; return less", len(raw), maxOutput)
				return out, nil
			}
			key, err := host.Store.spill(raw)
			if err != nil {
				out.Error = fmt.Sprintf("LIMIT: result %d bytes > %d and not saved: %v; return less", len(raw), maxOutput, err)
				return out, nil
			}
			out.Spill = &Spill{Key: key, Bytes: len(raw), Preview: string(raw[:previewBytes]),
				Hint: "whole result saved: in the next code_run use store.get('" + key + "') and return only what you need"}
			return out, nil
		}
		_ = json.Unmarshal(raw, &out.Result)
	}
	return out, nil
}

// timedInvoke calls one tool and normalises its output to JSON values.
func timedInvoke(ctx context.Context, host Host, name string, args map[string]any) (Call, any) {
	start := time.Now()
	type answer struct {
		value any
		err   error
	}
	done := make(chan answer, 1)
	select {
	case toolSlots <- struct{}{}:
	case <-ctx.Done():
		return Call{Tool: name, Error: ctx.Err().Error()}, nil
	}
	go func() {
		defer func() { <-toolSlots }()
		v, e := invoke(ctx, host, name, args)
		done <- answer{v, e}
	}()
	var res any
	var err error
	select {
	case a := <-done:
		res, err = a.value, a.err
	case <-ctx.Done():
		err = ctx.Err()
	}
	c := Call{Tool: name, MS: time.Since(start).Milliseconds()}
	if err != nil {
		c.Error = err.Error()[:min(len(err.Error()), 300)]
		return c, nil
	}
	var v any
	raw, _ := json.Marshal(res)
	_ = json.Unmarshal(raw, &v)
	return c, v
}

// invoke passes args as JSON would (numbers float64) and turns a tool's
// panic into an error instead of crashing the server.
func invoke(ctx context.Context, host Host, name string, args map[string]any) (res any, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("tool panicked: %v", p)
		}
	}()
	var norm map[string]any
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(raw, &norm)
	return host.Invoke(ctx, name, norm)
}

// watchHeap interrupts the script when the process heap grows by more than
// heapGrowth during the run (runtime/metrics: no stop-the-world).
func watchHeap(ctx context.Context, vm *goja.Runtime) func() {
	sample := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	read := func() uint64 {
		metrics.Read(sample)
		if sample[0].Value.Kind() == metrics.KindUint64 {
			return sample[0].Value.Uint64()
		}
		return 0
	}
	base := read()
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(50 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if base > 0 && read() > base+heapGrowth {
					vm.Interrupt("LIMIT: script memory over 128 MiB")
					return
				}
			}
		}
	}()
	return func() { close(done) }
}

func clamp(v, lo, hi, def int) int {
	if v == 0 {
		return def
	}
	return max(lo, min(hi, v))
}
