package codemode

import (
	"context"
	"strings"
	"testing"
	"time"
)

func host() Host {
	return Host{
		Allowed: func(n string) bool { return n == "double" },
		Invoke: func(_ context.Context, _ string, a map[string]any) (any, error) {
			return map[string]any{"v": a["x"].(float64) * 2}, nil
		},
	}
}

func TestRunChainsAndLimits(t *testing.T) {
	r, err := Run(context.Background(), Input{Script: "const a = mcp.call('double', {x: 2}); return mcp.call('double', {x: a.v}).v"}, host())
	if err != nil || r.Error != "" || r.Result != float64(8) || len(r.Calls) != 2 {
		t.Fatalf("chain: %+v %v", r, err)
	}
	for script, want := range map[string]string{
		"mcp.call('fs_write', {})":            "POLICY_DENIED",
		"while (true) {}":                     "TIMEOUT",
		"return typeof ArrayBuffer":           "",
		"for (;;) mcp.call('double', {x: 1})": "LIMIT",
		"return 'x'.repeat(70000)":            "LIMIT",
	} {
		r, _ := Run(context.Background(), Input{Script: script, TimeoutSeconds: 1, MaxCalls: 3}, host())
		if !strings.Contains(r.Error, want) || (want == "" && r.Result != "undefined") {
			t.Fatalf("%q: %+v, want %q", script, r, want)
		}
	}
}

func TestRunParallelStoreSpillAndAsk(t *testing.T) {
	st := NewStore()
	h := host()
	h.Store = st
	r, err := Run(context.Background(), Input{Script: "const r = mcp.all([['double', {x: 1}], ['double', {x: 2}], ['fs_write', {}]]); store.set('a', r.map(x => x.ok ? x.value.v : null)); return r.length"}, h)
	if err == nil && r.Error == "" {
		t.Fatalf("denied tool inside mcp.all must stop the script: %+v", r)
	}
	r, _ = Run(context.Background(), Input{Script: "const r = mcp.all([['double', {x: 1}], ['double', {x: 2}]]); store.set('a', r.map(x => x.value.v)); return r.filter(x => x.ok).length"}, h)
	if r.Error != "" || r.Result != float64(2) || len(r.Calls) != 2 {
		t.Fatalf("all: %+v", r)
	}
	r, _ = Run(context.Background(), Input{Script: "return 'x'.repeat(70000)"}, h)
	if r.Error != "" || r.Spill == nil || r.Spill.Bytes < 70000 || len(r.Spill.Preview) != previewBytes {
		t.Fatalf("spill: %+v", r.Spill)
	}
	r, _ = Run(context.Background(), Input{Script: "return store.get('" + r.Spill.Key + "').length"}, h)
	if r.Result != float64(70000) {
		t.Fatalf("spill read: %+v", r)
	}
	r, _ = Run(context.Background(), Input{Script: "return llm.ask('x')"}, host())
	if !strings.Contains(r.Error, "LLM_UNAVAILABLE") {
		t.Fatalf("no ask: %+v", r)
	}
}

func TestStoreEvictsOldestAndExpires(t *testing.T) {
	s := NewStore()
	s.MaxKeys, s.MaxSize = 2, 1<<20
	_ = s.Set("a", []byte("1"))
	_ = s.Set("b", []byte("2"))
	_ = s.Set("c", []byte("3"))
	if _, ok := s.Get("a"); ok || len(s.Keys()) != 2 {
		t.Fatalf("evict: %v", s.Keys())
	}
	s.TTL = 0
	if _, ok := s.Get("c"); ok {
		t.Fatal("expire")
	}
}

func TestCancelReleasesSlotWhileToolUnwinds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = Run(ctx, Input{Script: `return mcp.call("slow",{});`}, Host{Allowed: func(string) bool { return true }, Invoke: func(context.Context, string, map[string]any) (any, error) { close(entered); <-release; return nil, nil }})
	}()
	<-entered
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancel retained slot")
	}
	out, e := Run(context.Background(), Input{Script: `return 42;`}, Host{})
	if e != nil || out.Error != "" {
		t.Fatal(out, e)
	}
}

// S5.CM.6: a mutation without its retry key is POLICY_DENIED and never
// dispatched; with the key it goes through the same dispatcher, keyed.
func TestMutateNeedsTheRetryKey(t *testing.T) {
	var sent []string
	h := host()
	h.Invoke = func(_ context.Context, name string, a map[string]any) (any, error) {
		sent = append(sent, name)
		return map[string]any{"receipt": "r-1"}, nil
	}
	h.Mutable = func(name string, a map[string]any) (string, error) {
		k, _ := a["idempotency_key"].(string)
		if name != "fs_write" || k == "" {
			return "", errPolicy
		}
		return k, nil
	}
	r, _ := Run(context.Background(), Input{Script: "return mcp.mutate('fs_write', {path: '/x'})"}, h)
	if !strings.Contains(r.Error, "POLICY_DENIED") || len(sent) != 0 {
		t.Fatalf("without key: %+v sent %v", r, sent)
	}
	r, _ = Run(context.Background(), Input{Script: "return mcp.mutate('fs_write', {path: '/x', idempotency_key: 'k1'}).receipt"}, h)
	if r.Error != "" || r.Result != "r-1" || len(sent) != 1 || r.Calls[0].Key != "k1" {
		t.Fatalf("with key: %+v sent %v", r, sent)
	}
	// The default gate is the mutation contract: a read-only tool is refused.
	h.Mutable = nil
	if r, _ = Run(context.Background(), Input{Script: "mcp.mutate('fs_read', {idempotency_key: 'k2'})"}, h); !strings.Contains(r.Error, "POLICY_DENIED") || len(sent) != 1 {
		t.Fatalf("the default gate must refuse a tool without a contract: %+v", r)
	}
}

var errPolicy = &policyErr{}

type policyErr struct{}

func (*policyErr) Error() string { return "no key" }
