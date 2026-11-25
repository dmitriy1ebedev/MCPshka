package cievidence

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	commit = "1111111111111111111111111111111111111111"
	tree   = "2222222222222222222222222222222222222222"
	base   = "3333333333333333333333333333333333333333"
)

type fixture struct {
	runs    []map[string]any
	compare string
	behind  int
}

func (f fixture) server(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/commits/"+commit+"/check-runs"):
			json.NewEncoder(w).Encode(map[string]any{"check_runs": f.runs})
		case strings.HasSuffix(r.URL.Path, "/git/commits/"+commit):
			json.NewEncoder(w).Encode(map[string]any{"tree": map[string]string{"sha": tree}})
		case strings.HasSuffix(r.URL.Path, "/compare/"+base+"..."+commit):
			json.NewEncoder(w).Encode(map[string]any{"status": f.compare, "behind_by": f.behind})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func passing() []map[string]any {
	var runs []map[string]any
	for i, name := range RequiredChecks {
		runs = append(runs, map[string]any{"id": 100 + i, "name": name, "head_sha": commit, "status": "completed", "conclusion": "success",
			"pull_requests": []any{map[string]any{"base": map[string]string{"sha": base}}}})
	}
	return runs
}

func fetch(t *testing.T, f fixture) (Evidence, error) {
	s := f.server(t)
	defer s.Close()
	return Fetch(context.Background(), s.Client(), s.URL, "secret", commit)
}

func TestFetchAcceptsAllRequiredChecksOnAncestorBase(t *testing.T) {
	ev, err := fetch(t, fixture{runs: passing(), compare: "ahead"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ev.Verify(commit, tree); err != nil {
		t.Fatal(err)
	}
	if ev.Verify(commit, base) == nil || ev.Verify(base, tree) == nil {
		t.Fatal("evidence accepted for another commit or tree")
	}
}

func TestFetchUsesLatestRerunOfEachCheck(t *testing.T) {
	runs := passing()
	// An older failed attempt of the same job does not override the newer pass.
	runs = append(runs, map[string]any{"id": 1, "name": "checks (race)", "head_sha": commit, "status": "completed", "conclusion": "failure"})
	ev, err := fetch(t, fixture{runs: runs, compare: "ahead"})
	if err != nil || ev.Verify(commit, tree) != nil {
		t.Fatal("newer passing rerun rejected", err)
	}
	// A newer failure wins over an older pass.
	runs = append(passing(), map[string]any{"id": 999, "name": "checks (race)", "head_sha": commit, "status": "completed", "conclusion": "failure"})
	ev, err = fetch(t, fixture{runs: runs, compare: "ahead"})
	if err != nil || !errors.Is(ev.Verify(commit, tree), ErrHold) {
		t.Fatal("newer failed rerun accepted", err)
	}
}

func TestFetchRejectsIncompleteRunsAndForeignBase(t *testing.T) {
	missing := passing()[1:]
	if _, err := fetch(t, fixture{runs: missing, compare: "ahead"}); !errors.Is(err, ErrHold) {
		t.Fatal("missing required check accepted", err)
	}
	pending := passing()
	pending[0]["status"] = "in_progress"
	pending[0]["conclusion"] = nil
	if ev, err := fetch(t, fixture{runs: pending, compare: "ahead"}); err != nil || ev.Verify(commit, tree) == nil {
		t.Fatal("unfinished check accepted", err)
	}
	other := passing()
	other[0]["head_sha"] = base
	if _, err := fetch(t, fixture{runs: other, compare: "ahead"}); !errors.Is(err, ErrHold) {
		t.Fatal("check for another commit counted", err)
	}
	noBase := passing()
	for _, r := range noBase {
		delete(r, "pull_requests")
	}
	if _, err := fetch(t, fixture{runs: noBase}); !errors.Is(err, ErrHold) {
		t.Fatal("run without a PR base accepted", err)
	}
}

func TestFetchRequiresTokenAndExactCommit(t *testing.T) {
	s := fixture{runs: passing(), compare: "ahead"}.server(t)
	defer s.Close()
	if _, err := Fetch(context.Background(), s.Client(), s.URL, "wrong", commit); err == nil {
		t.Fatal("unauthenticated response accepted")
	}
	if _, err := Fetch(context.Background(), s.Client(), s.URL, "secret", "main"); !errors.Is(err, ErrHold) {
		t.Fatal("symbolic ref accepted")
	}
}
