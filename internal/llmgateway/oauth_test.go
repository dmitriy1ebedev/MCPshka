package llmgateway

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// GigaChat-style endpoint behind its own CA as the fallback of a dead
// primary: the key is exchanged once for a bearer that is then reused.
func TestFallbackToOAuthEndpointWithOwnCA(t *testing.T) {
	var tokens, chats atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth":
			if r.Header.Get("Authorization") != "Basic giga-auth-key" || r.Header.Get("RqUID") == "" || r.FormValue("scope") != "GIGACHAT_API_PERS" {
				http.Error(w, "bad oauth", 401)
				return
			}
			tokens.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"access_token": "short-bearer", "expires_at": time.Now().Add(30 * time.Minute).UnixMilli()})
		case "/api/v1/models":
			if r.Header.Get("Authorization") != "Bearer short-bearer" {
				http.Error(w, "no bearer", 401)
				return
			}
			w.Write([]byte(`{"data":[{"id":"GigaChat-2"}]}`))
		case "/api/v1/chat/completions":
			if r.Header.Get("Authorization") != "Bearer short-bearer" {
				http.Error(w, "no bearer", 401)
				return
			}
			chats.Add(1)
			w.Write([]byte(`{"model":"GigaChat-2","choices":[{"message":{"content":"ответ"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0600)
	key := filepath.Join(dir, "giga.key")
	os.WriteFile(key, []byte("giga-auth-key\n"), 0600)
	cfg := map[string]any{
		"base_url": "http://127.0.0.1:1/v1", "model": "auto", "budget_seconds": 30,
		"fallback": []any{map[string]any{
			"base_url": srv.URL + "/api/v1", "model": "GigaChat-2", "key_file": key,
			"oauth": map[string]any{"token_url": srv.URL + "/oauth", "scope": "GIGACHAT_API_PERS", "request_id_header": "RqUID", "ca_file": ca},
		}},
	}
	raw, _ := json.Marshal(cfg)
	path := filepath.Join(dir, "llm.json")
	os.WriteFile(path, raw, 0600)
	g := &Gateway{ConfigPath: path, JournalPath: filepath.Join(dir, "sends.jsonl"), Secrets: func() []string { return nil }}
	for i := 0; i < 2; i++ {
		reply, err := g.AskDetailed(context.Background(), "test", "", "привет", Options{})
		if err != nil || reply.Model != "GigaChat-2" || reply.Text != "ответ" {
			t.Fatalf("ask %d: %+v %v", i, reply, err)
		}
	}
	if tokens.Load() != 1 || chats.Load() < 2 {
		t.Fatalf("token exchanges %d (want 1), chats %d", tokens.Load(), chats.Load())
	}
	if journal, _ := os.ReadFile(filepath.Join(dir, "sends.jsonl")); strings.Contains(string(journal), "short-bearer") || strings.Contains(string(journal), "giga-auth-key") {
		t.Fatal("credential in journal")
	}
}

func TestOAuthConfigIsHTTPSOnlyAndFallbackDoesNotNest(t *testing.T) {
	dir := t.TempDir()
	for name, cfg := range map[string]string{
		"http token": `{"base_url":"http://a/v1","model":"m","oauth":{"token_url":"http://x/oauth"}}`,
		"nested":     `{"base_url":"http://a/v1","model":"m","fallback":[{"base_url":"http://b/v1","model":"m","fallback":[{"base_url":"http://c/v1","model":"m"}]}]}`,
		"bad header": `{"base_url":"http://a/v1","model":"m","oauth":{"token_url":"https://x/oauth","request_id_header":"X: y"}}`,
	} {
		p := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".json")
		os.WriteFile(p, []byte(cfg), 0600)
		if _, err := (&Gateway{ConfigPath: p}).config(); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}
