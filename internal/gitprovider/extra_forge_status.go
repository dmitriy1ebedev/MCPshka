package gitprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"
)

const forgeHTTPTimeout = 20 * time.Second

func (c *Client) extraStatus(ctx context.Context, repo, commit string, git forgeGit, out map[string]any) error {
	out["commit"] = commit
	switch c.site {
	case GitHub:
		return c.extraGitHubStatus(ctx, commit, out)
	case GitFlic:
		return c.extraGitFlicStatus(ctx, commit, out)
	default:
		tree, err := git(ctx, repo, "", "", "rev-parse", commit+"^{tree}")
		if err != nil || !shaRE.MatchString(tree) {
			return fmt.Errorf("source tree unavailable")
		}
		return c.extraSourceCraftStatus(ctx, commit, tree, out)
	}
}

func (c *Client) extraGitFlicStatus(ctx context.Context, commit string, out map[string]any) error {
	var data struct {
		Embedded struct {
			Pipelines []struct {
				Commit  string `json:"commitId"`
				Ref     string
				Status  string
				LocalID any `json:"localId"`
				Failure any `json:"failureReason"`
			} `json:"restPipelineModelList"`
		} `json:"_embedded"`
	}
	if err := c.ciFirstPage(ctx, "/project/lda6994/mcpshka-ci/cicd/pipeline?page=0&size=20", &data); err != nil {
		return err
	}
	for _, p := range data.Embedded.Pipelines {
		if p.Commit != commit || p.Ref != forgeSnapshot+commit {
			continue
		}
		status, state := strings.ToUpper(p.Status), "HOLD"
		switch status {
		case "SUCCESS":
			state = "observed_pass"
		case "FAILED", "CANCELED", "WARNING", "ERROR":
			state = "observed_fail"
		}
		out["state"], out["pipeline"], out["pipeline_status"], out["failure_reason"] = state, p.LocalID, status, p.Failure
		return nil
	}
	out["state"], out["code"] = "HOLD", "no_pipeline_for_commit"
	return nil
}

func (c *Client) extraSourceCraftStatus(ctx context.Context, commit, tree string, out map[string]any) error {
	p := CIProvider{Client: c}
	var data struct {
		Runs []struct {
			Status, Slug string
			Workflows    []struct {
				Tasks []struct {
					Cubes []struct {
						Artifacts []struct {
							Path   string `json:"local_path"`
							Status string
							URL    string `json:"download_url"`
						}
					}
				}
			}
		}
	}
	if err := c.ciFirstPage(ctx, c.ciRoute()+"/cicd/runs?page_size=20", &data); err != nil {
		return err
	}
	reason := "no_report_for_commit"
	for _, run := range data.Runs {
		switch run.Status {
		case "created", "prepared", "processing", "awaiting_approval":
			reason = "pending"
		}
		for _, wf := range run.Workflows {
			for _, task := range wf.Tasks {
				for _, cube := range task.Cubes {
					for _, artifact := range cube.Artifacts {
						if artifact.Status != "success" || !strings.HasSuffix(artifact.Path, "report.json") || artifact.URL == "" {
							continue
						}
						raw, err := p.sourceCraftArtifact(ctx, artifact.URL)
						if err != nil {
							continue
						}
						var report struct {
							Commit, Tree string
							Checks       map[string]string
						}
						if json.Unmarshal(raw, &report) != nil || report.Commit != commit || report.Tree != tree || report.Checks == nil {
							continue
						}
						state := "observed_fail"
						if run.Status == "success" && allExtraChecks(SourceCraft, func(name string) bool { return report.Checks[name] == "success" }) {
							state = "observed_pass"
						}
						out["state"], out["run"], out["run_status"], out["checks"], out["tree"] = state, run.Slug, run.Status, report.Checks, tree
						return nil
					}
				}
			}
		}
	}
	out["state"], out["code"] = "HOLD", reason
	return nil
}

func allExtraChecks(site Site, passes func(string) bool) bool {
	for _, name := range extraForges[site].required {
		if !passes(name) {
			return false
		}
	}
	return true
}

type forgeGitHubRun struct {
	ID         int64  `json:"run"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Event      string `json:"event"`
}

func (c *Client) extraGitHubStatus(ctx context.Context, commit string, out map[string]any) error {
	var data struct {
		Runs []struct {
			ID                              int64
			Path, Status, Conclusion, Event string
			Commit                          string `json:"head_sha"`
		} `json:"workflow_runs"`
	}
	if err := c.ciFirstPage(ctx, c.ciRoute()+"/actions/runs?per_page=30&head_sha="+commit, &data); err != nil {
		return err
	}
	slices.SortStableFunc(data.Runs, func(a, b struct {
		ID                              int64
		Path, Status, Conclusion, Event string
		Commit                          string `json:"head_sha"`
	}) int {
		if a.ID > b.ID {
			return -1
		}
		if a.ID < b.ID {
			return 1
		}
		return 0
	})
	seen := map[string][]forgeGitHubRun{}
	var failed *forgeGitHubRun
	for _, run := range data.Runs {
		if run.Commit != commit {
			continue
		}
		name := path.Base(run.Path)
		entry := forgeGitHubRun{run.ID, run.Status, run.Conclusion, run.Event}
		if failed == nil && len(seen[name]) == 0 && entry.Status == "completed" &&
			!slices.Contains([]string{"success", "skipped", "neutral"}, entry.Conclusion) {
			failed = &entry
		}
		seen[name] = append(seen[name], entry)
	}
	switch {
	case len(seen) == 0:
		out["state"], out["code"] = "HOLD", "no_run_for_commit"
		return nil
	case failed != nil:
		out["state"], out["diagnosis"] = "observed_fail", c.extraGitHubDiagnosis(ctx, failed.ID)
	case allExtraChecks(GitHub, func(name string) bool {
		return len(seen[name]) > 0 && seen[name][0].Status == "completed" && seen[name][0].Conclusion == "success"
	}):
		out["state"] = "observed_pass"
	default:
		out["state"], out["code"] = "HOLD", "pending"
	}
	out["runs"] = seen
	return nil
}

func (c *Client) extraGitHubDiagnosis(ctx context.Context, run int64) map[string]any {
	var data struct {
		Jobs []struct {
			Conclusion string
			Runner     string `json:"runner_name"`
			Steps      []json.RawMessage
		}
	}
	_ = c.ciFirstPage(ctx, fmt.Sprintf("%s/actions/runs/%d/jobs?per_page=50", c.ciRoute(), run), &data)
	blocked := len(data.Jobs) > 0
	for _, job := range data.Jobs {
		blocked = blocked && len(job.Steps) == 0 && job.Runner == "" && job.Conclusion == "failure"
	}
	code, reason := "CI_TEST_FAILED", "jobs ran and failed: read their logs"
	if blocked {
		code, reason = "CI_ACCOUNT_BLOCKED", "GitHub rejected every job before a runner (0 steps): Actions minutes/billing of the account; rerunning or a new commit does not help"
	}
	return map[string]any{"code": code, "reason": reason, "jobs": len(data.Jobs)}
}
