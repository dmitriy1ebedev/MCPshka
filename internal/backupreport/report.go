// Package backupreport derives historical recovery evidence from backup receipts.
package backupreport

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"mcpshka/internal/statestore"
	"sort"
	"strings"
	"time"
)

type Metric struct {
	State      string    `json:"state"`
	Value      any       `json:"value"`
	Source     string    `json:"source"`
	ObservedAt time.Time `json:"observed_at"`
}
type Snapshot struct {
	ID         string    `json:"id"`
	State      string    `json:"state"`
	Source     string    `json:"source"`
	ObservedAt time.Time `json:"observed_at"`
}
type Report struct {
	OfflineRestore     *OfflineRestore `json:"offline_restore,omitempty"`
	RecoveryPointAge   Metric          `json:"recovery_point_age_seconds"`
	LastRestoreDrill   Metric          `json:"last_restore_drill"`
	IndependentCopies  Metric          `json:"independent_copies"`
	UnhealthySnapshots Metric          `json:"unhealthy_snapshots"`
	Availability       Metric          `json:"current_remote_availability"`
	// P23.4: bytes the verified snapshots moved to the cloud, how long the
	// latest took from creation to a verified restore, and what is not
	// measured yet (cloud API quota, CPU/IO/temperature cost on H96) stays
	// UNKNOWN instead of guessed.
	TransferredBytes     Metric `json:"transferred_bytes"`
	RestoreVerifySeconds Metric `json:"restore_verify_seconds"`
	CloudQuota           Metric `json:"cloud_api_quota"`
	HostImpact           Metric `json:"host_impact"`
}

func unknown(now time.Time) Metric {
	return Metric{State: "UNKNOWN", Source: "state:backups/receipts", ObservedAt: now.UTC()}
}
func Read(ctx context.Context, db *statestore.DB, now time.Time) (Report, error) {
	if db == nil {
		return FromRecords(nil, now), nil
	}
	records, err := db.ReadSmallPrefixContext(ctx, "backups/")
	return FromRecords(records, now), err
}
func FromRecords(records map[string][]byte, now time.Time) Report {
	out := Report{RecoveryPointAge: unknown(now), LastRestoreDrill: unknown(now), IndependentCopies: unknown(now), UnhealthySnapshots: unknown(now), Availability: unknown(now),
		TransferredBytes: unknown(now), RestoreVerifySeconds: unknown(now), CloudQuota: unknown(now), HostImpact: unknown(now)}
	var latest, drill time.Time
	var moved int64
	verifySeconds := int64(-1)
	issues := []Snapshot{}
	source := "state:backups/receipts"
	drillUncertain := false
	for key, raw := range records {
		id := strings.TrimSuffix(strings.TrimPrefix(key, "backups/"), ".json")
		decoded, err := hex.DecodeString(id)
		if err != nil || len(decoded) != 16 || key != "backups/"+id+".json" {
			continue
		}
		var r struct {
			CiphertextSHA256 string    `json:"ciphertext_sha256"`
			ArchiveSHA256    string    `json:"archive_sha256"`
			State            string    `json:"state"`
			RestoreState     string    `json:"restore_state"`
			CreatedAt        time.Time `json:"created_at"`
			VerifiedAt       time.Time `json:"restore_verified_at"`
			Records          int64     `json:"restored_records"`
			FileID           string    `json:"file_id"`
			Size             int64     `json:"size_bytes"`
			HTTPStatus       int       `json:"http_status"`
		}
		state := ""
		if json.Unmarshal(raw, &r) != nil {
			state = "CORRUPTED_RECEIPT"
		} else {
			switch {
			case r.HTTPStatus == 404 || r.HTTPStatus == 410:
				state = "UNAVAILABLE"
			case r.State == "failed" || r.RestoreState == "failed":
				state = "FAILED"
			case r.State != "verified":
				state = "UNVERIFIED"
			}
		}
		if r.RestoreState == "failed" || r.RestoreState == "running" {
			drillUncertain = true
		}
		valid := state == "" && !r.CreatedAt.IsZero() && !r.CreatedAt.After(now) && !r.VerifiedAt.Before(r.CreatedAt) && !r.VerifiedAt.After(now) && r.Records > 0 && r.FileID != "" && r.Size > 0
		if !valid && state == "" {
			state = "UNVERIFIED"
		}
		if state != "" {
			issues = append(issues, Snapshot{id, state, "state:" + key, now.UTC()})
			continue
		}
		moved += r.Size
		if r.CreatedAt.After(latest) {
			latest = r.CreatedAt
			verifySeconds = int64(r.VerifiedAt.Sub(r.CreatedAt) / time.Second)
			out.OfflineRestore = nil
			if validDigest(r.CiphertextSHA256) && validDigest(r.ArchiveSHA256) && r.Size <= offlineLimit {
				out.OfflineRestore = &OfflineRestore{Schema: "mcpshka.offline-restore/v1", ReceiptRef: "state:" + key, FileID: r.FileID, CiphertextSHA256: r.CiphertextSHA256, ArchiveSHA256: r.ArchiveSHA256, SizeBytes: r.Size, SecretsProfileRequired: true, Steps: []string{"Скачать шифротекст по file_id с разрешённой rescue-машины", "Сверить размер и SHA-256 шифротекста", "Расшифровать отдельно разрешённым offline-ключом; ключ не экспортировать", "VerifyOffline: проверить хеш архива, пути, лимиты и обязательные файлы", "Проверить целостность SQLite и контрольные суммы записей в изолированном staging", "Production replay оставить на HOLD до отдельного разрешения и writer fence"}}
			}
		}
		if r.VerifiedAt.After(drill) {
			drill = r.VerifiedAt
		}
	}
	if !latest.IsZero() {
		out.RecoveryPointAge = Metric{"OBSERVED", int64(now.Sub(latest) / time.Second), source, now.UTC()}
		// All state-backup receipts use the same pinned Drive destination.
		out.IndependentCopies = Metric{"OBSERVED", 1, source, now.UTC()}
		out.TransferredBytes = Metric{"OBSERVED", moved, source, now.UTC()}
		out.RestoreVerifySeconds = Metric{"OBSERVED", verifySeconds, source, now.UTC()}
	}
	if !drill.IsZero() && !drillUncertain {
		out.LastRestoreDrill = Metric{"OBSERVED", drill, source, now.UTC()}
	}
	if len(records) > 0 {
		sort.Slice(issues, func(i, j int) bool { return issues[i].ID < issues[j].ID })
		out.UnhealthySnapshots = Metric{"OBSERVED", issues, source, now.UTC()}
	}
	return out
}
