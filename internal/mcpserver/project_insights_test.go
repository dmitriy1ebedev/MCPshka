package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"mcpshka/internal/issues"
	"mcpshka/internal/knowledge"
)

func TestProjectContextLinksScopedHealthAndKnowledge(t *testing.T) {
	store := withIssueStore(t)
	m, _, in := yieldFixture(t)
	card, err := store.Create(issues.NewIssue{Title: "Project fault", Body: "fixture", Priority: "P1", Project: in.Path}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(issues.NewIssue{Title: "Foreign fault", Body: "fixture", Priority: "P0", Project: "other"}, "test"); err != nil {
		t.Fatal(err)
	}
	ks, err := knowledge.FromDatabase(m.Database())
	if err != nil {
		t.Fatal(err)
	}
	record := knowledge.Record{ID: "project-dependency", Kind: "dependency", Scope: in.Path, Source: "fixture", Provenance: "test", Timestamp: time.Now().UTC(), Fields: map[string]knowledge.Field{"target": {Value: "DIAG", Export: true}, "password": {Value: "private-value", Secret: true}}}
	infra := knowledge.Record{ID: "infra-diag", Kind: "infrastructure", Scope: "infra", Source: "fixture", Provenance: "test", Timestamp: time.Now().UTC(), Fields: map[string]knowledge.Field{"label": {Value: "DIAG", Export: true}}}
	if err := ks.Add(infra); err != nil {
		t.Fatal(err)
	}
	record.DerivedFrom = map[string]uint64{infra.ID: 1}
	if err := ks.Add(record); err != nil {
		t.Fatal(err)
	}
	r := &toolRegistry{server: mcp.NewServer(&mcp.Implementation{Name: "project-test", Version: "1"}, nil), tools: map[string]bool{}, readers: map[string]probeHandler{}}
	registerProjectContext(r, m)
	ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: "reader", Role: "reader", Scopes: []string{"project_context"}})
	value, err := r.readers["project_context"](ctx, map[string]any{"path": in.Path})
	if err != nil {
		t.Fatal(err)
	}
	out := value.(ProjectContext)
	if out.ProjectHealth.State != "degraded" || len(out.ProjectHealth.Incidents) != 1 || out.ProjectHealth.Incidents[0].ID != card.ID || len(out.ProjectHealth.Knowledge) != 1 || out.ProjectHealth.Knowledge[0].ID != record.ID || len(out.ProjectHealth.Knowledge[0].DerivedFrom) != 1 || out.ProjectHealth.Knowledge[0].DerivedFrom[0] != infra.ID {
		t.Fatalf("%+v", out.ProjectHealth)
	}
	raw, _ := json.Marshal(out.ProjectHealth)
	if strings.Contains(string(raw), "private-value") || strings.Contains(string(raw), "target") {
		t.Fatal("record fields leaked into handoff")
	}
	if out.ProjectHealth.IncidentState != "observed" || out.ProjectHealth.KnowledgeState != "observed" {
		t.Fatal(out.ProjectHealth)
	}
}
