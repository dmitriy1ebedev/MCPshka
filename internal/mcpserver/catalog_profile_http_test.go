package mcpserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
)

func TestHTTPCatalogDefaultFullAndGateway(t *testing.T) {
	t.Setenv("MCPSHKA_TOOL_CATALOG", "")
	p := policy.Default()
	p.AllowedWorkdirs = []string{t.TempDir()}
	manager, err := jobs.New(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(HTTPHandler(manager, "catalog-fixture"))
	defer ts.Close()
	list := func(query string) map[string]bool {
		t.Helper()
		req, err := http.NewRequest("POST", ts.URL+"/mcp"+query, bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer catalog-fixture")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", "2025-06-18")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		var body struct {
			Result struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			} `json:"result"`
		}
		if res.StatusCode != 200 || json.Unmarshal(raw, &body) != nil || len(body.Result.Tools) == 0 {
			t.Fatalf("catalog HTTP %d: %s", res.StatusCode, raw)
		}
		out := map[string]bool{}
		for _, tool := range body.Result.Tools {
			out[tool.Name] = true
		}
		return out
	}
	compact := list("")
	full := list("?tools=all")
	gateway := list("?tools=core")
	if len(compact) != 40 || len(full) <= 250 || len(gateway) != len(coreTools) {
		t.Fatalf("compact=%d full=%d gateway=%d", len(compact), len(full), len(gateway))
	}
	for _, n := range []string{"pdf_read", "pdf_write", "tool_usage", "proc_start"} {
		if !full[n] {
			t.Error("full catalog missing", n)
		}
	}
	for n := range compact {
		if !full[n] {
			t.Error("full catalog lost", n)
		}
	}
	t.Setenv("MCPSHKA_TOOL_CATALOG", "all")
	if got := list(""); len(got) != len(full) {
		t.Fatalf("env full catalog: %d want %d", len(got), len(full))
	}
}
