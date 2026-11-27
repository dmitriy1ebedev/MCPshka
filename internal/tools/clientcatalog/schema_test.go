package clientcatalog

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClientCatalogDetectsSameNameSchemaDrift(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.json")
	c := newClientCatalog(path)
	names := []string{"project_test"}
	c.observeSurface("", "session", "codex", "1", true, true, names, time.Now(), map[string]string{"project_test": "old"})
	current := map[string]string{"project_test": "new"}
	view := newClientCatalog(path).Status(names, current).Clients[0]
	if view.SchemaMismatch == nil || !*view.SchemaMismatch || !view.Stale || !view.ClientRefreshNeeded || view.MissingCount != 0 {
		t.Fatal("same-name drift hidden", view)
	}
	c.observeSurface("", "refresh", "codex", "1", true, true, names, time.Now(), current)
	view = c.Status(names, current).Clients[0]
	if view.SchemaMismatch == nil || *view.SchemaMismatch || view.Stale {
		t.Fatal("refresh not proven", view)
	}
}

func TestClientCatalogOrphanPageCannotReplaceCompleteEvidence(t *testing.T) {
	c := newClientCatalog("")
	c.observeSurface("", "new-session", "codex", "1", false, true, []string{"project_test"}, time.Now(), map[string]string{"project_test": "schema"})
	if len(c.Status([]string{"project_test"}).Clients) != 0 {
		t.Fatal("orphan page became a complete listing")
	}
}

func TestClientCatalogLegacySchemaEvidenceStaysUnknown(t *testing.T) {
	c := newClientCatalog("")
	names := []string{"project_test"}
	c.observeSurface("", "session", "legacy", "1", true, true, names, time.Now())
	view := c.Status(names, map[string]string{"project_test": "new"}).Clients[0]
	if view.SchemaMismatch != nil {
		t.Fatal("legacy names claimed schema evidence", view)
	}
}

func TestClientCatalogNamesNewToolsAndChangedSchemas(t *testing.T) {
	e := &ClientCatalogEntry{Tools: []string{"fs_read", "project_test"}, Schemas: map[string]string{"fs_read": "v1", "project_test": "old"}}
	names := []string{"fs_read", "issue_get", "issue_list", "project_test"}
	summary := changedCatalogSummary(e, names, map[string]string{"fs_read": "v1", "issue_get": "v2", "issue_list": "v2", "project_test": "new"})
	for _, want := range []string{"new tools (2): issue_get, issue_list", "changed schemas (1): project_test"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("catalog did not name stale actions: %s", summary)
		}
	}
}
