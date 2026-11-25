package processtools

import (
	"time"

	"mcpshka/internal/jobs"
	"mcpshka/internal/toolcontract"
)

type JobStatusInput struct {
	JobID   string `json:"job_id" jsonschema:"Job identifier returned by exec_start."`
	Verbose bool   `json:"verbose,omitempty" jsonschema:"Return all internal status fields, including defaults."`
}

type JobWaitInput struct {
	JobID          string `json:"job_id" jsonschema:"Job identifier returned by exec_start."`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"Wait budget in seconds; any value is accepted and capped at 45 s, zero returns immediately. Repeat while running."`
}

type JobWaitResult struct {
	Status map[string]any `json:"status"`
	Stdout string         `json:"stdout,omitempty"`
	Stderr string         `json:"stderr,omitempty"`
}

// JobStatusView presents the public, compact job status while keeping verbose
// detail available for controlled diagnostics.
func JobStatusView(s jobs.Snapshot, verbose bool) (map[string]any, error) {
	out, err := toolcontract.Object(s)
	if err != nil {
		return nil, err
	}
	if verbose {
		return out, nil
	}
	delete(out, "boot_id")
	delete(out, "start_ticks")
	for key, value := range map[string]time.Time{
		"started_at":     s.StartedAt,
		"finished_at":    s.FinishedAt,
		"last_output_at": s.LastOutputAt,
		"reconciled_at":  s.ReconciledAt,
	} {
		if value.IsZero() {
			delete(out, key)
		}
	}
	for key, value := range map[string]int64{
		"timeout_seconds":        int64(jobs.DefaultJobTimeout / time.Second),
		"output_limit_bytes":     jobs.DefaultJobOutputLimit,
		"memory_limit_bytes":     jobs.DefaultMemoryLimit,
		"cpu_time_limit_seconds": int64(jobs.DefaultCPUTimeLimit / time.Second),
		"process_limit":          jobs.DefaultProcessLimit,
		"cpu_quota_percent":      jobs.DefaultCPUQuotaPercent,
	} {
		if out[key] == float64(value) {
			delete(out, key)
		}
	}
	return out, nil
}
