package llmgateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T, handler http.HandlerFunc) *Gateway {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	path := filepath.Join(t.TempDir(), "llm.json")
	if err := os.WriteFile(path, []byte(`{"base_url":"`+srv.URL+`/v1","model":"auto"}`), 0600); err != nil {
		t.Fatal(err)
	}
	return &Gateway{ConfigPath: path, Secrets: func() []string { return nil }}
}

func chat(w http.ResponseWriter, model string) {
	_ = json.NewEncoder(w).Encode(map[string]any{"model": model, "choices": []any{map[string]any{"message": map[string]string{"content": "answer " + model}}}})
}

func TestModelsFailoverHealthCacheAndRedaction(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}
	g := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == "GET" {
			calls["catalog"]++
			_, _ = io.WriteString(w, `{"data":[{"id":"auto"},{"id":"fusion"},{"id":"down"},{"id":"good"},{"id":"unwired","execution_status":"needsKey"}]}`)
			return
		}
		var body struct {
			Model    string
			Messages []struct{ Content string }
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls[body.Model]++
		if body.Model == "down" {
			w.WriteHeader(503)
			_, _ = io.WriteString(w, "provider overloaded token=fakecanary123")
			return
		}
		for _, m := range body.Messages {
			if strings.Contains(m.Content, "plainlowercaseword42") {
				t.Error("known secret leaked")
			}
		}
		chat(w, body.Model)
	})
	g.Secrets = func() []string { return []string{"plainlowercaseword42"} }
	reply, err := g.AskDetailed(context.Background(), "test", "summarize", "plainlowercaseword42; password=canary987", Options{})
	if err != nil || reply.Model != "good" || reply.Redactions != 2 || reply.RedactionTypes["known_secret"] != 1 || reply.RedactionTypes["assignment"] != 1 {
		t.Fatalf("%+v %v", reply, err)
	}
	models, err := g.Models(context.Background())
	if err != nil || len(models) != 3 || models[0].State != "dead" || !strings.Contains(models[0].LastError, "503: provider overloaded") || strings.Contains(models[0].LastError, "fakecanary123") || models[1].State != "alive" {
		t.Fatalf("%+v %v", models, err)
	}
	if _, err := g.AskDetailed(context.Background(), "test", "", "another task", Options{}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls["catalog"] != 1 || calls["down"] != 1 || calls["good"] != 3 || calls["unwired"] != 0 {
		t.Fatal(calls)
	}
}

func TestTimeoutFallsBackAndTotalBudget(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}
	g := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			_, _ = io.WriteString(w, `{"data":[{"id":"slow"},{"id":"good"}]}`)
			return
		}
		var body struct {
			Model     string
			MaxTokens int `json:"max_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		calls[body.Model]++
		mu.Unlock()
		if body.Model == "slow" && body.MaxTokens == 0 {
			<-r.Context().Done()
			return
		}
		chat(w, body.Model)
	})
	start := time.Now()
	reply, err := g.AskDetailed(context.Background(), "test", "", "task", Options{TimeoutSeconds: 1, BudgetSeconds: 4})
	if err != nil || reply.Model != "good" || time.Since(start) > 3*time.Second {
		t.Fatalf("%+v %v", reply, err)
	}
	mu.Lock()
	if calls["slow"] != 2 || calls["good"] != 2 {
		t.Fatal(calls)
	}
	mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	// Invalidate the cached failure and force a new short-budget timeout.
	g.mu.Lock()
	g.health = nil
	g.mu.Unlock()
	result, err := g.Compare(ctx, "test", "", "task", Options{Count: 3})
	if err != nil || result.Complete || len(result.Replies) != 0 || time.Since(start) > 3*time.Second {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestCompareDistinctAndPartial(t *testing.T) {
	g := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			_, _ = io.WriteString(w, `{"data":[{"id":"a"},{"id":"b"},{"id":"c"},{"id":"d"}]}`)
			return
		}
		var body struct{ Model string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		actual := body.Model
		if actual == "b" {
			actual = "a"
		}
		chat(w, actual)
	})
	result, err := g.Compare(context.Background(), "test", "", "task", Options{})
	if err != nil || !result.Complete || result.Requested != 3 || len(result.Replies) != 3 || len(result.Failures) != 1 {
		t.Fatalf("%+v %v", result, err)
	}
	result, err = g.Compare(context.Background(), "test", "", "task", Options{Count: 5})
	if err != nil || result.Complete || len(result.Replies) != 3 {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestRedactBlocksKeyMaterialAndCodeFalsePositives(t *testing.T) {
	private := "-----BEGIN PRIVATE KEY-----\nlineOneOfKeyMaterial12345\nlineTwoOfKeyMaterial67890\n-----END PRIVATE KEY-----"
	v := Redact("before " + private + " after")
	if strings.Contains(v.Text, "lineOne") || !strings.Contains(v.Text, "before [REDACTED:private_key] after") {
		t.Fatal(v)
	}
	for _, text := range []string{"TestExecKeyUnwrapsAndSkipsRouted", "token = os.Getenv", "Authorization: Bearer abcdefghijklmnopqrstuvwxyz"} {
		v := Redact(text)
		if !v.Allowed || v.Redactions == 0 || !strings.Contains(v.Text, "[REDACTED:") {
			t.Fatal(v)
		}
	}
	values := []string{}
	collect("PUBLIC=authorization\nTOKEN=secretvalue123\n", func(v string) { values = append(values, v) })
	collect(`{"key_file":"/var/lib/mcpshka/keys/local","token_path":"/tmp/token","client_secret":"othersecret123"}`, func(v string) { values = append(values, v) })
	if len(values) != 2 || values[0] != "secretvalue123" || values[1] != "othersecret123" {
		t.Fatal("credential metadata was collected as a secret")
	}
	text, n := redactKnown("prefix plainlowercaseword42 suffix", []string{"plainlowercaseword42"})
	if n != 1 || strings.Contains(text, "plainlowercase") || strings.Contains(text, "word42") {
		t.Fatal(text, n)
	}
	text, n = redactKnown("prefix plainlowercas… suffix", []string{"plainlowercaseword42"})
	if n != 1 || strings.Contains(text, "plainlowercas") {
		t.Fatal(text, n)
	}
}

func TestConfigBudgetsAndRejectOversize(t *testing.T) {
	g := fixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected request") })
	c, err := g.config()
	if err != nil || c.TimeoutSeconds != 90 || c.BudgetSeconds != 180 || c.ProbeTimeoutSeconds != 5 || c.HealthCacheMinutes != 5 {
		t.Fatal(c, err)
	}
	if _, err := g.AskDetailed(context.Background(), "test", "", strings.Repeat("x", c.MaxInputBytes+1), Options{}); err == nil {
		t.Fatal("oversize allowed")
	}
	if _, err := g.AskDetailed(context.Background(), "test", "", "x", Options{TimeoutSeconds: 301}); err == nil {
		t.Fatal("invalid timeout allowed")
	}
}

func TestCatalogAndReplyFailures(t *testing.T) {
	for _, bad := range []string{"not-json", `{"choices":[]}`, `{"choices":[{"message":{"content":""}}]}`} {
		t.Run(bad, func(t *testing.T) {
			g := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					_, _ = io.WriteString(w, `{"data":[{"id":"a"}]}`)
					return
				}
				_, _ = io.WriteString(w, bad)
			})
			if _, err := g.AskDetailed(context.Background(), "test", "", "task", Options{}); err == nil {
				t.Fatal("invalid completion accepted")
			}
		})
	}
	g := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		_, _ = io.WriteString(w, "upstream unavailable")
	})
	if _, err := g.Models(context.Background()); err == nil || !strings.Contains(err.Error(), "503: upstream unavailable") {
		t.Fatal(err)
	}
}

func TestHealthExpiryAndProbeTimeout(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	g := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			_, _ = io.WriteString(w, `{"data":[{"id":"slow"},{"id":"good"}]}`)
			return
		}
		var body struct{ Model string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		hits[body.Model]++
		mu.Unlock()
		if body.Model == "slow" {
			<-r.Context().Done()
			return
		}
		chat(w, body.Model)
	})
	// A shorter HTTP deadline simulates a slow probe without a multi-second test.
	g.HTTP = &http.Client{Timeout: 200 * time.Millisecond}
	reply, err := g.AskDetailed(context.Background(), "test", "", "task", Options{})
	if err != nil || reply.Model != "good" {
		t.Fatalf("%+v %v", reply, err)
	}
	c, err := g.config()
	if err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	h := g.health[c.BaseURL+"\nslow"]
	h.CheckedAt = time.Now().Add(-6 * time.Minute)
	g.health[c.BaseURL+"\nslow"] = h
	g.mu.Unlock()
	list, err := g.Models(context.Background())
	if err != nil || list[0].State != "unknown" || list[0].LastError == "" {
		t.Fatalf("%+v %v", list, err)
	}
	// Clear the good observation too, so catalog ordering re-probes slow first.
	g.mu.Lock()
	h = g.health[c.BaseURL+"\ngood"]
	h.CheckedAt = time.Now().Add(-6 * time.Minute)
	g.health[c.BaseURL+"\ngood"] = h
	g.mu.Unlock()
	if _, err := g.AskDetailed(context.Background(), "test", "", "task", Options{}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits["slow"] != 2 || hits["good"] != 4 {
		t.Fatal(hits)
	}
}

func TestQuotedPasswordsAndCredentialErrorRedaction(t *testing.T) {
	for _, secret := range []string{`password="correct horse, battery staple"`, "token := 'has spaces and punctuation!'", "password: short"} {
		v := Redact("before\n" + secret + "\nafter")
		if !strings.Contains(v.Text, "before\n[REDACTED:assignment]\nafter") || v.Redactions != 1 {
			t.Fatal(v)
		}
	}
	g := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tinykey" {
			t.Error("configured key not used")
		}
		w.WriteHeader(503)
		_, _ = io.WriteString(w, "gateway credentials tinykey; password=\"correct horse, battery staple\"")
	})
	keyPath := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(keyPath, []byte("tinykey"), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(g.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.KeyFile = keyPath
	raw, _ = json.Marshal(cfg)
	if err := os.WriteFile(g.ConfigPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = g.Models(context.Background())
	if err == nil || strings.Contains(err.Error(), "tinykey") || strings.Contains(err.Error(), "battery") || !strings.Contains(err.Error(), "503: gateway credentials") {
		t.Fatal(err)
	}
	if err := os.Chmod(keyPath, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Models(context.Background()); err == nil {
		t.Fatal("public key file allowed")
	}
}

func TestNeverRedirectsCredentials(t *testing.T) {
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer target.Close()
	g := fixture(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) })
	if _, err := g.Models(context.Background()); err == nil || leaked.Load() {
		t.Fatal("redirect followed")
	}
}

func TestWholeSecretPathAndKnownMultilineValue(t *testing.T) {
	v := Redact("cat /etc/mcpshka/short-password\npublic code")
	if strings.Contains(v.Text, "short-password") || !strings.Contains(v.Text, "public code") {
		t.Fatal(v)
	}
	secret := "known value with spaces and punctuation!"
	got, n := redactKnown("before "+secret+" after", []string{secret})
	if got != "before [REDACTED:known_secret] after" || n != 1 {
		t.Fatal(got, n)
	}
}
