// Package offbox keeps tests off H96: a targeted project_test becomes a
// runner job (internal/runnerhub) on a snapshot of the working tree, and a
// driver brings a runner up for it — a live one first, else it wakes the CI
// sites and cloud IDEs in turn (GitHub, GitVerse, MosHub, GigaIDE), the least
// recently used first so no single free quota drains. H96 runs the test only
// when no site served it in time.
package offbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"mcpshka/internal/runnerhub"
)

// Sites in preference order for equal use; a site without a waker is skipped.
var Sites = []string{"github", "gitverse", "moshub", "gigaide"}

// Waker starts a temporary runner on site (forge_ci mode=runner).
type Waker func(ctx context.Context, site string) error

// Local starts the same test on H96 and returns its job id.
type Local func() (string, error)

type Driver struct {
	Hub  *runnerhub.Hub
	Wake Waker // nil: no site can be woken, only live runners serve
	Now  func() time.Time
	Poll time.Duration
	// WakeEvery: a woken site that has not leased the job by then is given
	// up for the next one. GiveUp: total time before H96 takes the test.
	WakeEvery, GiveUp, FailCool time.Duration

	mu      sync.Mutex
	driving map[string]bool
}

func New(hub *runnerhub.Hub, wake Waker) *Driver {
	return &Driver{Hub: hub, Wake: wake, Now: time.Now, Poll: 10 * time.Second,
		WakeEvery: 4 * time.Minute, GiveUp: 12 * time.Minute, FailCool: 30 * time.Minute, driving: map[string]bool{}}
}

// Drive follows job id until a runner finished it or H96 took it over; one
// driver per job.
func (d *Driver) Drive(ctx context.Context, id string, local Local) {
	d.mu.Lock()
	if d.driving[id] {
		d.mu.Unlock()
		return
	}
	d.driving[id] = true
	d.mu.Unlock()
	defer func() { d.mu.Lock(); delete(d.driving, id); d.mu.Unlock() }()
	for {
		if done := d.step(ctx, id, local); done {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(d.Poll):
		}
	}
}

// step makes one decision; true when the driver is no longer needed.
func (d *Driver) step(ctx context.Context, id string, local Local) bool {
	j, err := d.Hub.Job(id)
	if err != nil {
		return true
	}
	now := d.Now()
	switch j.State {
	case runnerhub.Done, runnerhub.Failed:
		return true
	case runnerhub.NoRunner:
		if j.Fallback == "" {
			d.fallback(id, local)
		}
		return true
	case runnerhub.Leased:
		return false // a runner has it; a lost lease puts it back to Queued
	}
	last := j.Created
	if n := len(j.Wakes); n > 0 {
		last = j.Wakes[n-1].At
	}
	live := d.Hub.Fit(j.Needs) == ""
	if live && now.Sub(last) < d.WakeEvery || len(j.Wakes) > 0 && j.Wakes[len(j.Wakes)-1].Err == "" && now.Sub(last) < d.WakeEvery {
		return false // a live or freshly woken runner should take it
	}
	// One starting runner can serve the queue; do not wake other clouds meanwhile.
	for _, use := range d.Hub.Sites() {
		if now.Sub(use.LastWake) < d.WakeEvery && use.LastFail.Before(use.LastWake) {
			return false
		}
	}
	site := ""
	if d.Wake != nil && now.Sub(j.Created) < d.GiveUp {
		site = d.pick(j, now)
	}
	if site == "" {
		if live && now.Sub(j.Created) < d.GiveUp {
			return false
		}
		if ok, _ := d.Hub.GiveUp(id, "no off-box runner took the test in time; H96 runs it"); ok {
			d.fallback(id, local)
		}
		return !d.leased(id)
	}
	if reserved, err := d.Hub.ReserveWake(id, site, d.WakeEvery); err != nil || !reserved {
		return false
	}
	wctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	var werr error
	if site == "gigaide" {
		limit, _ := strconv.ParseFloat(os.Getenv("MCPSHKA_GIGAIDE_VCPU_HOURS_REMAINING"), 64)
		werr = d.Hub.ReserveCloudWake(id, limit, float64(max(j.Timeout, 3600))*2/3600)
	}
	if werr == nil {
		werr = d.Wake(wctx, site)
	}
	cancel()
	_ = d.Hub.NoteWake(id, site, werr)
	return false
}

// localRunOf is the H96 job that already took over another off-box job of
// the same project, commit and kind, or "".
func (d *Driver) localRunOf(id string) string {
	j, err := d.Hub.Job(id)
	if err != nil || j.Commit == "" {
		return ""
	}
	_, jobs, err := d.Hub.Snapshot()
	if err != nil {
		return ""
	}
	for _, o := range jobs {
		if o.ID != id && o.Fallback != "" && o.Project == j.Project && o.Commit == j.Commit && o.Kind == j.Kind {
			return o.Fallback
		}
	}
	return ""
}

func (d *Driver) leased(id string) bool {
	j, err := d.Hub.Job(id)
	return err == nil && j.State == runnerhub.Leased
}

func (d *Driver) fallback(id string, local Local) {
	// S5.DIAGCI: at most one heavy H96 run per commit; a second test of the
	// same commit reads that run instead of loading the box again.
	if prev := d.localRunOf(id); prev != "" {
		_ = d.Hub.SetFallback(id, "", "HOLD: H96 already ran this commit locally (job "+prev+"): read its result with job_wait, or fix the off-box runner; one local fallback per commit")
		return
	}
	if local == nil {
		_ = d.Hub.SetFallback(id, "", "no H96 route after a restart: run project_test again")
		return
	}
	lid, err := local()
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	_ = d.Hub.SetFallback(id, lid, msg)
}

// pick returns the site to wake next for j: not yet tried for it, no refused
// wake within FailCool, least recently woken first (spreads free quotas).
func (d *Driver) pick(j runnerhub.Job, now time.Time) string {
	tried := map[string]bool{}
	for _, w := range j.Wakes {
		tried[w.Site] = true
	}
	use := d.Hub.Sites()
	var cands []string
	for _, s := range Sites {
		if s == "gigaide" && !gigaBudget(j.Timeout) {
			continue
		}
		if now.Sub(use[s].LastWake) < d.WakeEvery && use[s].LastFail.Before(use[s].LastWake) {
			continue
		}
		if !tried[s] && now.Sub(use[s].LastFail) > d.FailCool {
			cands = append(cands, s)
		}
	}
	sort.SliceStable(cands, func(a, b int) bool { return use[cands[a]].LastWake.Before(use[cands[b]].LastWake) })
	if len(cands) == 0 {
		return ""
	}
	return cands[0]
}

// SnapshotCommit pins the working tree of dir, uncommitted changes
// included, as a commit object (no branch, index or worktree is touched):
// HEAD itself when the tree is clean.
func SnapshotCommit(ctx context.Context, dir string) (string, error) {
	tmp, err := os.MkdirTemp("", "offbox-index-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	env := append(os.Environ(), "GIT_INDEX_FILE="+filepath.Join(tmp, "index"),
		"GIT_AUTHOR_NAME=mcpshka", "GIT_AUTHOR_EMAIL=mcpshka@localhost",
		"GIT_COMMITTER_NAME=mcpshka", "GIT_COMMITTER_EMAIL=mcpshka@localhost")
	git := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = env
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("git %s: %w", args[0], err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	head, err := git("rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if _, err := git("read-tree", "HEAD"); err != nil {
		return "", err
	}
	if _, err := git("add", "-A", "."); err != nil {
		return "", err
	}
	tree, err := git("write-tree")
	if err != nil {
		return "", err
	}
	if headTree, err := git("rev-parse", "HEAD^{tree}"); err == nil && headTree == tree {
		return head, nil
	}
	return git("commit-tree", tree, "-p", head, "-m", "mcpshka off-box test snapshot")
}
