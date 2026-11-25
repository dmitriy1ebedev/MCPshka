// Package cibinding is the explicit per-project CI routing matrix (MCP-091):
// which provider/repository/branch builds which project, whether the binding
// was reviewed by the owner and dry-run verified, and the observed state of
// one exact run, kept apart from "published". Artifacts are isolated by
// project and commit, so one project's CI never certifies another.
package cibinding

import (
	"errors"
	"regexp"
)

const (
	Unverified = "UNVERIFIED" // binding not dry-run checked
	Queued     = "QUEUED"
	Running    = "RUNNING"
	Pass       = "PASS"
	Fail       = "FAIL"
	Published  = "PUBLISHED" // only after PASS of the same commit
)

type Binding struct {
	Project       string `json:"project"`
	Provider      string `json:"provider"` // github, gitverse, moshub, laptop
	Repo          string `json:"repo"`
	Branch        string `json:"branch"`
	Workflow      string `json:"workflow"`
	OwnerReviewed bool   `json:"owner_reviewed"`
	DryRunOK      bool   `json:"dry_run_ok"`
	CanDispatch   bool   `json:"can_dispatch"` // token has workflow permission
}

type Run struct {
	Binding Binding `json:"binding"`
	Commit  string  `json:"commit"`
	RunURL  string  `json:"run_url,omitempty"`
	State   string  `json:"state"`
}

var sha = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Runnable says whether a commit may be sent through this binding.
func (b Binding) Runnable() (bool, string) {
	switch {
	case !b.OwnerReviewed:
		return false, "binding not reviewed by the owner (project_onboard)"
	case !b.DryRunOK:
		return false, "binding not dry-run verified: repository/branch/workflow unobserved"
	case !b.CanDispatch:
		return false, "token lacks workflow dispatch permission for " + b.Repo
	}
	return true, ""
}

// ArtifactKey isolates CI artifacts by project and commit.
func ArtifactKey(project, commit string) (string, error) {
	if project == "" || !sha.MatchString(commit) {
		return "", errors.New("artifact key needs project and full commit SHA")
	}
	return "ci/" + project + "/" + commit, nil
}

var next = map[string][]string{
	Unverified: {Queued},
	Queued:     {Running, Fail},
	Running:    {Pass, Fail},
	Pass:       {Published},
}

// Advance moves a run only along observed transitions; publication needs a
// PASS of the same commit, and the binding of the run cannot change.
func (r Run) Advance(state string, observed Run) (Run, error) {
	if observed.Commit != r.Commit || observed.Binding != r.Binding {
		return r, errors.New("observation belongs to another commit or binding")
	}
	for _, s := range next[r.State] {
		if s == state {
			r.State = state
			if observed.RunURL != "" {
				r.RunURL = observed.RunURL
			}
			return r, nil
		}
	}
	return r, errors.New("CI state " + r.State + " -> " + state + " not allowed")
}
