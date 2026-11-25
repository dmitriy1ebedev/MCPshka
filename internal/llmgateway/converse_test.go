package llmgateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Both dialects round-trip a function call and its result.
func TestConverseDialects(t *testing.T) {
	for _, dialect := range []string{"gigachat", "openai"} {
		var seen map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&seen)
			if dialect == "gigachat" {
				w.Write([]byte(`{"model":"GigaChat-2-Max","choices":[{"message":{"content":"","functions_state_id":"st-1","function_call":{"name":"tool_resolve","arguments":{"goal":"статус"}}}}]}`))
				return
			}
			w.Write([]byte(`{"model":"m","choices":[{"message":{"tool_calls":[{"id":"c1","function":{"name":"tool_resolve","arguments":"{\"goal\":\"статус\"}"}}]}}]}`))
		}))
		dir := t.TempDir()
		cfg := filepath.Join(dir, "llm.json")
		raw, _ := json.Marshal(map[string]any{"base_url": srv.URL + "/v1", "model": "m", "dialect": dialect, "concurrency": 1})
		os.WriteFile(cfg, raw, 0600)
		g := &Gateway{ConfigPath: cfg, JournalPath: filepath.Join(dir, "j"), Secrets: func() []string { return nil }}
		msgs := []Message{{Role: "user", Content: "что с хостом"},
			{Role: "assistant", Call: &FunctionCall{ID: "c0", StateID: "st-0", Name: "mcpshka_guide", Arguments: map[string]any{"topic": "rules"}}},
			{Role: "function", Name: "mcpshka_guide", CallID: "c0", Content: "{}"}}
		turn, err := g.Converse(context.Background(), "brain", msgs, []Function{{Name: "tool_resolve", Description: "d", Parameters: map[string]any{"type": "object"}}}, "")
		srv.Close()
		if err != nil || turn.Call == nil || turn.Call.Name != "tool_resolve" || turn.Call.Arguments["goal"] != "статус" {
			t.Fatalf("%s: %+v %v", dialect, turn, err)
		}
		if dialect == "gigachat" {
			ms := seen["messages"].([]any)
			if ms[1].(map[string]any)["functions_state_id"] != "st-0" || ms[2].(map[string]any)["content"] != `{}` || turn.Call.StateID != "st-1" {
				t.Fatalf("gigachat state/content: %v %+v", ms, turn.Call)
			}
		}
		_, fn := seen["functions"]
		_, tl := seen["tools"]
		if fn != (dialect == "gigachat") || tl != (dialect == "openai") {
			t.Fatalf("%s: wrong wire format %v", dialect, seen)
		}
	}
}


// 0.19.0 acceptance: model "auto" of a FreeLLMAPI endpoint was refused —
// its /v1/models lists only concrete models; the configured model counts.
func TestConfiguredRouterModelIsServed(t *testing.T) {
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Write([]byte(`{"data":[{"id":"gemini-2.5-flash"}]}`))
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		asked, _ = body["model"].(string)
		w.Write([]byte(`{"model":"gemini-2.5-flash","choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "llm.json")
	raw, _ := json.Marshal(map[string]any{"base_url": srv.URL + "/v1", "model": "auto"})
	os.WriteFile(cfg, raw, 0600)
	g := &Gateway{ConfigPath: cfg, JournalPath: filepath.Join(dir, "j"), Secrets: func() []string { return nil }}
	turn, err := g.Converse(context.Background(), "ask", []Message{{Role: "user", Content: "hi"}}, nil, "auto")
	if err != nil || turn.Text != "ok" || asked != "auto" {
		t.Fatalf("%+v %v asked=%q", turn, err, asked)
	}
	if _, err := g.Converse(context.Background(), "ask", []Message{{Role: "user", Content: "hi"}}, nil, "other-model"); err == nil {
		t.Fatal("a model no endpoint lists or configures must be refused")
	}
}
