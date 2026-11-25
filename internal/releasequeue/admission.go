package releasequeue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"mcpshka/internal/brokerclient"
	"mcpshka/internal/jobs"
)

// AdmitEnvironment checks the job registry and binds the running pair to the rollback version.
func AdmitEnvironment(ctx context.Context, m *jobs.Manager, controller, version string) error {
	activity := m.RegistrySummary(1)
	if activity.Count != 0 || activity.UnknownCount != 0 || activity.Truncated {
		return fmt.Errorf("host jobs active=%d, UNKNOWN=%d; inspect the original jobs with job_wait", activity.Count-activity.UnknownCount, activity.UnknownCount)
	}
	raw, err := brokerclient.Run(ctx, "health", "broker")
	var identity struct {
		Version string `json:"version"`
		SHA     string `json:"build_sha256"`
	}
	if err != nil || json.Unmarshal(raw, &identity) != nil || len(identity.SHA) != 64 {
		return errors.New("running broker identity unavailable")
	}
	if version == "" || version != controller || identity.Version != version {
		return fmt.Errorf("running broker %s, controller %s, rollback checkpoint %s must agree", identity.Version, controller, version)
	}
	return nil
}
