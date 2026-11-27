package mcpserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/audit"
	"mcpshka/internal/authn"
	"mcpshka/internal/knowledgeops"
	"mcpshka/internal/searchindex"
	"mcpshka/internal/selfheal"
)

func TestIndexRepairTickShowsVerifiedResultThroughExistingTools(t *testing.T) {
	m, _, _ := yieldFixture(t)
	db := m.Database()
	old := knowledgeops.IndexHealth{Trust: "UNTRUSTED", Health: searchindex.Health{Ready: true}, SourceRevision: "stale", CapturedAt: time.Now().Add(-time.Hour)}
	raw, _ := json.Marshal(old)
	if err := db.Put("searchindex/v1/health", raw); err != nil {
		t.Fatal(err)
	}
	log, err := audit.New(filepath.Join(t.TempDir(), "audit.jsonl"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	docker := &statusOnlyDocker{}
	s := selfheal.New(db, docker, log, selfheal.Config{Enabled: true, MaxRepairs: 2})
	repair := knowledgeops.NewIndexRepairer(m, s, func() (bool, string) { return true, "" })
	phase, err := repair.Tick(context.Background())
	if err != nil || phase != "recovered" {
		t.Fatal(phase, err)
	}
	health, err := knowledgeops.IndexStatus(context.Background(), m)
	if err != nil || !health.Ready || health.SourceRevision == "stale" {
		t.Fatal(health, err)
	}
	if phase, err = repair.Tick(context.Background()); err != nil || phase != "THROTTLED" {
		t.Fatal(phase, err)
	}
	r := &toolRegistry{server: mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil), tools: map[string]bool{}, readers: map[string]probeHandler{}}
	registerSelfHealTool(r, s)
	registerOwnerPortal(r, nil, db)
	registerToolUsage(r, db)
	ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: "reader", Role: "reader", Scopes: []string{"self_heal_status", "owner_portal", "tool_usage"}})
	v, err := r.readers["self_heal_status"](ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range v.(SelfHealStatusResult).Incidents {
		if e.Result == "recovered" {
			found = true
		}
	}
	if !found {
		t.Fatal("no verified repair incident", v)
	}
	v, err = r.readers["owner_portal"](ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, e := range v.(OwnerPortalResult).Events {
		if e.Source == "selfheal/index_health" && e.Ref != nil && e.NextSafeAction == "self_heal_status" {
			found = true
		}
	}
	if !found || docker.calls != 0 {
		t.Fatal("missing notification or host mutation", v, docker.calls)
	}
	v, err = r.readers["tool_usage"](ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	w := v.(ToolUsageResult).Work.Outcomes.Windows[0]
	if w.Incidents != 1 || w.RecoveryAttempts != 1 || w.RecoveryRate == nil || *w.RecoveryRate != 1 {
		t.Fatal("real repair missing from metrics", w)
	}
}

func TestIndexRepairBusyAndDisabledCannotWrite(t *testing.T) {
	m, _, _ := yieldFixture(t)
	db := m.Database()
	before := []byte("corrupt derived receipt")
	if err := db.Put("searchindex/v1/health", before); err != nil {
		t.Fatal(err)
	}
	s := selfheal.New(db, &statusOnlyDocker{}, nil, selfheal.Config{Enabled: true})
	repair := knowledgeops.NewIndexRepairer(m, s, func() (bool, string) { return false, "host busy" })
	if phase, err := repair.Tick(context.Background()); err != nil || phase != "WAIT_NATIVE" {
		t.Fatal(phase, err)
	}
	raw, _ := db.Get("searchindex/v1/health")
	if string(raw) != string(before) {
		t.Fatal("busy host caused mutation")
	}
	disabled := knowledgeops.NewIndexRepairer(m, selfheal.New(db, &statusOnlyDocker{}, nil, selfheal.Config{}), func() (bool, string) { t.Fatal("disabled probed host"); return true, "" })
	if phase, err := disabled.Tick(context.Background()); err != nil || phase != "DISABLED" {
		t.Fatal(phase, err)
	}
}
