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
	"sync/atomic"
	"testing"
)

func TestFilterRefusesSecretsAndRedactsLAN(t *testing.T) {
	for _, s := range []string{
		"Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghijklmnop",
		"ha_token = 'abcdef123456'",
		"PSK=Sup3rS3cretValue",
		"key: sk-proj-AAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"cat /var/lib/mcpshka/secrets/ha.token",
		"PrivateKey = 0123456789abcdefghijklmnopqrstuvwxyzABCDEFG=",
		"x9F2kQ7pLm3Zt8Rw1Vb6Nc4Hy0Jd5Gs2Ae7Ku9Qo3Wi",
	} {
		if v := Filter(s); v.Allowed {
			t.Fatalf("allowed secret %q", s)
		}
	}
	v := Filter("Apple TV 192.168.77.60 02:00:00:00:00:60 lost at 06:20, commit 13d787fb6a42e463a07a1fc1001aa63770a5bb12")
	if !v.Allowed || strings.Contains(v.Text, "192.168") || strings.Contains(v.Text, "58:d3") || v.Redactions != 2 || !strings.Contains(v.Text, "13d787fb") {
		t.Fatalf("redaction: %+v", v)
	}
}

// Canary: a fake token must never reach the provider; the journal records
// the redaction without the text.
func TestAskCanaryRedactsAndJournals(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"free"}]}`))
			return
		}
		hits.Add(1)
		raw, _ := io.ReadAll(r.Body)
		for _, secret := range []string{"MCPSHKA_CANARY", "plainlowercaseword42"} {
			if strings.Contains(string(raw), secret) {
				t.Error("secret reached provider")
			}
		}
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}}})
	}))
	defer srv.Close()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "llm.json")
	_ = os.WriteFile(cfg, []byte(`{"base_url":"`+srv.URL+`/v1","model":"free"}`), 0o600)
	g := &Gateway{ConfigPath: cfg, JournalPath: filepath.Join(dir, "sends.jsonl"), Secrets: func() []string { return []string{"plainlowercaseword42"} }}
	canary := "token=MCPSHKA_CANARY_c4n4ryV4lu3"
	if _, err := g.Ask(context.Background(), "test", "sum up", canary); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Ask(context.Background(), "test", "sum up", "log: plainlowercaseword42 ok"); err != nil {
		t.Fatal(err)
	}

	got, err := g.Ask(context.Background(), "test", "sum up", "Apple TV 192.168.77.60 silent 06:20-06:52")
	if err != nil || got != "ok" || hits.Load() != 4 {
		t.Fatalf("ask: %q %v", got, err)
	}
	j, _ := os.ReadFile(g.JournalPath)
	if strings.Contains(string(j), "CANARY") || strings.Contains(string(j), "192.168") || strings.Count(string(j), "\n") != 4 || strings.Contains(string(j), "plainlowercase") {
		t.Fatalf("journal: %s", j)
	}
}

func TestAskWithoutConfigIsUnavailable(t *testing.T) {
	g := &Gateway{ConfigPath: filepath.Join(t.TempDir(), "none.json")}
	if _, err := g.Ask(context.Background(), "t", "", "x"); err == nil || !strings.Contains(err.Error(), "LLM_UNAVAILABLE") {
		t.Fatal(err)
	}
}

func TestKnownSecretsRefusedEvenWithoutShape(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "ha.token"), []byte("plainlowercaseword42\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "x.env"), []byte("API_TOKEN=zzqqshortish99\nNAME=public\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"client_secret":"jsonsecretvalue1","user":"owner"}`), 0o600)
	old := SecretSources
	SecretSources = []string{dir}
	known = knownSet{}
	t.Cleanup(func() { SecretSources = old; known = knownSet{} })
	vals := knownSecrets()
	for _, s := range []string{"see plainlowercaseword42 here", "env zzqqshortish99", "json jsonsecretvalue1", "prefix plainlowercas…"} {
		if !leaksKnown(s, vals) {
			t.Fatalf("missed %q in %d known", s, len(vals))
		}
	}
	if leaksKnown("NAME=public owner", vals) {
		t.Fatalf("false positive: %v", vals)
	}
	if v := Filter("пишите owner@example.com"); !v.Allowed || strings.Contains(v.Text, "@") {
		t.Fatalf("email: %+v", v)
	}
}
