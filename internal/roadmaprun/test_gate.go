package roadmaprun

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// HTTPTest owns the sealed test and its observation; an agent's prose is
// never acceptance. The retry key is pinned to this run/phase/attempt.
func HTTPTest(url, auth string) func(context.Context, string, string, string, func(string) error) (string, string, string, error) {
	return func(ctx context.Context, path, token, key string, started func(string) error) (string, string, string, error) {
		type project struct {
			Source   string `json:"profile_source"`
			Revision string `json:"revision"`
			Git      struct {
				Dirty bool `json:"dirty"`
			} `json:"git"`
		}
		var before project
		if err := callTool(ctx, url, auth, "project_context", map[string]any{"path": path}, &before); err != nil {
			return "", "", "", err
		}
		if before.Revision == "" || before.Git.Dirty {
			return "", "", "commit the task and leave a clean HEAD before sealed project_test", nil
		}
		if before.Source == "" || before.Source == "builtin" {
			return "", "", "sealed workflow missing/unverified: use project_onboard_preview, review its test workflow and project_onboard with the runner lease; preserve any existing profile", nil
		}

		var test struct {
			Job struct {
				ID       string `json:"id"`
				External *struct {
					Runtime string `json:"runtime"`
				} `json:"external"`
			} `json:"job"`
		}
		admissionErr := callTool(ctx, url, auth, "project_test", map[string]any{"path": path, "lease_token": token, "idempotency_key": key}, &test)
		if admissionErr != nil || test.Job.ID == "" {
			// Recover only the original job handle; UNKNOWN never authorizes a new test.
			var probe struct {
				Handles struct {
					JobID string `json:"job_id"`
				} `json:"handles"`
			}
			if err := callTool(ctx, url, auth, "mutation_probe", map[string]any{"tool": "project_test", "key": key}, &probe); err != nil {
				return "", "", "", fmt.Errorf("project_test outcome UNKNOWN; reconcile original key %s: %w", key, errors.Join(admissionErr, err))
			}
			id := probe.Handles.JobID
			if id == "" {
				return "", "", "", fmt.Errorf("project_test outcome UNKNOWN; receipt has no job; reconcile original key %s", key)
			}
			if err := callTool(ctx, url, auth, "job_status", map[string]any{"job_id": id, "verbose": true}, &test.Job); err != nil {
				return id, "", "", fmt.Errorf("test job %s outcome UNKNOWN: %w", id, err)
			}
			if test.Job.ID != id {
				return id, "", "", errors.New("recovered test job identity differs; outcome UNKNOWN")
			}
		}
		id := test.Job.ID
		if err := started(id); err != nil {
			return id, "", "", err
		}
		if test.Job.External == nil || test.Job.External.Runtime != "mcpshka-workbench/v1" {
			return id, "", "", errors.New("job is not bound to the sealed workbench runtime; reconcile this job before retrying")
		}

		lastUnknown := ""
		for {
			var wait struct {
				Status struct {
					State    string `json:"state"`
					ExitCode *int   `json:"exit_code"`
					Released bool   `json:"resources_released"`
				} `json:"status"`
				Stdout string `json:"stdout"`
				Stderr string `json:"stderr"`
			}
			if err := callTool(ctx, url, auth, "job_wait", map[string]any{"job_id": id, "timeout_seconds": 50}, &wait); err != nil {
				lastUnknown = err.Error()
				select {
				case <-ctx.Done():
					return id, "", "", fmt.Errorf("test job %s outcome UNKNOWN (%s): %w", id, lastUnknown, ctx.Err())
				case <-time.After(time.Second):
					continue
				}
			}
			switch wait.Status.State {
			case "done":
				if !wait.Status.Released {
					break
				}
				if wait.Status.ExitCode == nil || *wait.Status.ExitCode != 0 {
					return id, "", "sealed project_test failed: " + wait.Stdout + "\n" + wait.Stderr, nil
				}
				var after project
				if err := callTool(ctx, url, auth, "project_context", map[string]any{"path": path}, &after); err != nil {
					return id, "", "", err
				}
				if after.Git.Dirty || after.Revision != before.Revision {
					return id, "", "HEAD/tree changed during sealed project_test; commit and retest", nil
				}
				return id, before.Revision, "", nil
			case "failed", "timed_out", "cancelled":
				if !wait.Status.Released {
					break
				}
				return id, "", "sealed project_test " + wait.Status.State + ": " + wait.Stdout + "\n" + wait.Stderr, nil
			case "unknown":
				lastUnknown = "job_wait reports unknown; observation only, no relaunch"
			case "queued", "QUEUED", "running", "cancelling":
			default:
				return id, "", "", fmt.Errorf("test job %s state %q UNKNOWN; reconcile", id, wait.Status.State)
			}
			select {
			case <-ctx.Done():
				return id, "", "", fmt.Errorf("test job %s outcome UNKNOWN (%s): %w", id, lastUnknown, ctx.Err())
			case <-time.After(time.Second):
			}
		}
	}
}
