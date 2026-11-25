package gitprovider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitVerseCapacityFailureIsNotQuotaEvidence(t *testing.T) {
	for _, tc := range []struct{ status, code string }{{"waiting", "CI_NO_RUNNER"}, {"cancelled", "CI_PLATFORM_CANCELLED"}, {"running", ""}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"workflow_runs":[{"status":"` + tc.status + `"}]}`))
		}))
		c := &Client{site: GitVerse, base: server.URL, http: server.Client(), token: func(Site) (string, error) { return "fixture", nil }}
		err := c.gitverseRunnerHealth(context.Background())
		if tc.code == "" && err != nil || tc.code != "" && (err == nil || !strings.Contains(err.Error(), tc.code)) {
			t.Fatal(tc, err)
		}
		server.Close()
	}
}

func TestGitVerseMixedWaitingAndCancelledIsUnavailable(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"workflow_runs":[{"status":"waiting"},{"status":"cancelled"}]}`))
	}))
	defer s.Close()
	c := &Client{site: GitVerse, base: s.URL, http: s.Client(), token: func(Site) (string, error) { return "fixture", nil }}
	if err := c.gitverseRunnerHealth(context.Background()); err == nil || !strings.Contains(err.Error(), "CI_NO_RUNNER") {
		t.Fatal(err)
	}
}

func TestGitVerseRunnerHealthReadsOnlyTheNewestPage(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", "<https://gitverse.ru/public/api/x?page=2&per_page=10>; rel=\"next\"")
		w.Write([]byte(`{"workflow_runs":[{"status":"completed"}]}`))
	}))
	defer s.Close()
	c := &Client{site: GitVerse, base: s.URL, http: s.Client(), token: func(Site) (string, error) { return "fixture", nil }}
	if err := c.gitverseRunnerHealth(context.Background()); err != nil {
		t.Fatal("a repository with more than one page of runs is healthy:", err)
	}
}
