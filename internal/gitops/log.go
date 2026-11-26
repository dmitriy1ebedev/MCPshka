package gitops

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// LogResult is git log/rev-parse without a shell (MCP-261, MCP-266): the
// resolved commit of a ref and its newest commits, bounded.
type LogResult struct {
	Root    string   `json:"root"`
	Ref     string   `json:"ref"`
	Commit  string   `json:"commit"`
	Commits []string `json:"commits"` // "<full sha> <ISO date> <subject>"
}

var logRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/@^~-]{0,127}$`)

func (s *Service) Log(path, ref string, n int) (LogResult, error) {
	if ref == "" {
		ref = "HEAD"
	}
	if !logRef.MatchString(ref) || strings.Contains(ref, "..") || strings.HasPrefix(ref, "-") {
		return LogResult{}, errors.New("ref must be a branch, tag, HEAD~n or commit")
	}
	root, err := s.RepoRoot(path)
	if err != nil {
		return LogResult{}, err
	}
	n = min(max(n, 1), 50)
	out, stderr, _, _, err := runBounded(root, 5*time.Second, 32<<10, "git", "log", "-n", fmt.Sprint(n), "--format=%H %cI %s", ref, "--")
	if err != nil {
		return LogResult{}, commandError("git log", err, stderr)
	}
	res := LogResult{Root: root, Ref: ref}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			res.Commits = append(res.Commits, l[:min(len(l), 200)])
		}
	}
	if len(res.Commits) > 0 {
		res.Commit = strings.Fields(res.Commits[0])[0]
	}
	return res, nil
}
