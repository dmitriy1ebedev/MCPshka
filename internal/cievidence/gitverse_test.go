package cievidence

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchGitVerseBindsSnapshotAndJobs(t *testing.T) {
	commit, tree, snap, other := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40), strings.Repeat("d", 40)
	jobs := map[string]string{"release-suite": "success", "snapshot-contracts": "success"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/actions/runs"):
			// The real API wraps runs: {"total_count": n, "workflow_runs": [...]}.
			json.NewEncoder(w).Encode(map[string]any{"total_count": 2, "workflow_runs": []gitverseRun{
				{ID: 2, Ref: "refs/heads/mcpshka-snapshots/" + commit, CommitSHA: other, Status: "success", Event: "push"},
				{ID: 1, Ref: "refs/heads/mcpshka-snapshots/" + commit, CommitSHA: snap, Status: "success", Event: "push"},
			}})
		case strings.HasSuffix(r.URL.Path, "/runs/1/jobs"):
			var out struct {
				Jobs []gitverseJob `json:"jobs"`
			}
			for name, st := range jobs {
				out.Jobs = append(out.Jobs, gitverseJob{RunID: 1, Name: name, Status: st})
			}
			json.NewEncoder(w).Encode(out)
		case strings.HasSuffix(r.URL.Path, "/runs/2/jobs"):
			t.Fatal("run on a foreign snapshot was read")
		}
	}))
	defer srv.Close()
	ev, err := FetchGitVerse(context.Background(), srv.Client(), srv.URL, "tok", commit, tree, snap)
	if err != nil || ev.Verify(commit, tree) != nil || ev.BaseSHA != snap || ev.Provider != GitVerseProvider {
		t.Fatal("passing GitVerse snapshot rejected", ev, err)
	}
	jobs["release-suite"] = "failure"
	ev, err = FetchGitVerse(context.Background(), srv.Client(), srv.URL, "tok", commit, tree, snap)
	if err == nil && ev.Verify(commit, tree) == nil {
		t.Fatal("failing release suite accepted")
	}
	delete(jobs, "release-suite")
	if _, err := FetchGitVerse(context.Background(), srv.Client(), srv.URL, "tok", commit, tree, snap); err == nil {
		t.Fatal("missing release suite accepted")
	}
	github := Evidence{Schema: Schema, Repository: Repository, HeadSHA: commit, Tree: tree, BaseSHA: snap, Checks: map[string]string{"release-suite": "success", "snapshot-contracts": "success"}}
	github.CheckedAt = ev.CheckedAt
	if github.Verify(commit, tree) == nil {
		t.Fatal("GitVerse checks counted as GitHub evidence")
	}
}

func TestGitVerseLatestRunAndJobs(t *testing.T) {
	commit, tree, snap := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	for _, state := range []string{"success", "failure", "waiting", "missing"} {
		t.Run(state, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/actions/runs") {
					if r.URL.Query().Get("event") != "" {
						t.Error("events filtered")
					}
					json.NewEncoder(w).Encode(map[string]any{"workflow_runs": []gitverseRun{
						{ID: 1, Ref: "refs/heads/mcpshka-snapshots/" + commit, CommitSHA: snap, Event: "push"},
						{ID: 3, Ref: "refs/heads/mcpshka-snapshots/" + commit, CommitSHA: snap, Event: "workflow_dispatch"},
						{ID: 4, Ref: "refs/heads/mcpshka-snapshots/" + commit, CommitSHA: snap, Event: "pull_request"},
					}})
				} else if strings.HasSuffix(r.URL.Path, "/runs/3/jobs") {
					jobs := []gitverseJob{}
					if state != "missing" {
						for _, name := range GitVerseRequiredChecks {
							jobs = append(jobs, gitverseJob{ID: 2, RunID: 3, Name: name, Status: state}, gitverseJob{ID: 1, RunID: 3, Name: name, Status: "success"})
						}
					}
					json.NewEncoder(w).Encode(map[string]any{"jobs": jobs})
				} else {
					t.Errorf("read stale/foreign run: %s", r.URL.Path)
				}
			}))
			defer srv.Close()
			ev, err := FetchGitVerse(context.Background(), srv.Client(), srv.URL, "tok", commit, tree, snap)
			passed := err == nil && ev.Verify(commit, tree) == nil
			if passed != (state == "success") {
				t.Fatalf("state %s: %v %v", state, ev, err)
			}
		})
	}
}
