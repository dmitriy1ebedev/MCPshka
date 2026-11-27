package mcpserver

import (
	"context"
	"encoding/json"
	"mcpshka/internal/healthlayer"
	"mcpshka/internal/jobs"
	"mcpshka/internal/knowledgeops"
	"mcpshka/internal/storagefabric"
	"time"
)

func providerHealthProbes(r *toolRegistry, m *jobs.Manager, run healthBrokerRunner) []healthlayer.Probe {
	return []healthlayer.Probe{
		{Name: "storage_backup", NextSafeAction: healthAction("storage_providers", map[string]any{}), Read: func(ctx context.Context) (healthlayer.Sample, error) {
			// The aggregate contains counts/states only, no owners, paths or credentials.
			raw, err := run(ctx, "storage", `{"action":"health","owner":"health_snapshot"}`)
			var result storagefabric.Result
			h := storagefabric.Health{State: "unknown", BackupFreshness: "unknown", StorageAuth: map[string]string{}, ProviderHealth: map[string]string{}}
			if err == nil && len(raw) <= 8192 && json.Unmarshal(raw, &result) == nil && len(result.Entries) <= healthlayer.MaxProbeBytes {
				var observed storagefabric.Health
				if json.Unmarshal(result.Entries, &observed) == nil && !observed.CapturedAt.IsZero() {
					h = observed
				}
			}
			if m != nil && m.Database() != nil {
				if err := mergeStateBackupHealth(ctx, m.Database(), &h, time.Now().UTC()); err != nil {
					return healthlayer.Sample{}, err
				}
			}
			sample, err := healthSample(h.State, h)
			sample.ObservedAt = h.CapturedAt
			return sample, err
		}},
		{Name: "capability_catalog", NextSafeAction: healthAction("client_catalog_status", map[string]any{}), Read: func(ctx context.Context) (healthlayer.Sample, error) {
			view := r.snapshotRegistry()
			h := r.clients.Health(view.exposedNames(), view.schemas(), view.revision)
			index, err := knowledgeops.IndexStatus(ctx, m)
			h.IndexStaleness = "unknown"
			if err == nil && !index.CapturedAt.IsZero() {
				h.IndexRevision = index.SourceRevision
				h.IndexStaleness = "stale"
				if index.Ready && !index.CapturedAt.After(time.Now().Add(time.Second)) {
					h.IndexStaleness = "current"
				}
			}
			h = h.Evaluate()
			return healthSample(h.State, h)
		}},
	}
}
