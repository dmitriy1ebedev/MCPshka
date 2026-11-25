package health

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"mcpshka/internal/statestore"
	"mcpshka/internal/storagefabric"
)

// mergeStateBackupHealth reads receipts only; it never invokes the backup helper.
func MergeStateBackup(ctx context.Context, db *statestore.DB, h *storagefabric.Health, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	records, err := db.ReadSmallPrefixContext(ctx, "backups/")
	if err != nil {
		return err
	}
	var latest, drill time.Time
	for key, raw := range records {
		id := strings.TrimSuffix(strings.TrimPrefix(key, "backups/"), ".json")
		decoded, err := hex.DecodeString(id)
		if err != nil || len(decoded) != 16 || key != "backups/"+id+".json" {
			continue
		}
		var receipt struct {
			State             string    `json:"state"`
			CreatedAt         time.Time `json:"created_at"`
			RestoreVerifiedAt time.Time `json:"restore_verified_at"`
			RestoredRecords   int64     `json:"restored_records"`
			FileID            string    `json:"file_id"`
			SizeBytes         int64     `json:"size_bytes"`
		}
		if json.Unmarshal(raw, &receipt) != nil || receipt.State != "verified" ||
			receipt.CreatedAt.IsZero() || receipt.CreatedAt.After(now) ||
			receipt.RestoreVerifiedAt.Before(receipt.CreatedAt) || receipt.RestoreVerifiedAt.After(now) ||
			receipt.RestoredRecords <= 0 || receipt.FileID == "" || receipt.SizeBytes <= 0 {
			continue
		}
		if receipt.CreatedAt.After(latest) {
			latest = receipt.CreatedAt
		}
		if receipt.RestoreVerifiedAt.After(drill) {
			drill = receipt.RestoreVerifiedAt
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	count := 0
	if h.VerifiedReplicaCount != nil {
		count = *h.VerifiedReplicaCount
	}
	if !latest.IsZero() {
		// Historical snapshots on the same Drive destination are one replica.
		count++
		age := int64(now.Sub(latest) / time.Second)
		if h.BackupAgeSeconds == nil || age < *h.BackupAgeSeconds {
			h.BackupAgeSeconds = &age
		}
		if h.LastRestoreDrill == nil || drill.After(*h.LastRestoreDrill) {
			h.LastRestoreDrill = &drill
		}
	}
	h.VerifiedReplicaCount = &count
	h.BackupFreshness = "unknown"
	if h.BackupAgeSeconds != nil {
		h.BackupFreshness = "ok"
		if *h.BackupAgeSeconds > int64(26*time.Hour/time.Second) {
			h.BackupFreshness = "degraded"
			h.State = "degraded"
		}
	}
	h.CapturedAt = now
	return nil
}
