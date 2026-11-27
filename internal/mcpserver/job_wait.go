package mcpserver

import (
	"context"
	"errors"
	"strings"
	"time"

	"mcpshka/internal/jobs"
	"mcpshka/internal/tools/processtools"
)

type JobStatusInput = processtools.JobStatusInput
type JobWaitInput = processtools.JobWaitInput
type JobWaitResult = processtools.JobWaitResult

func jobStatusView(s jobs.Snapshot, verbose bool) (map[string]any, error) {
	return processtools.JobStatusView(s, verbose)
}

// maxJobWaitSeconds keeps one job_wait inside common client call timeouts.
const maxJobWaitSeconds = 45

func waitJob(ctx context.Context, m *jobs.Manager, id string, seconds int) (JobWaitResult, error) {
	// An oversized wait is clamped, not refused: the refusal looked like a
	// blocker to clients waiting on a long test (MCP-117).
	if seconds < 0 {
		return JobWaitResult{}, errors.New("timeout_seconds must not be negative")
	}
	// MCP clients abort a call at about 60 s (Claude Code timed out at 60,
	// 90 and 120 on 01.11), losing the answer; a shorter wait just repolls.
	if seconds > maxJobWaitSeconds {
		seconds = maxJobWaitSeconds
	}
	if err := ctx.Err(); err != nil {
		return JobWaitResult{}, err
	}
	s, err := m.Get(id)
	if err != nil {
		return JobWaitResult{}, err
	}
	initial := s.State
	timer := time.NewTimer(time.Duration(seconds) * time.Second)
	defer timer.Stop()
	// A local job signals its next phase (MCP-273: each read queries the
	// systemd scope); the slow tick only backs it up. Jobs without a signal
	// are polled as before.
	changed := m.Changed(id)
	tick := func() time.Duration {
		if changed != nil {
			return 5 * time.Second
		}
		return 500 * time.Millisecond
	}
	ticker := time.NewTicker(tick())
	defer ticker.Stop()
wait:
	for seconds > 0 && s.State == initial && (s.State == jobs.Queued || s.State == jobs.Running || s.State == jobs.Cancelling) {
		select {
		case <-ctx.Done():
			return JobWaitResult{}, ctx.Err()
		case <-timer.C:
			break wait
		case <-changed:
			if next := m.Changed(id); next != changed {
				changed = next
			} else {
				changed = nil // closed and not replaced: fall back to polling
			}
			ticker.Reset(tick())
		case <-ticker.C:
		}
		s, err = m.Get(id)
		if err != nil {
			return JobWaitResult{}, err
		}
	}
	out := JobWaitResult{}
	out.Status, err = jobStatusView(s, false)
	if err != nil {
		return out, err
	}
	// Split the shared 2 KiB budget so stderr cannot hide stdout or vice versa.
	for _, stream := range []string{"stdout", "stderr"} {
		tail, err := m.Read(id, stream, -1024, 1024)
		if err != nil {
			return out, err
		}
		if stream == "stdout" {
			out.Stdout = strings.ToValidUTF8(tail.Text, "?")
		} else {
			out.Stderr = strings.ToValidUTF8(tail.Text, "?")
		}
	}
	return out, nil
}
