// Package cievidence reads GitHub CI results for one exact commit so the
// integration gate can accept the cloud test run instead of rebuilding the
// whole module on the host. It only reads; the caller seals what it returns.
package cievidence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mcpshka/internal/distributedci"
	"net/http"
	"regexp"
	"sort"
	"time"
)

const (
	Schema     = "mcpshka.ci-evidence/v1"
	Repository = "dmitriy1ebedev/mcpshka"
	// APIRepository is where the GitHub check runs live: the private CI
	// repository (0.19.1). Repository stays the receipts' identity label.
	APIRepository = "dmitriy1ebedev/mcpshka-ci"
	APIBase       = "https://api.github.com"
	// TokenPath is root-only; the preparation unit binds it read-only.
	TokenPath = "/root/.config/gh-mcpshka.token"
	maxBody   = 4 << 20
)

// RequiredChecks are the CI jobs that together run the release suite
// (go test ./...), race, fuzz and both builds on the exact commit.
var RequiredChecks = []string{"admin-recovery", "checks (build)", "checks (fuzz)", "checks (race)", "checks (release)", "checks (release-arm64)", "isolated-workbench"}

var sha = regexp.MustCompile(`^[a-f0-9]{40}$`)

// ErrHold marks evidence that does not prove a passing run for the commit.
var ErrHold = errors.New("CI evidence incomplete or failing: HOLD")

type Evidence struct {
	Distributed *distributedci.Report `json:"distributed,omitempty"`
	Schema      string                `json:"schema"`
	// Provider is empty for GitHub (sealed before providers existed) or
	// "gitverse"; it selects the repository and required check set.
	Provider   string `json:"provider,omitempty"`
	Repository string `json:"repository"`
	HeadSHA    string `json:"head_sha"`
	// External-project evidence binds remote code to this exact local source.
	SourceSHA   string            `json:"source_sha,omitempty"`
	PullRequest int               `json:"pull_request,omitempty"`
	Tree        string            `json:"tree"`
	BaseSHA     string            `json:"base_sha"`
	Checks      map[string]string `json:"checks"`
	CheckedAt   time.Time         `json:"checked_at"`
}

// Verify accepts only a complete, all-success record for this commit and tree.
func (e Evidence) Verify(commit, tree string) error {
	if e.Provider == "distributed" {
		if e.Schema != Schema || e.Repository != "distributed:mcpshka" || e.HeadSHA != commit || e.Tree != tree || !sha.MatchString(commit) || !sha.MatchString(tree) || e.Distributed == nil || e.CheckedAt.IsZero() {
			return ErrHold
		}
		p := e.Distributed.Plan
		if p.Commit != commit || !sha.MatchString(p.Tree) || p.Digest == "" && p.Tree != tree || e.BaseSHA != p.Snapshot {
			return ErrHold
		}
		return e.Distributed.Verify(p)
	}
	if e.Distributed != nil {
		return ErrHold
	}
	if e.Provider == VentaProvider {
		// Root fetched authenticated GitHub PR checks for a different commit
		// object whose complete Git tree equals the pinned local source tree.
		if e.Schema != Schema || e.Repository != VentaRepository ||
			e.SourceSHA != commit || !sha.MatchString(commit) ||
			!sha.MatchString(e.HeadSHA) || !sha.MatchString(tree) ||
			e.Tree != tree || !sha.MatchString(e.BaseSHA) ||
			e.PullRequest < 1 || e.CheckedAt.IsZero() ||
			len(e.Checks) != len(VentaRequiredChecks) {
			return ErrHold
		}
		for _, name := range VentaRequiredChecks {
			if e.Checks[name] != "success" {
				return fmt.Errorf("%w: %s=%q", ErrHold, name, e.Checks[name])
			}
		}
		return nil
	}
	if e.SourceSHA != "" || e.PullRequest != 0 {
		return ErrHold
	}
	if (e.Provider != "" && e.Provider != GitVerseProvider) || e.Schema != Schema || e.Repository != e.repository() || !sha.MatchString(commit) || !sha.MatchString(tree) ||
		e.HeadSHA != commit || e.Tree != tree || !sha.MatchString(e.BaseSHA) || e.CheckedAt.IsZero() || len(e.Checks) != len(e.required()) {
		return ErrHold
	}
	for _, name := range e.required() {
		if e.Checks[name] != "success" {
			return fmt.Errorf("%w: %s=%q", ErrHold, name, e.Checks[name])
		}
	}
	return nil
}

type client struct {
	http  *http.Client
	base  string
	token string
}

func (c client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "mcpshka-ci-evidence")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("github %s: HTTP %d", path, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		return errors.New("github response exceeds budget")
	}
	return json.Unmarshal(body, out)
}

type checkRun struct {
	// GitHub Actions is a trusted first-party GitHub App, not a caller-defined
	// check-run publisher. Only the Venta-specific gate requires this issuer.
	App struct {
		ID   int64  `json:"id"`
		Slug string `json:"slug"`
	} `json:"app"`
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	HeadSHA      string `json:"head_sha"`
	Status       string `json:"status"`
	Conclusion   string `json:"conclusion"`
	PullRequests []struct {
		Number int `json:"number"`
		Base   struct {
			SHA string `json:"sha"`
		} `json:"base"`
	} `json:"pull_requests"`
}

// Fetch reads the latest run of every required check for commit, the commit's
// tree and the PR base the runs merged with. GitHub tests a pull request
// merged into that base; the caller proves locally that the merge equals the
// commit's tree (an ancestor base trivially does).
func Fetch(ctx context.Context, h *http.Client, base, token, commit string) (Evidence, error) {
	if !sha.MatchString(commit) || token == "" {
		return Evidence{}, ErrHold
	}
	c := client{http: h, base: base, token: token}
	var runs struct {
		CheckRuns []checkRun `json:"check_runs"`
	}
	if err := c.get(ctx, "/repos/"+APIRepository+"/commits/"+commit+"/check-runs?per_page=100", &runs); err != nil {
		return Evidence{}, err
	}
	sort.Slice(runs.CheckRuns, func(i, j int) bool { return runs.CheckRuns[i].ID > runs.CheckRuns[j].ID })
	ev := Evidence{Schema: Schema, Repository: Repository, HeadSHA: commit, Checks: map[string]string{}}
	required := map[string]bool{}
	for _, name := range RequiredChecks {
		required[name] = true
	}
	for _, r := range runs.CheckRuns {
		if !required[r.Name] || r.HeadSHA != commit {
			continue
		}
		if _, seen := ev.Checks[r.Name]; seen {
			continue // older rerun of the same job
		}
		conclusion := r.Conclusion
		if r.Status != "completed" {
			conclusion = r.Status
		}
		ev.Checks[r.Name] = conclusion
		for _, pr := range r.PullRequests {
			if sha.MatchString(pr.Base.SHA) && ev.BaseSHA == "" {
				ev.BaseSHA = pr.Base.SHA
			}
		}
	}
	if len(ev.Checks) != len(RequiredChecks) || ev.BaseSHA == "" {
		return Evidence{}, ErrHold
	}
	var commitObj struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := c.get(ctx, "/repos/"+APIRepository+"/git/commits/"+commit, &commitObj); err != nil {
		return Evidence{}, err
	}
	ev.Tree = commitObj.Tree.SHA
	ev.CheckedAt = time.Now().UTC()
	return ev, nil
}
