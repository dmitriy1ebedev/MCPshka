package cievidence

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVerifyGitHubMainRequiresPinnedRepositoryAndTip(t *testing.T) {
	candidate := strings.Repeat("a", 40)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/repos/"+Repository+"/git/ref/heads/main" || r.Header.Get("Authorization") != "Bearer fixture" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
			return
		}
		fmt.Fprintf(w, `{"ref":"refs/heads/main","object":{"type":"commit","sha":%q}}`, candidate)
	}))
	defer server.Close()
	if err := VerifyGitHubMain(context.Background(), server.Client(), server.URL, "fixture", candidate); err != nil {
		t.Fatal(err)
	}
	if err := VerifyGitHubMain(context.Background(), server.Client(), server.URL, "fixture", strings.Repeat("b", 40)); err == nil {
		t.Fatal("a different branch tip was accepted")
	}
}
