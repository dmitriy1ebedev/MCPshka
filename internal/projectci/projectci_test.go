package projectci

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeGitVerse(t *testing.T, public bool) (*Client, *[]string, *[]string) {
	var api, git []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api = append(api, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("no token on %s", r.URL.Path)
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/dmitriy_1ebedev/diag":
			if public {
				_ = json.NewEncoder(w).Encode(Repo{Name: "diag", Private: false})
				return
			}
			w.WriteHeader(404)
		case r.Method == "POST" && r.URL.Path == "/user/repos":
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			_ = json.NewEncoder(w).Encode(Repo{Name: "diag", Private: in["private"] == true, DefaultBranch: "master"})
		case strings.HasSuffix(r.URL.Path, "/actions/runs"):
			_, _ = w.Write([]byte(`{"workflow_runs":[{"id":7,"commit_sha":"` + strings.Repeat("a", 40) + `","status":"in_progress"},{"id":3,"commit_sha":"` + strings.Repeat("b", 40) + `","status":"success"}]}`))
		case strings.HasSuffix(r.URL.Path, "/runs/7/jobs"):
			_, _ = w.Write([]byte(`{"jobs":[{"name":"test","status":"failure"}]}`))
		default:
			w.WriteHeader(200)
		}
	}))
	t.Cleanup(srv.Close)
	old := API
	API = srv.URL
	t.Cleanup(func() { API = old })
	c := &Client{HTTP: srv.Client(), Token: func() (string, error) { return "tok", nil },
		Git: func(_ context.Context, dir string, env []string, args ...string) ([]byte, error) {
			git = append(git, strings.Join(env, ",")+"|"+strings.Join(args, " "))
			if strings.Contains(strings.Join(args, " "), PublishFile) {
				return nil, errors.New("no publish.json")
			}
			return []byte("pushed with tok\n"), nil
		}}
	return c, &api, &git
}

// A new project gets a PRIVATE repo; main is mirrored exactly (forced) and
// the token never shows up in arguments or output.
func TestPublishCreatesPrivateMirror(t *testing.T) {
	c, api, git := fakeGitVerse(t, false)
	sha := strings.Repeat("a", 40)
	res, err := c.Publish(context.Background(), "/root/repos/diag", sha)
	if err != nil || !res.Created || !res.Private {
		t.Fatalf("publish: %+v %v", res, err)
	}
	if !strings.Contains(strings.Join(*api, ";"), "POST /user/repos") {
		t.Fatalf("repo not created: %v", *api)
	}
	push := (*git)[len(*git)-1]
	if !strings.Contains(push, "+"+sha+":refs/heads/main") || strings.Contains(strings.SplitN(push, "|", 2)[1], "tok") {
		t.Fatalf("push: %s", push)
	}
	if strings.Contains(res.Output, "tok") {
		t.Fatal("token leaked into output")
	}
	if _, err := c.Publish(context.Background(), "/root/repos/mcpshka", sha); err == nil {
		t.Fatal("protected MCPshka routed through the mirror")
	}
	if _, err := c.Publish(context.Background(), "/etc", sha); err == nil {
		t.Fatal("path outside /root/repos accepted")
	}
}

// MCP-206: a provider-side 403 must be actionable and must not expose
// the HTTPS credential, including if Git echoed it in its diagnostic.
func TestPublishGitVerse403HasRedactedRemoteEvidence(t *testing.T) {
	c, _, _ := fakeGitVerse(t, false)
	original := c.Git
	c.Git = func(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
		for _, arg := range args {
			if arg == "push" {
				return []byte("error: RPC failed; HTTP 403\\nremote: access denied for tok\\n"), errors.New("exit status 128")
			}
		}
		return original(ctx, dir, env, args...)
	}
	out, err := c.Publish(context.Background(), "/root/repos/diag", strings.Repeat("a", 40))
	if err == nil || !strings.Contains(err.Error(), "GITVERSE_PUSH_HTTP_403") ||
		!strings.Contains(err.Error(), "remote: access denied") ||
		!strings.Contains(out.Next, "PRIVATE repository write permission") {
		t.Fatalf("missing actionable HTTP 403 receipt: %+v %v", out, err)
	}
	if strings.Contains(err.Error(), "tok") || strings.Contains(out.Output, "tok") {
		t.Fatal("credential leak from failed push")
	}
}

func TestMCP232VerifiedWorktreeUsesCanonicalProtectedMirror(t *testing.T) {
	if name, err := RepoName("/root/repos/diag"); err != nil || name != "diag" {
		t.Fatal("immediate project mirror changed", name, err)
	}
	for _, path := range []string{
		"/root/repos/wt/foreign-nonexistent-repo",
		"/root/repos/wt/lane-agent-unknown",
		"/root/repos/wt/../lane",
		"/tmp/lane",
	} {
		if name, err := RepoName(path); err == nil {
			t.Fatalf("unverified worktree %q mirrored as %q", path, name)
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	const protected = "/root/repos/mcpshka"
	root := filepath.Dir(filepath.Dir(wd))
	if filepath.Dir(root) == "/root/repos/wt" {
		if got := canonicalMirrorRoot(root); got != protected {
			t.Fatalf("trusted source worktree did not map to original Git common dir: %q", got)
		}
		if _, err := RepoName(root); err == nil || !strings.Contains(err.Error(), "MCPshka itself") {
			t.Fatal("worktree basename bypassed protected repository identity", err)
		}
	}
	if _, err := RepoName(protected); err == nil || !strings.Contains(err.Error(), "MCPshka itself") {
		t.Fatal("canonical protected repository bypassed mirror protection", err)
	}
}

func TestPublicRepoRefused(t *testing.T) {
	c, _, git := fakeGitVerse(t, true)
	if _, err := c.Publish(context.Background(), "/root/repos/diag", strings.Repeat("a", 40)); err == nil || !strings.Contains(err.Error(), "PUBLIC") {
		t.Fatalf("public repo: %v", err)
	}
	for _, g := range *git {
		if strings.Contains(g, "push") {
			t.Fatal("pushed to a public repository")
		}
	}
}

func TestStatusVerdict(t *testing.T) {
	c, _, _ := fakeGitVerse(t, false)
	st, err := c.ReadStatus(context.Background(), "/root/repos/diag", strings.Repeat("a", 40))
	if err != nil || st.RunID != 7 || st.State != "FAIL" {
		t.Fatalf("status: %+v %v", st, err)
	}
	st, _ = c.ReadStatus(context.Background(), "/root/repos/diag", strings.Repeat("c", 40))
	if st.State != "NO_RUN" {
		t.Fatalf("no run: %+v", st)
	}
}

func TestFailureExcerptStopsAtFirstError(t *testing.T) {
	log := "a\nsh tests/run_unit.sh\nmissing test python\n##[error]Process completed with exit code 1.\nPost Checkout\n"
	got := FailureExcerpt(log)
	if !strings.Contains(got, "missing test python") || strings.Contains(got, "Post Checkout") {
		t.Fatal(got)
	}
}

// MCP-158: rerun dispatches only workflows with workflow_dispatch, and only
// when GitVerse main already is the commit.
func TestRerunDispatchesSameCommit(t *testing.T) {
	sha := strings.Repeat("c", 40)
	var api []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api = append(api, r.Method+" "+r.URL.RequestURI())
		switch {
		case strings.HasSuffix(r.URL.Path, "/branches/main"):
			_, _ = w.Write([]byte(`{"commit":{"id":"` + sha + `"}}`))
		case strings.HasSuffix(r.URL.Path, "/dispatches"):
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	old := API
	API = srv.URL
	defer func() { API = old }()
	c := &Client{HTTP: srv.Client(), Token: func() (string, error) { return "tok", nil },
		Git: func(_ context.Context, dir string, env []string, args ...string) ([]byte, error) {
			if strings.Contains(strings.Join(args, " "), PublishFile) {
				return nil, errors.New("no publish.json")
			}
			if args[0] == "ls-tree" {
				return []byte(".gitverse/workflows/ci.yml\n.gitverse/workflows/nightly.yml\n"), nil
			}
			if strings.HasSuffix(args[1], "ci.yml") {
				return []byte("on:\n  push:\n  workflow_dispatch:\n"), nil
			}
			return []byte("on:\n  schedule:\n"), nil
		}}
	res, err := c.Rerun(context.Background(), "/root/repos/lane", sha)
	if err != nil || res["state"] != "DISPATCHED" {
		t.Fatalf("rerun: %v %v", res, err)
	}
	got := strings.Join(api, ";")
	if !strings.Contains(got, "POST /repos/dmitriy_1ebedev/lane/actions/workflows/ci.yml/dispatches?branch=main") || strings.Contains(got, "nightly") {
		t.Fatalf("dispatches: %s", got)
	}
	if _, err := c.Rerun(context.Background(), "/root/repos/lane", strings.Repeat("d", 40)); err == nil || !strings.Contains(err.Error(), "publish this commit first") {
		t.Fatalf("stale main must be refused: %v", err)
	}
}

func TestInfraFailureIsNotCodeVerdict(t *testing.T) {
	if !infraRE.MatchString("Error: Failed to download Python: HTTP Error 504: Gateway Time-out") || !infraRE.MatchString("##[error]Unexpected HTTP response: 504") || infraRE.MatchString("FAIL: test_router_baseline") {
		t.Fatal("infra pattern")
	}
}

// One wait job holds a whole CI run: it polls through NO_RUN and RUNNING
// and returns the terminal verdict.
func TestWaitHoldsUntilTerminal(t *testing.T) {
	sha := strings.Repeat("e", 40)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/actions/runs"):
			calls++
			if calls == 1 {
				_, _ = w.Write([]byte(`{"workflow_runs":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"workflow_runs":[{"id":9,"commit_sha":"` + sha + `","status":"in_progress"}]}`))
		case strings.HasSuffix(r.URL.Path, "/runs/9/jobs"):
			state := "in_progress"
			if calls >= 3 {
				state = "success"
			}
			_, _ = w.Write([]byte(`{"jobs":[{"id":1,"name":"unit","status":"` + state + `"}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	old := API
	API = srv.URL
	defer func() { API = old }()
	c := &Client{HTTP: srv.Client(), Token: func() (string, error) { return "tok", nil }}
	st, err := c.Wait(context.Background(), "/root/repos/lane", sha, time.Millisecond)
	if err != nil || st.State != "PASS" || calls != 3 {
		t.Fatalf("wait: %+v %v calls=%d", st, err, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls = 1
	st, _ = c.Wait(ctx, "/root/repos/lane", sha, time.Hour)
	if st.State == "PASS" || !strings.Contains(st.Next, "mode=wait again") {
		t.Fatalf("expired wait must say continue: %+v", st)
	}
}

// MCP-272: an unrelated downstream release workflow can finish after the
// primary validation on the same Git SHA. It must never hide a passing CI run.
func mcp272CIClient(t *testing.T, runs, jobs, selectedRun string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/actions/runs"):
			_, _ = w.Write([]byte(runs))
		case strings.HasSuffix(r.URL.Path, "/runs/"+selectedRun+"/jobs"):
			_, _ = w.Write([]byte(jobs))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	old := API
	API = srv.URL
	t.Cleanup(func() { API = old })
	return &Client{
		HTTP:  srv.Client(),
		Token: func() (string, error) { return "tok", nil },
		Git: func(_ context.Context, _ string, _ []string, args ...string) ([]byte, error) {
			if strings.Contains(strings.Join(args, " "), PublishFile) {
				return nil, errors.New("no publish.json")
			}
			return []byte("stub"), nil
		},
	}
}

func TestMCP272SkippedPublisherCannotMaskValidGitVerseCI(t *testing.T) {
	sha := strings.Repeat("a", 40)
	runs := `{"workflow_runs":[{"id":2190544,"commit_sha":"` + sha + `","event":"workflow_run","ref":"refs/heads/main","status":"skipped"},{"id":2190525,"commit_sha":"` + sha + `","event":"push","ref":"refs/heads/main","status":"success"}]}`
	jobs := `{"jobs":[{"id":1,"name":"Python tests and repository checks","status":"success"},{"id":2,"name":"HACS / Marketplace validation","status":"skipped"},{"id":3,"name":"Disposable Home Assistant smoke","status":"skipped"}]}`
	c := mcp272CIClient(t, runs, jobs, "2190525")
	st, err := c.ReadStatus(context.Background(), "/root/repos/diag", sha)
	if err != nil || st.RunID != 2190525 || st.State != "PASS" {
		t.Fatalf("selected skipped downstream run instead of passing validation: %+v, %v", st, err)
	}
	if st.Jobs["HACS / Marketplace validation"] != "skipped" {
		t.Fatalf("missing non-applicable job in evidence: %+v", st.Jobs)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	st, err = c.Wait(ctx, "/root/repos/diag", sha, time.Millisecond)
	if err != nil || st.State != "PASS" || st.RunID != 2190525 {
		t.Fatalf("wait must terminate on primary CI success: %+v, %v", st, err)
	}
}

func TestMCP272AllSkippedPrimaryGitVerseJobsFailClosed(t *testing.T) {
	sha := strings.Repeat("b", 40)
	runs := `{"workflow_runs":[{"id":2190623,"commit_sha":"` + sha + `","event":"push","ref":"refs/heads/main","status":"skipped"}]}`
	jobs := `{"jobs":[{"id":5,"name":"unit","status":"skipped"},{"id":6,"name":"hassfest","status":"skipped"}]}`
	c := mcp272CIClient(t, runs, jobs, "2190623")
	st, err := c.ReadStatus(context.Background(), "/root/repos/diag", sha)
	if err != nil || st.State != "FAIL" || !strings.Contains(st.Next, "all GitVerse CI jobs skipped") {
		t.Fatalf("all skipped must not be green or run forever: %+v, %v", st, err)
	}
	if Verdict("skipped") != "FAIL" {
		t.Fatal("a skipped primary workflow is not a passing test")
	}
}

func TestMCP272PublisherOnlyCannotSupplyProjectCIEvidence(t *testing.T) {
	sha := strings.Repeat("c", 40)
	runs := `{"workflow_runs":[{"id":2190640,"commit_sha":"` + sha + `","event":"workflow_run","ref":"refs/heads/main","status":"skipped"}]}`
	c := mcp272CIClient(t, runs, `{"jobs":[]}`, "2190640")
	st, err := c.ReadStatus(context.Background(), "/root/repos/diag", sha)
	if err != nil || st.State != "NO_RUN" || st.RunID != 0 {
		t.Fatalf("downstream-only workflow is not project CI: %+v, %v", st, err)
	}
}
