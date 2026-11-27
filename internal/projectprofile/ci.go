package projectprofile

import (
	"mcpshka/internal/repopath"
	"path/filepath"
)

type CI struct {
	Provider     string `json:"provider"`
	ExternalOnly bool   `json:"external_only"`
}

// CIFor retains the built-in placement policy even when a local profile
// supplies project-specific commands. An explicitly configured policy wins.
func (s *Service) CIFor(path string) *CI {
	if r, e := s.Resolve(path); e == nil && r.Profile.CI != nil {
		c := *r.Profile.CI
		return &c
	}
	p, e := s.projects.Detect(path)
	if e != nil {
		return nil
	}
	canonical := p.Root
	if repo := repopath.Worktree(canonical); repo != "" {
		canonical = repo
	}
	if filepath.Dir(canonical) != "/root/repos" {
		return nil
	}
	for _, b := range s.builtins {
		if b.CI != nil && validateMatch(b.Match, p) == nil {
			c := *b.CI
			return &c
		}
	}
	return nil
}
