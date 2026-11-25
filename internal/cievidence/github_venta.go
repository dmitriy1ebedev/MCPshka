package cievidence

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"time"
)

// Venta's first-party repository identity and CI contract are compiled into
// the privileged verifier. A source checkout cannot redefine these checks or
// redirect the verifier to a different repository.
const (
	VentaRoot       = "/root/repos/venta-aw730-homeassistant"
	VentaRepository = "dmitriy1ebedev/venta-aw730-homeassistant"
	VentaProvider   = "github-venta"
)

var VentaRequiredChecks = []string{
	"Python tests and repository checks",
	"HACS / Marketplace validation",
	"Home Assistant hassfest",
	"Disposable Home Assistant 2026.11.1 configuration smoke",
	"Disposable Home Assistant 2026.11.2 configuration smoke",
}

var ventaBranch = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,119}$`)

// FetchVenta binds the CI of an authenticated GitHub pull request to a local
// source commit by Git TREE, not a user-supplied or lookalike commit SHA.
// GitHub and MCPshka may have different commit objects with identical trees
// because their authors/parents differ. The tree equality is checked both
// here and again by the sealed executor before any approval is possible.
func FetchVenta(ctx context.Context, h *http.Client, base, token, sourceCommit, sourceTree, branch string) (Evidence, error) {
	if token == "" || !sha.MatchString(sourceCommit) || !sha.MatchString(sourceTree) ||
		!ventaBranch.MatchString(branch) || branch == "main" ||
		branch == "." || branch == ".." {
		return Evidence{}, ErrHold
	}
	c := client{http: h, base: base, token: token}
	var requests []struct {
		Number int    `json:"number"`
		State  string `json:"state"`
		Draft  bool   `json:"draft"`
		Head   struct {
			Ref  string `json:"ref"`
			SHA  string `json:"sha"`
			Repo struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"head"`
		Base struct {
			Ref  string `json:"ref"`
			SHA  string `json:"sha"`
			Repo struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"base"`
	}
	if err := c.get(ctx, "/repos/"+VentaRepository+"/pulls?state=open&per_page=100", &requests); err != nil {
		return Evidence{}, err
	}
	// A full page can hide another matching PR; ambiguity must not be
	// resolved using result order.
	if len(requests) == 100 {
		return Evidence{}, fmt.Errorf("%w: PR discovery incomplete", ErrHold)
	}
	var selected *struct {
		Number int    `json:"number"`
		State  string `json:"state"`
		Draft  bool   `json:"draft"`
		Head   struct {
			Ref  string `json:"ref"`
			SHA  string `json:"sha"`
			Repo struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"head"`
		Base struct {
			Ref  string `json:"ref"`
			SHA  string `json:"sha"`
			Repo struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"base"`
	}
	for i := range requests {
		pr := &requests[i]
		if pr.State != "open" || pr.Draft || pr.Head.Ref != branch ||
			pr.Head.Repo.FullName != VentaRepository ||
			pr.Base.Ref != "main" || pr.Base.Repo.FullName != VentaRepository ||
			!sha.MatchString(pr.Head.SHA) || !sha.MatchString(pr.Base.SHA) ||
			pr.Number < 1 {
			continue
		}
		if selected != nil {
			return Evidence{}, fmt.Errorf("%w: ambiguous approved PR", ErrHold)
		}
		selected = pr
	}
	if selected == nil {
		return Evidence{}, fmt.Errorf("%w: no ready exact-branch PR", ErrHold)
	}

	// Require a direct fast-forward from the GitHub PR's actual main base;
	// otherwise a synthetic PR merge commit can execute different code.
	var comparison struct {
		Status          string `json:"status"`
		AheadBy         int    `json:"ahead_by"`
		BehindBy        int    `json:"behind_by"`
		MergeBaseCommit struct {
			SHA string `json:"sha"`
		} `json:"merge_base_commit"`
	}
	if err := c.get(ctx, "/repos/"+VentaRepository+"/compare/"+selected.Base.SHA+"..."+selected.Head.SHA, &comparison); err != nil {
		return Evidence{}, err
	}
	if comparison.Status != "ahead" || comparison.AheadBy < 1 ||
		comparison.BehindBy != 0 || comparison.MergeBaseCommit.SHA != selected.Base.SHA {
		return Evidence{}, fmt.Errorf("%w: PR base does not fast-forward", ErrHold)
	}
	var remoteCommit struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := c.get(ctx, "/repos/"+VentaRepository+"/git/commits/"+selected.Head.SHA, &remoteCommit); err != nil {
		return Evidence{}, err
	}
	if remoteCommit.Tree.SHA != sourceTree {
		return Evidence{}, fmt.Errorf("%w: remote CI source tree differs from the local commit", ErrHold)
	}

	var runs struct {
		TotalCount int        `json:"total_count"`
		CheckRuns  []checkRun `json:"check_runs"`
	}
	if err := c.get(ctx, "/repos/"+VentaRepository+"/commits/"+selected.Head.SHA+"/check-runs?per_page=100", &runs); err != nil {
		return Evidence{}, err
	}
	if runs.TotalCount > 100 || len(runs.CheckRuns) > 100 {
		return Evidence{}, fmt.Errorf("%w: incomplete required check coverage", ErrHold)
	}
	sort.Slice(runs.CheckRuns, func(i, j int) bool { return runs.CheckRuns[i].ID > runs.CheckRuns[j].ID })
	required := make(map[string]bool, len(VentaRequiredChecks))
	for _, name := range VentaRequiredChecks {
		required[name] = true
	}
	evidence := Evidence{
		Schema: Schema, Provider: VentaProvider, Repository: VentaRepository,
		HeadSHA: selected.Head.SHA, SourceSHA: sourceCommit,
		Tree: sourceTree, BaseSHA: selected.Base.SHA,
		PullRequest: selected.Number, Checks: make(map[string]string),
		CheckedAt: time.Now().UTC(),
	}
	for _, run := range runs.CheckRuns {
		if !required[run.Name] || run.HeadSHA != selected.Head.SHA {
			continue
		}
		if _, seen := evidence.Checks[run.Name]; seen {
			continue
		}
		// The check name alone is not trusted. An unrelated GitHub App could
		// publish an identically named success on the same commit. Require
		// GitHub's first-party Actions App (verified id 15368, slug).
		if run.App.ID != 15368 || run.App.Slug != "github-actions" {
			return Evidence{}, fmt.Errorf("%w: CI check issuer is not GitHub Actions", ErrHold)
		}
		associated := false
		for _, associatedPR := range run.PullRequests {
			if associatedPR.Number == selected.Number &&
				associatedPR.Base.SHA == selected.Base.SHA {
				associated = true
			}
		}
		if !associated {
			return Evidence{}, fmt.Errorf("%w: required check is not attached to reviewed PR", ErrHold)
		}
		result := run.Conclusion
		if run.Status != "completed" {
			result = run.Status
		}
		evidence.Checks[run.Name] = result
	}
	if err := evidence.Verify(sourceCommit, sourceTree); err != nil {
		return Evidence{}, err
	}
	return evidence, nil
}
