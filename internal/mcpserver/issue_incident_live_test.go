package mcpserver

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"mcpshka/internal/issues"
	"testing"
)

func TestIssueGetShowsFingerprintAndHistoricalRiskThroughTool(t *testing.T) {
	s := withIssueStore(t)
	prior, err := s.Create(issues.NewIssue{Title: "Worker 123 timeout", Priority: "P0", Project: "app", Body: "timeout"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.Create(issues.NewIssue{Title: "Worker 456 timeout", Priority: "P3", Project: "app", Body: "timeout"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Update(prior.ID, issues.Patch{Priority: "P3"}, "test"); err != nil {
		t.Fatal(err)
	}
	old := issueService.Manager
	t.Cleanup(func() { issueService.Manager = old })
	r := &toolRegistry{server: mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil), tools: map[string]bool{}, readers: map[string]probeHandler{}}
	registerIssueTools(r, nil)
	ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: "reader", Role: "reader", Scopes: []string{"issue_get"}})
	v, err := r.readers["issue_get"](ctx, map[string]any{"id": current.ID})
	if err != nil {
		t.Fatal(err)
	}
	got := v.(IssueGetResult)
	if got.Fingerprint == "" || got.RiskFloor != "P0" || len(got.SimilarIncidents) != 1 || len(got.SuggestedActions) != 1 {
		t.Fatal(got)
	}
}
