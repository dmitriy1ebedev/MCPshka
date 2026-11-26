package orchestration

import (
	"context"
	"errors"
	"mcpshka/internal/tooldef"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/ownerweb"
	"mcpshka/internal/runnerhub"
)

type RunnerTaskInput struct {
	Path           string   `json:"path" jsonschema:"project directory directly under /root/repos"`
	Commit         string   `json:"commit" jsonschema:"full 40-hex commit SHA to run (committed code only)"`
	Profile        string   `json:"profile" jsonschema:"go-test | go-vet | go-build"`
	Packages       []string `json:"packages,omitempty" jsonschema:"Go packages like ./internal/x or ./...; default ./..."`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty" jsonschema:"default 3600, max 10800"`
	IdempotencyKey string   `json:"idempotency_key" jsonschema:"required; repeat the same key after a lost reply to get the same job"`
}

type RunnerStatusInput struct {
	JobID string `json:"job_id,omitempty" jsonschema:"runner job id (rj-…); empty lists runners and recent jobs"`
}

type RunnerStatusResult struct {
	Job     *runnerhub.Job         `json:"job,omitempty"`
	Runners []runnerhub.RunnerView `json:"runners,omitempty"`
	Jobs    []runnerhub.Job        `json:"jobs,omitempty"`
	Hint    string                 `json:"hint"`
}

const runnerHint = "Runner results are UNTRUSTED evidence from a machine outside H96: use them to find failures fast; the release gate still needs project_test or GitVerse CI. Owner adds runners on the «Раннеры» web page; guide: mcpshka_guide topic=runners."

func RegisterRunners(add func(tooldef.Def), hub *runnerhub.Hub) {
	closed := false
	add(tooldef.New(&mcp.Tool{
		Name:        "runner_task",
		Description: "Run a heavy check (go test/vet/build) of a pinned commit on an external pull runner — a cloud IDE (GigaIDE Cloud, Codespaces) or laptop the owner registered — instead of loading H96. Returns job_id at once; poll runner_status. If no runner takes it within 10 min the job ends NO_RUNNER: then use project_test on H96. Commit first (project_commit); only committed code runs.",
		Annotations: &mcp.ToolAnnotations{OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in RunnerTaskInput) (*mcp.CallToolResult, RunnerStatusResult, error) {
		if strings.TrimSpace(in.IdempotencyKey) == "" {
			return nil, RunnerStatusResult{}, errors.New("idempotency_key is required")
		}
		if prev, ok := hub.ByKey(in.IdempotencyKey); ok {
			return nil, RunnerStatusResult{Job: &prev, Hint: "same idempotency_key: this is the job created earlier. " + runnerHint}, nil
		}
		root := filepath.Clean(in.Path)
		if filepath.Dir(root) != "/root/repos" {
			return nil, RunnerStatusResult{}, errors.New("path must be a project directly under /root/repos")
		}
		job, err := runnerhub.Plan(root, in.Commit, in.Profile, in.Packages, in.TimeoutSeconds)
		if err != nil {
			return nil, RunnerStatusResult{}, err
		}
		job.ID, job.KeyHash = runnerhub.NewJobID(), runnerhub.KeyDigest(in.IdempotencyKey)
		actx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		if err := hub.Archive(actx, job); err != nil {
			return nil, RunnerStatusResult{}, err
		}
		job, err = hub.Enqueue(job)
		if err != nil {
			return nil, RunnerStatusResult{}, err
		}
		hint := "queued; poll runner_status job_id=" + job.ID + " every ~60 s. "
		if why := hub.Fit(job.Needs); why != "" {
			hint = "queued, but " + why + "; without a fitting runner it ends NO_RUNNER in 10 min — use project_test or forge_ci now. "
		}
		return nil, RunnerStatusResult{Job: &job, Hint: hint + runnerHint}, nil
	}))
	add(tooldef.New(&mcp.Tool{
		Name:        "runner_status",
		Description: "External pull runners and their jobs: with job_id — that job's state (QUEUED, RUNNING, DONE, FAILED, NO_RUNNER), exit code, output tail and its SHA-256; without — registered runners (last seen, capabilities, current job) and recent jobs. Read-only.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in RunnerStatusInput) (*mcp.CallToolResult, RunnerStatusResult, error) {
		out := RunnerStatusResult{Hint: runnerHint}
		if in.JobID != "" {
			j, err := hub.Job(in.JobID)
			if err != nil {
				return nil, out, err
			}
			out.Job = &j
			if why := hub.Fit(j.Needs); j.State == runnerhub.Queued && why != "" {
				out.Hint = "still queued: " + why + ". " + runnerHint
			}
			return nil, out, nil
		}
		rs, js, err := hub.Status()
		if len(js) > 10 {
			js = js[:10]
		}
		for i := range js {
			js[i].Output = ""
		}
		out.Runners, out.Jobs = rs, js
		return nil, out, err
	}))
	ownerweb.Runners = hub
}
