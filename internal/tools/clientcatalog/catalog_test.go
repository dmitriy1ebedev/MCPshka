package clientcatalog

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newClientCatalog(path string) *Catalog { return New(Deps{Path: path, Now: time.Now}) }

func TestClientCatalogFindsWhatAStaleClientCannotSee(t *testing.T) {
	clock := time.Now
	newClientCatalog := func(path string) *Catalog { return New(Deps{Path: path, Now: func() time.Time { return clock() }}) }
	path := filepath.Join(t.TempDir(), "client-catalog.json")
	c := newClientCatalog(path)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	clock = func() time.Time { return now.Add(time.Hour) }
	// Two pages of one listing, then a second client with everything.
	c.observeSurface("", "s1", "openai-mcp", "1.0", true, false, []string{"exec_start", "job_output"}, now)
	c.observeSurface("", "s1", "openai-mcp", "1.0", false, true, []string{"fs_read"}, now)
	c.observeSurface("", "s2", "claude-code", "2.1", true, true, []string{"exec_start", "fs_read", "job_output", "tool_invoke"}, now)
	current := []string{"exec_start", "fs_read", "job_output", "tool_invoke"}

	st := newClientCatalog(path).Status(current) // reloaded from disk
	if len(st.Clients) != 2 || !strings.Contains(st.NextAction, "refresh") {
		t.Fatal(st)
	}
	chat, claude := st.Clients[1], st.Clients[0]
	if chat.Client != "openai-mcp" || !chat.Stale || chat.MissingCount != 1 || chat.Missing[0] != "tool_invoke" || chat.ToolsSeen != 3 {
		t.Fatalf("stale client: %+v", chat)
	}
	if claude.Stale || claude.Lists != 1 {
		t.Fatalf("complete client: %+v", claude)
	}
	// MCP-181: a client silent for days keeps its record but stops asking
	// for a refresh and no longer drives the catalog health.
	clock = func() time.Time { return now.Add(clientCatalogFresh + time.Hour) }
	old := newClientCatalog(path).Status(current)
	if !old.Clients[1].Historical || !old.Clients[1].Stale || old.Clients[1].ClientRefreshNeeded || strings.Contains(old.NextAction, "refresh") {
		t.Fatalf("a long-silent client still drives the catalog: %+v", old)
	}
	if h := newClientCatalog(path).Health(current, nil, "m"); h.ClientRefreshNeeded != nil || h.ExposedCount != nil {
		t.Fatalf("historical clients must not count in health: %+v", h)
	}
	clock = func() time.Time { return now.Add(time.Hour) }
	// A refreshed connector clears the finding.
	c.observeSurface("", "s3", "openai-mcp", "1.1", true, true, current, now.Add(time.Hour))
	if st := c.Status(current); st.Clients[1].Stale || st.Clients[1].Lists != 2 || st.Clients[1].Version != "1.1" {
		t.Fatalf("refresh not recorded: %+v", st.Clients[1])
	}
}

func TestCatalogHealthHasBoundedClientSummary(t *testing.T) {
	c := newClientCatalog("")
	names := []string{"backup_verify", "health_snapshot"}
	for i := 0; i < 20; i++ {
		c.observeSurface("", string(rune('a'+i)), string(rune('a'+i)), "1", true, true, []string{"health_snapshot"}, time.Now(), map[string]string{"health_snapshot": "old"})
	}
	h := c.Health(names, map[string]string{"backup_verify": "new", "health_snapshot": "new"}, "manifest").Evaluate()
	if !h.Truncated || h.State != "degraded" || h.ExposedCount == nil || *h.ExposedCount != 1 || h.SchemaMismatch == nil || !*h.SchemaMismatch || len(h.MissingActions) != 1 || h.MissingActions[0] != "backup_verify" {
		t.Fatal(h)
	}
}
