package cievidence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// GitVerse is the second independent CI. MCPshka publishes an immutable
// parentless snapshot of the commit's tree to mcpshka-snapshots/<commit>; its
// SHA is deterministic (packaging/gitverse/snapshot_immutable.py), so the
// caller recomputes it locally and passes it here: a run on any other commit
// can never count.
const (
	GitVerseProvider   = "gitverse"
	GitVerseRepository = "gitverse:dmitriy_1ebedev/mcpshka-ci"
	GitVerseAPIBase    = "https://api.gitverse.ru"
	GitVerseTokenPath  = "/var/lib/mcpshka/gitverse/api_token"
	gitverseRepoPath   = "/repos/dmitriy_1ebedev/mcpshka-ci"
)

// GitVerseRequiredChecks are the snapshot workflow jobs that run the full
// suite and the snapshot contracts on the exact tree.
var GitVerseRequiredChecks = []string{"release-suite", "snapshot-contracts"}

// required returns the check set the evidence's provider must pass.
func (e Evidence) required() []string {
	if e.Provider == VentaProvider {
		return VentaRequiredChecks
	}
	if e.Provider == GitVerseProvider {
		return GitVerseRequiredChecks
	}
	return RequiredChecks
}

func (e Evidence) repository() string {
	if e.Provider == VentaProvider {
		return VentaRepository
	}
	if e.Provider == GitVerseProvider {
		return GitVerseRepository
	}
	return Repository
}

func (c client) gitverseGet(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.gitverse.object+json;version=1")
	req.Header.Set("User-Agent", "mcpshka-ci-evidence")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gitverse %s: HTTP %d", path, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		return errors.New("gitverse response exceeds budget")
	}
	return json.Unmarshal(body, out)
}

type gitverseRun struct {
	ID        int64  `json:"id"`
	Ref       string `json:"ref"`
	CommitSHA string `json:"commit_sha"`
	Status    string `json:"status"`
	Event     string `json:"event"`
}

type gitverseJob struct {
	ID     int64  `json:"id"`
	RunID  int64  `json:"run_id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// FetchGitVerse reads the snapshot runs for commit and requires every
// required job to have succeeded on exactly snapshot (the recomputed SHA).
// tree is the commit's own tree as read locally; GitVerse runs only on it.
func FetchGitVerse(ctx context.Context, h *http.Client, base, token, commit, tree, snapshot string) (Evidence, error) {
	if !sha.MatchString(commit) || !sha.MatchString(tree) || !sha.MatchString(snapshot) || token == "" {
		return Evidence{}, ErrHold
	}
	c := client{http: h, base: base, token: token}
	ref := "refs/heads/mcpshka-snapshots/" + commit
	ev := Evidence{Schema: Schema, Provider: GitVerseProvider, Repository: GitVerseRepository, HeadSHA: commit, Tree: tree, BaseSHA: snapshot, Checks: map[string]string{}}
	required := map[string]bool{}
	for _, name := range GitVerseRequiredChecks {
		required[name] = true
	}
	// Select the newest eligible run, independent of API ordering. Never fill
	// missing jobs in a pending dispatch from an older successful push.
	// GitVerse lists newest first; once the eligible run is found on a page
	// that is still strictly newest-first, nothing newer can follow, so the
	// listing may stop there even when older runs fill page 3 (BUG-20261103-32:
	// with more than 150 runs every prepare was held).
	var latest gitverseRun
	descending, lastID := true, int64(0)
	for page := 1; page <= 3; page++ {
		var listing struct {
			WorkflowRuns []gitverseRun `json:"workflow_runs"`
		}
		if err := c.gitverseGet(ctx, gitverseRepoPath+"/actions/runs?per_page=50&page="+strconv.Itoa(page), &listing); err != nil {
			return Evidence{}, err
		}
		for _, r := range listing.WorkflowRuns {
			if lastID != 0 && r.ID >= lastID {
				descending = false
			}
			lastID = r.ID
			if r.Ref == ref && r.CommitSHA == snapshot && (r.Event == "push" || r.Event == "workflow_dispatch") && r.ID > latest.ID {
				latest = r
			}
		}
		if len(listing.WorkflowRuns) < 50 || (latest.ID > 0 && descending) {
			break
		}
		if page == 3 {
			return Evidence{}, fmt.Errorf("%w: incomplete GitVerse run listing", ErrHold)
		}
	}
	if latest.ID > 0 {
		var jobs struct {
			Jobs []gitverseJob `json:"jobs"`
		}
		if err := c.gitverseGet(ctx, gitverseRepoPath+"/actions/runs/"+strconv.FormatInt(latest.ID, 10)+"/jobs", &jobs); err != nil {
			return Evidence{}, err
		}
		ids := map[string]int64{}
		for _, j := range jobs.Jobs {
			if required[j.Name] && j.RunID == latest.ID {
				if previous, seen := ids[j.Name]; !seen || j.ID > previous {
					ids[j.Name] = j.ID
					ev.Checks[j.Name] = j.Status
				}
			}
		}
	}
	if len(ev.Checks) != len(GitVerseRequiredChecks) {
		return Evidence{}, fmt.Errorf("%w: GitVerse snapshot %s has %d of %d required jobs", ErrHold, snapshot, len(ev.Checks), len(GitVerseRequiredChecks))
	}
	ev.CheckedAt = time.Now().UTC()
	return ev, nil
}
