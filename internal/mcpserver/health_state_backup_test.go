package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/storagefabric"
)

func TestStateBackupHealthReceipts(t *testing.T) {
	now := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, state, freshness string
		age                    time.Duration
		invalid                string
		wantCount              int
	}{
		{"daily", "verified", "ok", 5 * time.Hour, "", 1},
		{"boundary", "verified", "ok", 26 * time.Hour, "", 1},
		{"old", "verified", "degraded", 26*time.Hour + time.Second, "", 1},
		{"pending", "uploaded", "unknown", time.Hour, "", 0},
		{"future", "verified", "unknown", -time.Hour, "", 0},
		{"bad_timestamp", "verified", "unknown", time.Hour, "created_at", 0},
		{"no_restore", "verified", "unknown", time.Hour, "restore_verified_at", 0},
		{"no_file", "verified", "unknown", time.Hour, "file_id", 0},
		{"empty_restore", "verified", "unknown", time.Hour, "restored_records", 0},
		{"empty_size", "verified", "unknown", time.Hour, "size_bytes", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _, _ := yieldFixture(t)
			created := now.Add(-tc.age)
			receipt := map[string]any{"state": tc.state, "created_at": created, "restore_verified_at": created.Add(time.Second), "restored_records": 142, "file_id": "private-drive-file", "size_bytes": 12345}
			if tc.invalid != "" {
				delete(receipt, tc.invalid)
			}
			raw, _ := json.Marshal(receipt)
			key := "backups/692fe511000000000000000000000000.json"
			if err := m.Database().Put(key, raw); err != nil {
				t.Fatal(err)
			}
			h := storagefabric.Health{State: "unknown"}
			if err := mergeStateBackupHealth(context.Background(), m.Database(), &h, now); err != nil {
				t.Fatal(err)
			}
			if h.BackupFreshness != tc.freshness || h.VerifiedReplicaCount == nil || *h.VerifiedReplicaCount != tc.wantCount {
				t.Fatalf("%+v", h)
			}
			if tc.wantCount == 1 {
				if h.LastRestoreDrill == nil || !h.LastRestoreDrill.Equal(created.Add(time.Second)) || *h.BackupAgeSeconds != int64(tc.age/time.Second) {
					t.Fatalf("%+v", h)
				}
			} else if h.LastRestoreDrill != nil || h.BackupAgeSeconds != nil {
				t.Fatalf("invalid receipt accepted: %+v", h)
			}
			after, err := m.Database().Get(key)
			if err != nil || string(after) != string(raw) {
				t.Fatal("receipt changed", err)
			}
		})
	}
}

func TestStateBackupProbeMergesWithoutBrokerAndRedacts(t *testing.T) {
	m, _, _ := yieldFixture(t)
	now := time.Now().UTC()
	for i, age := range []time.Duration{time.Hour, 48 * time.Hour} {
		raw := []byte(fmt.Sprintf(`{"state":"verified","created_at":%q,"restore_verified_at":%q,"restored_records":142,"file_id":"private-drive-id","size_bytes":12345}`, now.Add(-age).Format(time.RFC3339), now.Add(-age+time.Second).Format(time.RFC3339)))
		if err := m.Database().Put(fmt.Sprintf("backups/%032x.json", i), raw); err != nil {
			t.Fatal(err)
		}
	}
	run := func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("broker unavailable") }
	sample, err := providerHealthProbes(nil, m, run)[0].Read(context.Background())
	var h storagefabric.Health
	if err != nil || json.Unmarshal(sample.Data, &h) != nil || h.BackupFreshness != "ok" || h.VerifiedReplicaCount == nil || *h.VerifiedReplicaCount != 1 || h.LastRestoreDrill == nil {
		t.Fatal(sample, err)
	}
	if strings.Contains(string(sample.Data), "private-drive") || strings.Contains(string(sample.Data), "backups/") {
		t.Fatal("private receipt leaked")
	}
	count, age := 2, int64(48*time.Hour/time.Second)
	h = storagefabric.Health{State: "degraded", VerifiedReplicaCount: &count, BackupAgeSeconds: &age}
	if err := mergeStateBackupHealth(context.Background(), m.Database(), &h, now); err != nil || *h.VerifiedReplicaCount != 3 || h.BackupFreshness != "ok" || h.State != "degraded" {
		t.Fatal(h, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := mergeStateBackupHealth(ctx, m.Database(), &h, now); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
