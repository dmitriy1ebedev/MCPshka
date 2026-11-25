package offbox

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/jobs"
	"mcpshka/internal/repopath"
	"mcpshka/internal/runnerhub"
)

// Default drives the production hub; Waker is set once a site can be woken
// (forge_ci mode=runner). Without it only live runners take tests off H96.
var Default = New(runnerhub.Default, nil)

// Route sends a targeted Go test of the working tree root to a runner off
// H96 (GitHub, GitVerse, MosHub, GigaIDE; H96 last). ok=false: no off-box
// route right now, the caller runs it on H96 as before.
func (d *Driver) Route(ctx context.Context, root string, packages []string, timeout int, local func() (jobs.Snapshot, error)) (s jobs.Snapshot, reason string, ok bool) {
	if testing.Testing() && d == Default { // unit tests never queue into production
		return s, "", false
	}
	repo := root
	if r := repopath.Worktree(root); r != "" {
		repo = r
	}
	if filepath.Dir(repo) != "/root/repos" {
		return s, "", false
	}
	job, err := runnerhub.Plan(repo, strings.Repeat("0", 40), "go-test", packages, timeout)
	if err != nil || d.Wake == nil && d.Hub.Fit(job.Needs) != "" {
		return s, "", false
	}
	sctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	commit, err := SnapshotCommit(sctx, root)
	if err != nil {
		return s, "", false
	}
	job.Commit, job.Workdir, job.ID = commit, root, runnerhub.NewJobID()
	if d.Hub.Archive(sctx, job) != nil {
		return s, "", false
	}
	if job, err = d.Hub.Enqueue(job); err != nil {
		return s, "", false
	}
	go d.Drive(context.Background(), job.ID, func() (string, error) {
		s, err := local()
		return s.ID, err
	})
	s, err = View{Hub: d.Hub}.Get(job.ID)
	return s, "; off-box: runner job " + job.ID + " on snapshot " + commit[:12] + " (live runner or woken CI site/GigaIDE; H96 only if none takes it) — job_wait/job_output take this id", err == nil
}

// Resume shows runner jobs through the H96 job tools and restarts drivers of
// open off-box tests after a restart (a given-up one then asks for a rerun).
func (d *Driver) Resume(local LocalJobs) {
	if testing.Testing() && d == Default {
		return
	}
	jobs.SetForeign(View{Hub: d.Hub, Local: local})
	for _, id := range d.Hub.Open() {
		go d.Drive(context.Background(), id, nil)
	}
}

// JobWaker wakes a site through the bounded forge_ci runner job that start
// launches (the interpreter allowlist stays the job manager's): it waits
// for the job and reads its mcpshka.runner-wake/v1 line.
func JobWaker(start func(site string) (string, error), jm LocalJobs) Waker {
	return func(ctx context.Context, site string) error {
		id, err := start(site)
		if err != nil {
			return err
		}
		for {
			s, err := jm.Get(id)
			if err != nil {
				return err
			}
			if s.State != jobs.Queued && s.State != jobs.Running {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
		out, err := jm.Read(id, "stdout", 0, 4096)
		if err != nil {
			return err
		}
		var r struct{ State, Reason string }
		if json.Unmarshal([]byte(strings.TrimSpace(out.Text)), &r) != nil || r.State != "accepted" {
			return fmt.Errorf("%s wake %s: %s", site, orDefault(r.State, "unreadable"), r.Reason)
		}
		return nil
	}
}
