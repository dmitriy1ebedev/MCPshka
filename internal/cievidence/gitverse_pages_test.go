package cievidence

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// BUG-20261103-32: with more than 150 runs the third page is full; the
// snapshot run on page 1 of a newest-first listing must still be accepted,
// while an unordered full listing stays on HOLD.
func TestGitVerseManyRunsNewestFirst(t *testing.T) {
	commit, tree, snap := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	ordered := true
	pages := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/actions/runs"):
			pages++
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			runs := make([]gitverseRun, 0, 50)
			for i := 0; i < 50; i++ {
				id := int64(1000 - (page-1)*50 - i)
				if !ordered && i%2 == 1 {
					id += 3
				}
				run := gitverseRun{ID: id, Ref: "refs/heads/other", CommitSHA: strings.Repeat("e", 40), Status: "success", Event: "push"}
				if page == 1 && i == 3 {
					run = gitverseRun{ID: id, Ref: "refs/heads/mcpshka-snapshots/" + commit, CommitSHA: snap, Status: "success", Event: "push"}
				}
				runs = append(runs, run)
			}
			json.NewEncoder(w).Encode(map[string]any{"workflow_runs": runs})
		case strings.HasSuffix(r.URL.Path, "/jobs"):
			id, _ := strconv.ParseInt(strings.Split(r.URL.Path, "/")[len(strings.Split(r.URL.Path, "/"))-2], 10, 64)
			json.NewEncoder(w).Encode(map[string]any{"jobs": []gitverseJob{{ID: 1, RunID: id, Name: "release-suite", Status: "success"}, {ID: 2, RunID: id, Name: "snapshot-contracts", Status: "success"}}})
		}
	}))
	defer srv.Close()
	ev, err := FetchGitVerse(context.Background(), srv.Client(), srv.URL, "tok", commit, tree, snap)
	if err != nil || ev.Verify(commit, tree) != nil || pages != 1 {
		t.Fatalf("newest-first listing: pages=%d err=%v", pages, err)
	}
	ordered, pages = false, 0
	if _, err := FetchGitVerse(context.Background(), srv.Client(), srv.URL, "tok", commit, tree, snap); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("unordered full listing accepted: %v", err)
	}
}
