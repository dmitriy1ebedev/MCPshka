package gitops

import (
	"strings"
	"time"
)

// TidyMerged removes what a release leaves behind (owner 14.11: «репа
// MCPshka должна быть вычищена» after every release, not only the released
// branch): every clean linked worktree whose branch is already in main and
// whose lease is free, then every branch merged into main that no worktree
// still checks out. held returns the lease owner of a worktree ("" = free).
// A dirty, unmerged or leased checkout is kept and named in the notes.
func (s *Service) TidyMerged(repo string, held func(dir string) string) []string {
	root, err := s.RepoRoot(repo)
	if err != nil {
		return []string{"tidy: " + err.Error()}
	}
	git := func(args ...string) (string, error) {
		out, stderr, _, _, err := runBounded(root, 60*time.Second, 256<<10, "git", append([]string{"-c", "safe.directory=*", "-c", "core.hooksPath=/dev/null"}, args...)...)
		if err != nil {
			return "", commandError("git "+args[0], err, stderr)
		}
		return strings.TrimSpace(string(out)), nil
	}
	merged := func(ref string) bool { _, err := git("merge-base", "--is-ancestor", ref, "main"); return err == nil }
	var notes []string
	out, err := git("worktree", "list", "--porcelain")
	if err != nil {
		return []string{"tidy: " + err.Error()}
	}
	busy := map[string]bool{"main": true}
	for _, w := range parseWorktrees(out) {
		if w.Path == root || w.Branch == "" || w.Branch == "main" {
			continue
		}
		keep := ""
		switch {
		case !merged(w.Branch):
			keep = "not in main"
		case held(w.Path) != "":
			keep = "lease held by " + held(w.Path)
		default:
			if dirty, err := git("-C", w.Path, "status", "--porcelain"); err != nil || dirty != "" {
				keep = "uncommitted changes"
			} else if _, err := git("worktree", "remove", w.Path); err != nil {
				keep = err.Error()
			}
		}
		if keep != "" {
			busy[w.Branch] = true
			notes = append(notes, "worktree "+w.Path+" kept: "+keep)
			continue
		}
		notes = append(notes, "worktree "+w.Path+" removed")
	}
	_, _ = git("worktree", "prune")
	refs, err := git("for-each-ref", "--format=%(refname:short)", "--merged=main", "refs/heads")
	if err != nil {
		return append(notes, "tidy branches: "+err.Error())
	}
	for _, b := range strings.Fields(refs) {
		if busy[b] {
			continue
		}
		if _, err := git("branch", "-d", b); err != nil {
			notes = append(notes, "branch "+b+" kept: "+err.Error())
		} else {
			notes = append(notes, "branch "+b+" deleted")
		}
	}
	return notes
}
