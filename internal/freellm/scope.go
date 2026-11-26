// Package freellm holds the night conveyor's repository scope check.
package freellm

import (
	"path/filepath"
	"strings"
)

type Card struct {
	Project              string   `json:"project"`
	Paths                []string `json:"paths"`
	RequiresOtherProject bool     `json:"requires_other_project"`
}

type Selection struct {
	Eligible             bool   `json:"eligible"`
	RequiresOtherProject bool   `json:"requires_other_project"`
	Reason               string `json:"reason,omitempty"`
}

// Select uses explicit card metadata; mentioning another project in prose
// is not enough to exclude a diagnostic fix in the current repository.
func Select(card Card, project, root string) Selection {
	skip := func(reason string) Selection { return Selection{RequiresOtherProject: true, Reason: reason} }
	if project == "" || !filepath.IsAbs(root) {
		return Selection{Reason: "current project and absolute repository root required"}
	}
	root = filepath.Clean(root)
	if card.RequiresOtherProject {
		return skip("card explicitly requires another project")
	}
	if card.Project != "" && card.Project != project && filepath.Clean(card.Project) != root {
		return skip("card project is outside current repository")
	}
	for _, p := range card.Paths {
		if !filepath.IsAbs(p) {
			p = filepath.Join(root, p)
		}
		rel, err := filepath.Rel(root, filepath.Clean(p))
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return skip("card path is outside current repository")
		}
	}
	return Selection{Eligible: true}
}
