package storagefabric

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Health struct {
	State                string            `json:"state"`
	CapturedAt           time.Time         `json:"captured_at"`
	BackupFreshness      string            `json:"backup_freshness"`
	BackupAgeSeconds     *int64            `json:"backup_age_seconds"`
	VerifiedReplicaCount *int              `json:"verified_replica_count"`
	LastRestoreDrill     *time.Time        `json:"last_restore_drill"`
	StorageAuth          map[string]string `json:"storage_auth"`
	StorageQuota         *int64            `json:"storage_quota"`
	ProviderHealth       map[string]string `json:"provider_health"`
	FailedUploads        *int              `json:"failed_uploads"`
}

// Health reads at most 1000 local journal entries. No remote quota/auth probes,
// directory creation, credential refresh, restore or periodic work is performed.
func (e Engine) Health(ctx context.Context, now time.Time) (Health, error) {
	h := Health{State: "unknown", CapturedAt: now, BackupFreshness: "unknown", StorageAuth: map[string]string{}, ProviderHealth: map[string]string{}}
	for _, p := range e.providers().Providers {
		if p.ID == "iclouddrive" {
			continue
		}
		h.StorageAuth[p.ID] = p.Authenticated
		h.ProviderHealth[p.ID] = "unknown"
	}
	if err := privateDir(e.Dir); err != nil {
		return h, err
	}
	dir, err := os.Open(e.Dir)
	if err != nil {
		return h, err
	}
	entries, err := dir.ReadDir(1001)
	dir.Close()
	if err != nil && !errors.Is(err, io.EOF) || len(entries) > 1000 {
		return h, errors.New("storage health journal budget exceeded")
	}
	var latest time.Time
	digest := ""
	verified := []operation{}
	failures := 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return h, err
		}
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := privateFile(filepath.Join(e.Dir, entry.Name()))
		if err != nil {
			return h, err
		}
		var op operation
		if json.Unmarshal(raw, &op) != nil || !idPattern.MatchString(op.Result.ID) {
			return h, ErrInvalid
		}
		if (op.Request.Action == "upload" || op.Request.Action == "backup_create") && (op.Result.State == "failed" || op.Result.State == "UNKNOWN") {
			failures++
		}
		w := op.Result.Workflow
		if op.Request.Action != "backup_create" || op.Result.State != "verified" || w == nil || w.Schema != BackupSchema || w.Phase != "verified" || w.UpdatedAt.IsZero() || w.UpdatedAt.After(now) || len(op.Result.SHA256) != 64 {
			continue
		}
		verified = append(verified, op)
		if w.UpdatedAt.After(latest) {
			latest = w.UpdatedAt
			digest = op.Result.SHA256
		}
	}
	count := 0
	providers := map[string]bool{}
	for _, op := range verified {
		if op.Result.SHA256 == digest && !providers[op.Request.Provider] {
			providers[op.Request.Provider] = true
			count++
		}
	}
	h.VerifiedReplicaCount = &count
	h.FailedUploads = &failures
	if !latest.IsZero() {
		age := int64(now.Sub(latest) / time.Second)
		h.BackupAgeSeconds = &age
		h.BackupFreshness = "fresh"
		if now.Sub(latest) > 24*time.Hour {
			h.BackupFreshness = "stale"
			h.State = "stale"
		}
	}
	if failures > 0 {
		h.State = "degraded"
	}
	return h, nil
}
