package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/llmgateway"
)

func TestLLMModelsAskCompareScopeAndLimits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			_, _ = io.WriteString(w, `{"data":[{"id":"one"},{"id":"two"},{"id":"three"}]}`)
			return
		}
		var in struct {
			Model    string
			Messages []struct{ Content string }
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		text := "answer " + in.Model
		_ = json.NewEncoder(w).Encode(map[string]any{"model": in.Model, "choices": []any{map[string]any{"message": map[string]string{"content": text}}}})
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "llm.json")
	if err := os.WriteFile(path, []byte(`{"base_url":"`+srv.URL+`/v1","model":"auto"}`), 0600); err != nil {
		t.Fatal(err)
	}
	h := host()
	h.LLM = &llmgateway.Gateway{ConfigPath: path, Secrets: func() []string { return nil }}
	script := `
 const catalog = llm.models();
 llm.ask("warm up", "public", {model:"one"});
 const reply = llm.ask("explain", {code:"password=fakecanary123"}, {model:"two", timeout_seconds:91, budget_seconds:180});
 const comparison = llm.compare("review", "public code");
 const scope = llm.scope({project:"go2rtc",paths:[".gitverse/workflows/ci.yml"]},"mcpshka","/repo/mcpshka");
 return catalog.length === 3 && reply.model === "two" && reply.redactions === 1 &&
 reply.redaction_types.assignment === 1 && reply.text === "answer two" &&
 comparison.complete && comparison.requested === 3 && comparison.replies.length === 3 &&
 scope.requires_other_project && !scope.eligible;
 `
	result, err := Run(context.Background(), Input{Script: script, TimeoutSeconds: 300}, h)
	if err != nil || result.Error != "" || result.Result != true || len(result.Calls) != 4 {
		t.Fatalf("%+v %v", result, err)
	}
	result, err = Run(context.Background(), Input{Script: "llm.models(); return llm.ask('x')", MaxCalls: 1}, h)
	if err != nil || result.Error == "" {
		t.Fatal("LLM call limit was not enforced")
	}
}

func TestExtendedCodeRunBudget(t *testing.T) {
	h := host()
	h.Invoke = func(ctx context.Context, _ string, _ map[string]any) (any, error) {
		deadline, ok := ctx.Deadline()
		return ok && time.Until(deadline) > 179*time.Second, nil
	}
	result, err := Run(context.Background(), Input{Script: "return mcp.call('double',{})", TimeoutSeconds: 180}, h)
	if err != nil || result.Error != "" || result.Result != true {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestLLMCompareReturnsBackgroundHandle(t *testing.T) {
	h := host()
	h.LLM = &llmgateway.Gateway{}
	started := false
	h.AsyncLLM = func(ctx context.Context, run func(context.Context) (any, error)) (any, error) {
		started = true
		return map[string]any{"job": map[string]any{"id": "llm-fixture", "state": "running"}}, nil
	}
	out, e := Run(context.Background(), Input{Script: `return llm.compare("review","public").job.id;`}, h)
	if e != nil || out.Error != "" || out.Result != "llm-fixture" || !started {
		t.Fatal(out, e)
	}
}

func TestLLMBusyPreservesAcceptedReceipt(t *testing.T) {
	calls := 0
	out, err := Run(context.Background(), Input{Script: "return [llm.ask('first'), llm.ask('second')]"}, Host{
		LLM: &llmgateway.Gateway{},
		AsyncLLM: func(context.Context, func(context.Context) (any, error)) (any, error) {
			calls++
			if calls == 1 {
				return map[string]any{"job": map[string]string{"id": "accepted-1"}}, nil
			}
			return nil, errors.New("BUSY: LLM job already running")
		},
	})
	if err != nil || !strings.Contains(out.Error, "BUSY") || calls != 2 {
		t.Fatal(out, err)
	}
	raw, _ := json.Marshal(out.Result)
	if !strings.Contains(string(raw), "accepted-1") {
		t.Fatalf("missing accepted receipt: %s", raw)
	}
}
