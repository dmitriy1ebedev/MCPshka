package mcpserver

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
)

func TestHTTPHandlerRequiresBearerAndInitializesMCP(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	manager, err := jobs.New(root, &policy.Policy{
		AllowedWorkdirs: []string{work},
		DeniedCommands:  map[string]struct{}{},
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(HTTPHandler(manager, "secret-token"))
	defer ts.Close()

	body := []byte("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-11-25\",\"capabilities\":{},\"clientInfo\":{\"name\":\"test\",\"version\":\"1\"}}}")

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/mcp", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", resp.StatusCode)
	}

	req, err = http.NewRequest(http.MethodPost, ts.URL+"/mcp", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authorized status=%d body=%q", resp.StatusCode, got)
	}
	if !strings.Contains(string(got), "MCPshka") || !strings.Contains(string(got), Version) {
		t.Fatalf("unexpected initialize response: %q", got)
	}
}

func TestHealthDoesNotExposeMCPToken(t *testing.T) {
	manager, err := jobs.New(t.TempDir(), &policy.Policy{
		AllowedWorkdirs: []string{t.TempDir()},
		DeniedCommands:  map[string]struct{}{},
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(HTTPHandler(manager, "top-secret"))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if strings.Contains(string(got), "top-secret") {
		t.Fatalf("health response leaked token: %q", got)
	}
}
