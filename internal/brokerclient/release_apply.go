package brokerclient

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"mcpshka/internal/releaseapply"
)

// Reconcile a lost apply reply; never submit the mutation again.
func recoverReleaseApply(ctx context.Context, version string, since time.Time) ([]byte, error) {
	wait, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	for {
		if raw, ok := releaseapply.Result(version, since); ok {
			return raw, nil
		}
		probe, stop := context.WithTimeout(wait, 3*time.Second)
		raw, err := Run(probe, "release-apply-status", version)
		stop()
		var report struct {
			State      string    `json:"state"`
			Version    string    `json:"version"`
			ReportedAt time.Time `json:"reported_at"`
		}
		if err == nil && json.Unmarshal(raw, &report) == nil && report.Version == version && report.State != "NOT_STARTED" && !report.ReportedAt.Before(since.Add(-time.Second)) {
			return raw, nil
		}
		select {
		case <-wait.Done():
			return json.Marshal(map[string]string{"state": "HOLD", "version": version, "unit": "mcpshka-release-apply-" + strings.ReplaceAll(version, ".", ""), "reason": "apply response lost; no durable result observed within recovery window; inspect release_apply_status"})
		case <-time.After(time.Second):
		}
	}
}
