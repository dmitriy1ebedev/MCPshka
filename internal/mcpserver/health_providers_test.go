package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/knowledge"
	"mcpshka/internal/knowledgeops"
	"mcpshka/internal/searchindex"
	"mcpshka/internal/storagefabric"
)

func TestBackupHealthDoesNotTurnMissingAuthorityGreen(t *testing.T) {
	for _, missing := range []bool{false, true} {
		run := func(ctx context.Context, action string, args ...string) ([]byte, error) {
			if action != "storage" || len(args) != 1 {
				t.Fatal("unexpected probe", action, args)
			}
			if missing {
				return nil, errors.New("private error")
			}
			h := storagefabric.Health{State: "unknown", CapturedAt: time.Now(), BackupFreshness: "unknown"}
			raw, _ := json.Marshal(h)
			return json.Marshal(storagefabric.Result{Entries: raw})
		}
		p := providerHealthProbes(nil, nil, run)[0]
		sample, err := p.Read(context.Background())
		var fields map[string]any
		json.Unmarshal(sample.Data, &fields)
		if err != nil || sample.State != "unknown" {
			t.Fatal(sample, err)
		}
		for _, key := range []string{"backup_freshness", "verified_replica_count", "last_restore_drill", "storage_auth", "storage_quota", "provider_health", "failed_uploads"} {
			if _, ok := fields[key]; !ok {
				t.Fatal("missing explicit signal", key)
			}
		}
	}
}

func TestCapabilityHealthCannotAcceptFutureIndexTimestamp(t *testing.T) {
	m, _, _ := yieldFixture(t)
	store, err := knowledge.FromDatabaseContext(context.Background(), m.Database())
	if err != nil {
		t.Fatal(err)
	}
	revision, err := store.Revision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h := knowledgeops.IndexHealth{SourceRevision: revision, CapturedAt: time.Now().Add(time.Hour), Health: searchindex.Health{Ready: true}}
	raw, _ := json.Marshal(h)
	if err := m.Database().Put("searchindex/v1/health", raw); err != nil {
		t.Fatal(err)
	}
	r := &toolRegistry{clients: newClientCatalog(""), catalog: map[string]*mcp.Tool{}}
	sample, err := providerHealthProbes(r, m, nil)[1].Read(context.Background())
	var fields map[string]any
	json.Unmarshal(sample.Data, &fields)
	if err != nil || fields["index_staleness"] != "stale" {
		t.Fatal("future index called current", fields, err)
	}
}
