package projectlease

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"time"

	"mcpshka/internal/policy"
)

// AutoStaleAfter is how long a dead holder's lease waits before the server
// releases it alone (S5.LEASE, owner 23.11: a ChatGPT/Codex session that went
// away must not hold a merged branch for a day).
const AutoStaleAfter = 2 * time.Hour

// AutoStaleResult is one maintenance pass: released scopes and, for the rest,
// why each was kept (shown in the brief, with the Face ID tools as the way out).
type AutoStaleResult struct {
	Released []string          `json:"released,omitempty"`
	Kept     map[string]string `json:"kept,omitempty"`
}

// AutoReleaseStale releases, without the owner, only leases with nothing to
// lose: staleEvidence proves the holder gone (no owner process, no job, no
// process in the worktree), the last heartbeat is older than AutoStaleAfter
// and lost(path) is empty — the worktree is clean and its HEAD is in main.
// Everything else stays for project_lease_admin_stale_preview.
func (s *Service) AutoReleaseStale(now time.Time, lost func(path string) string) AutoStaleResult {
	out := AutoStaleResult{Kept: map[string]string{}}
	if s == nil || s.database == nil {
		return out
	}
	keys, err := s.database.List("leases/")
	if err != nil {
		return out
	}
	for _, key := range keys {
		raw, err := s.database.Get(key)
		if err != nil {
			continue
		}
		var r record
		if json.Unmarshal(raw, &r) != nil || r.ProjectRoot == "" {
			continue
		}
		last := r.AcquiredAt
		if r.HeartbeatAt.After(last) {
			last = r.HeartbeatAt
		}
		if quiet := now.Sub(last); quiet < AutoStaleAfter {
			// cheap check first: the full evidence scans /proc and jobs
			out.Kept[r.ProjectRoot] = "heartbeat " + quiet.Round(time.Minute).String() + " ago, under " + AutoStaleAfter.String()
			continue
		}
		plan, err := s.PreviewStaleRelease(r.ProjectRoot, now)
		switch {
		case err != nil:
			out.Kept[r.ProjectRoot] = err.Error()
		default:
			if why := lost(r.ProjectRoot); why != "" {
				out.Kept[r.ProjectRoot] = why
			} else if _, err := s.ReleaseStale(plan, now); err != nil {
				out.Kept[r.ProjectRoot] = err.Error()
			} else {
				out.Released = append(out.Released, r.ProjectRoot)
			}
		}
	}
	return out
}

// AutoReleaseNotes is the pass after a finished release (S5.LEASE,
// 23.11: 0.17.4 left two merged worktrees under leases of sessions dead for
// hours): it frees what has nothing to lose, so TidyMerged can remove them.
func (s *Service) AutoReleaseNotes(now time.Time) []string {
	var notes []string
	for _, path := range s.AutoReleaseStale(now, NothingToLose).Released {
		notes = append(notes, "stale lease released: "+path)
	}
	return notes
}

// AutoReleaseDefault is the maintenance pass over the live lease store.
func AutoReleaseDefault(now time.Time) AutoStaleResult {
	return New("/var/lib/mcpshka/leases", policy.Default()).AutoReleaseStale(now, NothingToLose)
}

// NothingToLose is the live lost() check: an absent worktree, or a clean one
// whose HEAD is already an ancestor of main. Git runs read-only (no optional
// locks), so root never writes into the project's .git.
func NothingToLose(path string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		// Only where the worker sees the real tree (/root/repos is bound in;
		// its /tmp is private, so absence there proves nothing) or a removed
		// scratch clone (MCP-167).
		if strings.HasPrefix(path, "/root/repos/") || DeadScratchClone(path) {
			return ""
		}
		return "worktree not visible to the maintenance worker"
	}
	git := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "/usr/bin/git", append([]string{"-c", "safe.directory=*", "-C", path}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
		return cmd.Output()
	}
	if st, err := git("status", "--porcelain", "--untracked-files=normal"); err != nil {
		return "worktree status unreadable"
	} else if strings.TrimSpace(string(st)) != "" {
		return "worktree has uncommitted changes"
	}
	if _, err := git("merge-base", "--is-ancestor", "HEAD", "main"); err != nil {
		return "HEAD is not in main yet"
	}
	return ""
}
