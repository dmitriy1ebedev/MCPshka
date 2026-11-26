package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"mcpshka/internal/issues"
)

func withIssueStore(t *testing.T) *issues.Store {
	t.Helper()
	dir := t.TempDir()
	old := issueService
	issueService = newIssueService()
	issueService.StatePath, issueService.OwnerPath = filepath.Join(dir, "issues.json"), filepath.Join(dir, "owners")
	t.Cleanup(func() { issueService = old })
	return issueStore()
}

func TestIssueEvidenceAndPrompt(t *testing.T) {
	s := withIssueStore(t)
	i, err := s.Create(issues.NewIssue{Title: "phase_gate PASS без чекпоинта", Priority: "P0", Body: "P26…P39 прошли на общем тесте"}, "ai")
	if err != nil {
		t.Fatal(err)
	}
	if verifyIssueEvidence(i, "owner", false) == nil || verifyIssueEvidence(i, "owner", true) != nil {
		t.Fatal("owner acceptance must come only from the page")
	}
	if verifyIssueEvidence(i, "release:0.0.1", false) == nil || verifyIssueEvidence(i, "release:"+Version, false) == nil {
		t.Fatal("MCP card requires an installed check receipt")
	}
	if verifyIssueEvidence(i, "trust me", false) == nil {
		t.Fatal("free text accepted as evidence")
	}
	p := issues.Prompt(i, []issues.Summary{{ID: "MCP-004", Status: "open", Title: "phase_gate PASS не закрывает чекбоксы"}}, []string{"lesson-mcp-001: опыт"})
	for _, want := range []string{i.ID, "P26…P39", "MCP-004", "lesson-mcp-001", "issue_update id=" + i.ID, "issue_accept"} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, p)
		}
	}
}

func TestIssuesAPIListUpdateAccept(t *testing.T) {
	s := withIssueStore(t)
	i, _ := s.Create(issues.NewIssue{Title: "Брокер не успевает восстановление", Priority: "P0", Body: "deadline"}, "ai")
	call := func(method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, "/connections/api/issues"+path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		issuesAPI(context.Background(), rec, req, strings.TrimPrefix(strings.TrimPrefix(path, "?"), "/"))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	if code, out := call(http.MethodGet, "", ""); code != 200 || len(out["issues"].([]any)) != 1 {
		t.Fatalf("list %d %v", code, out)
	}
	if code, _ := call(http.MethodPost, "/"+i.ID+"/accept", `{}`); code != 400 {
		t.Fatal("open card accepted")
	}
	if _, err := s.Update(i.ID, issues.Patch{Status: issues.Fixed, Commit: "abcdef1"}, "ai"); err != nil {
		t.Fatal(err)
	}
	code, out := call(http.MethodPost, "/"+i.ID+"/accept", `{"Note":"проверил журнал"}`)
	if code != 200 || out["issue"].(map[string]any)["status"] != issues.Closed {
		t.Fatalf("owner accept %d %v", code, out)
	}
	if code, _ := call(http.MethodPost, "/"+i.ID+"/update", `{"Status":"closed"}`); code != 400 {
		t.Fatal("closed through update")
	}
}
