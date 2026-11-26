package backupreport

import (
	"context"
	"encoding/json"
	"mcpshka/internal/statestore"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReceiptMetricsKeepUnknownAndCountDestinations(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	empty := FromRecords(nil, now)
	if empty.RecoveryPointAge.State != "UNKNOWN" || empty.IndependentCopies.Value != nil || empty.LastRestoreDrill.State != "UNKNOWN" {
		t.Fatal(empty)
	}
	receipt := func(age time.Duration, state string) []byte {
		raw, err := json.Marshal(map[string]any{"state": state, "created_at": now.Add(-age), "restore_verified_at": now.Add(-age + time.Minute), "restored_records": 10, "file_id": "private", "size_bytes": 123})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	records := map[string][]byte{}
	records["backups/"+strings.Repeat("a", 32)+".json"] = receipt(time.Hour, "verified")
	records["backups/"+strings.Repeat("b", 32)+".json"] = receipt(2*time.Hour, "verified")
	records["backups/"+strings.Repeat("c", 32)+".json"] = []byte("{")
	records["backups/"+strings.Repeat("d", 32)+".json"] = []byte(`{"state":"failed","http_status":404}`)
	out := FromRecords(records, now)
	if out.RecoveryPointAge.Value != int64(3600) || out.IndependentCopies.Value != 1 || out.LastRestoreDrill.State != "OBSERVED" || out.Availability.State != "UNKNOWN" {
		t.Fatal(out)
	}
	if out.TransferredBytes.Value != int64(246) || out.RestoreVerifySeconds.Value != int64(60) || out.CloudQuota.State != "UNKNOWN" || out.HostImpact.State != "UNKNOWN" {
		t.Fatal("P23.4 metrics", out.TransferredBytes, out.RestoreVerifySeconds, out.CloudQuota, out.HostImpact)
	}
	issues := out.UnhealthySnapshots.Value.([]Snapshot)
	if len(issues) != 2 || issues[0].State != "CORRUPTED_RECEIPT" || issues[1].State != "UNAVAILABLE" {
		t.Fatal(issues)
	}
	for _, m := range []Metric{out.RecoveryPointAge, out.LastRestoreDrill, out.IndependentCopies, out.UnhealthySnapshots, out.Availability} {
		if m.Source == "" || !m.ObservedAt.Equal(now) {
			t.Fatal(m)
		}
	}
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	for key, raw := range records {
		if err := db.Put(key, raw); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Read(context.Background(), db, now)
	if err != nil || got.IndependentCopies.Value != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	records["backups/"+strings.Repeat("e", 32)+".json"] = []byte(`{"state":"verified","restore_state":"failed"}`)
	if FromRecords(records, now).LastRestoreDrill.State != "UNKNOWN" {
		t.Fatal("failed drill hidden")
	}
	future := map[string][]byte{"backups/" + strings.Repeat("a", 32) + ".json": receipt(-time.Hour, "verified")}
	if FromRecords(future, now).RecoveryPointAge.State != "UNKNOWN" {
		t.Fatal("future receipt trusted")
	}
}
