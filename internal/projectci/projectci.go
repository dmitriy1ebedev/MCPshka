// Package projectci moves full checks of any H96 project off the box: it
// mirrors a committed main to a PRIVATE GitVerse repository of the same name
// (created on first use, never public — owner 08.11: «приватные всегда»),
// where the project's own .gitverse/workflows run, and reads the result back.
// It is a mirror: H96 holds the history, so GitVerse main is set exactly to
// the commit (forced, owner 08.11 — clean mirrors, no divergence), or to
// its parentless code-only snapshot when the commit's .mcpshka/publish.json
// says so (snapshot.go; owner 14.11 «только код»). MCPshka's
// own protected main is not a mirror and never goes through here.
// A public repository is refused, never pushed.
package projectci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mcpshka/internal/repopath"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	Owner     = "dmitriy_1ebedev"
	maxBody   = 1 << 20
	accept    = "application/vnd.gitverse.object+json;version=1"
	userAgent = "mcpshka-project-ci"
)

var (
	API       = "https://api.gitverse.ru"
	GitHost   = "https://gitverse.ru"
	TokenFile = "/var/lib/mcpshka/gitverse/api_token"
	nameRE    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	shaRE     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	// Protected has its own pipeline (forge_ci, integration gate).
	Protected = map[string]bool{"mcpshka": true}
)

// Client talks to GitVerse; fields are replaceable in tests.
type Client struct {
	HTTP  *http.Client
	Token func() (string, error)
	Git   func(ctx context.Context, dir string, env []string, args ...string) ([]byte, error)
}

func New() *Client {
	return &Client{
		HTTP: &http.Client{Timeout: 30 * time.Second},
		Token: func() (string, error) {
			raw, err := os.ReadFile(TokenFile)
			if err != nil {
				return "", errors.New("GitVerse token unavailable: add it on «Подключения»")
			}
			return strings.TrimSpace(string(raw)), nil
		},
		Git: func(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
			cmd.Env = append(os.Environ(), env...)
			return cmd.CombinedOutput()
		},
	}
}

// canonicalMirrorRoot binds a project worktree to its parent repository.
// A nested worktree must have an independently verified .git link; its
// directory basename is NEVER used as the GitVerse repository name.
func canonicalMirrorRoot(root string) string {
	if filepath.Clean(root) != root {
		return ""
	}
	if filepath.Dir(root) == "/root/repos" {
		return root
	}
	return repopath.Worktree(root)
}

// RepoName is the GitVerse repository for a canonical project or its verified
// linked worktree. In particular, a protected MCPshka worktree cannot publish
// into a second, accidentally unprotected repository name (MCP-232).
func RepoName(root string) (string, error) {
	canonical := canonicalMirrorRoot(root)
	if canonical == "" {
		return "", errors.New("path must be a canonical /root/repos project or a verified linked worktree")
	}
	name := strings.ToLower(filepath.Base(canonical))
	if !nameRE.MatchString(name) {
		return "", errors.New("invalid project repository name")
	}
	if Protected[name] {
		return "", errors.New("MCPshka itself uses forge_ci and the integration gate, not project_ci")
	}
	return name, nil
}

type Repo struct {
	Name          string `json:"name"`
	Private       bool   `json:"private"`
	HTMLURL       string `json:"html_url"`
	DefaultBranch string `json:"default_branch"`
}

func (c *Client) call(ctx context.Context, method, path string, body any, out any) (int, error) {
	tok, err := c.Token()
	if err != nil {
		return 0, err
	}
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, API+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("gitverse %s %s: HTTP %d", method, path, resp.StatusCode)
	}
	if p, ok := out.(*[]byte); ok {
		*p = raw
		return resp.StatusCode, nil
	}
	if out != nil {
		return resp.StatusCode, json.Unmarshal(raw, out)
	}
	return resp.StatusCode, nil
}

// Ensure returns the private repository, creating it when missing. An
// existing public repository is refused: project code never goes public.
func (c *Client) Ensure(ctx context.Context, name string) (Repo, bool, error) {
	var r Repo
	code, err := c.call(ctx, http.MethodGet, "/repos/"+Owner+"/"+name, nil, &r)
	created := false
	if code == http.StatusNotFound {
		created = true
		_, err = c.call(ctx, http.MethodPost, "/user/repos", map[string]any{"name": name, "private": true, "description": "Зеркало проекта H96 для CI (MCPshka)"}, &r)
	}
	if err != nil {
		return r, created, err
	}
	if !r.Private {
		return r, created, fmt.Errorf("GitVerse repository %s/%s is PUBLIC: refusing to push; make it private first", Owner, name)
	}
	if created && r.DefaultBranch != "main" {
		_, _ = c.call(ctx, http.MethodPatch, "/repos/"+Owner+"/"+name, map[string]any{"default_branch": "main", "private": true}, nil)
	}
	return r, created, nil
}

// PublishResult reports one mirror push.
type PublishResult struct {
	Repository string `json:"repository"`
	URL        string `json:"url"`
	Private    bool   `json:"private"`
	Created    bool   `json:"created"`
	Commit     string `json:"commit"`
	// Snapshot is the parentless code-only commit the site got instead of
	// Commit (.mcpshka/publish.json mode code_only); empty for a full mirror.
	Snapshot string `json:"snapshot,omitempty"`
	Workflow bool   `json:"workflow"`
	Output   string `json:"output,omitempty"`
	Next     string `json:"next"`
}

// Publish sets the mirror's main to exactly commit.
func (c *Client) Publish(ctx context.Context, root, commit string) (PublishResult, error) {
	name, err := RepoName(root)
	if err != nil {
		return PublishResult{}, err
	}
	if commit == "" {
		out, err := c.Git(ctx, root, nil, "rev-parse", "HEAD")
		if err != nil {
			return PublishResult{}, fmt.Errorf("git rev-parse: %s", strings.TrimSpace(string(out)))
		}
		commit = strings.TrimSpace(string(out))
	}
	if !shaRE.MatchString(commit) {
		return PublishResult{}, errors.New("commit must be a full 40-hex SHA")
	}
	repo, created, err := c.Ensure(ctx, name)
	if err != nil {
		return PublishResult{}, err
	}
	res := PublishResult{Repository: Owner + "/" + name, URL: repo.HTMLURL, Private: repo.Private, Created: created, Commit: commit}
	target, cfg, err := c.siteCommit(ctx, root, commit)
	if err != nil {
		return res, err
	}
	if cfg.Mode == "code_only" {
		res.Snapshot = target
	}
	_, werr := c.Git(ctx, root, nil, "cat-file", "-e", commit+":.gitverse/workflows")
	res.Workflow = werr == nil
	tok, err := c.Token()
	if err != nil {
		return res, err
	}
	helper := `credential.helper=!f(){ echo username=` + Owner + `; printf 'password=%s\n' "$MCPSHKA_GV_TOKEN"; }; f`
	out, err := c.Git(ctx, root, []string{"MCPSHKA_GV_TOKEN=" + tok, "GIT_TERMINAL_PROMPT=0"},
		"-c", "credential.helper=", "-c", helper, "push", GitHost+"/"+Owner+"/"+name+".git", "+"+target+":refs/heads/main")
	res.Output = bounded(strings.ReplaceAll(string(out), tok, "***"), 2048)
	if err != nil {
		// GitVerse 403 for one private repository is not a CI/test failure.
		// Include only bounded, token-redacted Git output; previously the
		// useful "remote:" and HTTP diagnostics were omitted from the error.
		if strings.Contains(res.Output, "HTTP 403") || strings.Contains(res.Output, "403 Forbidden") {
			res.Next = "GITVERSE_PUSH_HTTP_403: inspect PRIVATE repository write permission, token scope and provider quota; do not blindly retry or change remote history"
			return res, fmt.Errorf("GITVERSE_PUSH_HTTP_403: %v; git output: %s", err, res.Output)
		}
		res.Next = "inspect the bounded Git push output and remote permissions before retrying"
		return res, fmt.Errorf("git push: %v; git output: %s", err, res.Output)
	}
	res.Next = "forge_ci provider=gitverse mode=wait path=" + root + " commit=" + commit + ", then job_wait until done (one job holds the whole CI run; do not stop or report while it runs)"
	if !res.Workflow {
		res.Next = "no .gitverse/workflows in this commit: CI will not start. Add .gitverse/workflows/ci.yml (on: push to main), commit, publish again"
	}
	return res, nil
}

// Status is the CI verdict for one commit.
type Status struct {
	Repository string            `json:"repository"`
	Commit     string            `json:"commit"`
	State      string            `json:"state"` // NO_RUN, RUNNING, PASS, FAIL
	RunID      int64             `json:"run_id,omitempty"`
	Jobs       map[string]string `json:"jobs,omitempty"`
	// Failures holds the log excerpt of each failed job: a brain must see
	// the actual error, not «unit: failure» (Lane CI 09.11).
	Failures map[string]string `json:"failures,omitempty"`
	URL      string            `json:"url,omitempty"`
	// Infra marks a failure of the CI site itself (HTTP 5xx while fetching
	// tools, DNS): not a code verdict — rerun the same commit (MCP-158).
	Infra bool   `json:"infra,omitempty"`
	Next  string `json:"next,omitempty"`
	Trust string `json:"trust"`
}

type run struct {
	ID        int64  `json:"id"`
	CommitSHA string `json:"commit_sha"`
	HeadSHA   string `json:"head_sha"`
	Event     string `json:"event"`
	Ref       string `json:"ref"`
	Status    string `json:"status"`
}

// Verdict maps one GitVerse status word. A skipped primary workflow cannot
// establish CI success, while skipped *jobs* are handled as non-applicable.
func Verdict(s string) string {
	switch strings.ToLower(s) {
	case "success", "completed_success", "passed":
		return "PASS"
	case "failure", "failed", "error", "cancelled", "canceled", "timed_out", "skipped_failure", "skipped":
		return "FAIL"
	}
	return "RUNNING"
}

// ReadStatus finds the newest run for commit and its jobs.
func (c *Client) ReadStatus(ctx context.Context, root, commit string) (Status, error) {
	name, err := RepoName(root)
	if err != nil {
		return Status{}, err
	}
	if !shaRE.MatchString(commit) {
		return Status{}, errors.New("commit must be a full 40-hex SHA")
	}
	st := Status{Repository: Owner + "/" + name, Commit: commit, State: "NO_RUN", Trust: "CI evidence from GitVerse"}
	target, _, err := c.siteCommit(ctx, root, commit)
	if err != nil {
		return st, err
	}
	var listing struct {
		WorkflowRuns []run `json:"workflow_runs"`
	}
	if _, err := c.call(ctx, http.MethodGet, "/repos/"+Owner+"/"+name+"/actions/runs?per_page=50", nil, &listing); err != nil {
		return st, err
	}
	var latest run
	for _, r := range listing.WorkflowRuns {
		if r.CommitSHA != target && r.HeadSHA != target {
			continue
		}
		// This mirror's CI runs on main push or explicit dispatch. A later
		// workflow_run (e.g. a skipped GitHub-only release publisher) is not
		// evidence for the primary project validation.
		if r.Event != "" && r.Event != "push" && r.Event != "workflow_dispatch" {
			continue
		}
		if r.Ref != "" && r.Ref != "refs/heads/main" && r.Ref != "main" {
			continue
		}
		if r.ID > latest.ID {
			latest = r
		}
	}
	if latest.ID == 0 {
		return st, nil
	}
	st.RunID, st.State = latest.ID, Verdict(latest.Status)
	st.URL = GitHost + "/" + Owner + "/" + name + "/tasktracker"
	var jobs struct {
		Jobs []struct {
			ID     int64  `json:"id"`
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"jobs"`
	}
	if _, err := c.call(ctx, http.MethodGet, "/repos/"+Owner+"/"+name+"/actions/runs/"+strconv.FormatInt(latest.ID, 10)+"/jobs", nil, &jobs); err == nil && len(jobs.Jobs) > 0 {
		st.Jobs = map[string]string{}
		state := "PASS"
		applicable := 0
		for _, j := range jobs.Jobs {
			st.Jobs[j.Name] = j.Status
			// GitVerse skips non-applicable HA/HACS jobs without invalidating
			// successful native tests. Skips must never keep CI RUNNING.
			if strings.EqualFold(j.Status, "skipped") {
				continue
			}
			applicable++
			switch Verdict(j.Status) {
			case "FAIL":
				state = "FAIL"
				var raw []byte
				if j.ID > 0 {
					if _, err := c.call(ctx, http.MethodGet, "/repos/"+Owner+"/"+name+"/actions/jobs/"+strconv.FormatInt(j.ID, 10)+"/logs", nil, &raw); err == nil {
						if st.Failures == nil {
							st.Failures = map[string]string{}
						}
						st.Failures[j.Name] = FailureExcerpt(string(raw))
					}
				}
			case "RUNNING":
				if state != "FAIL" {
					state = "RUNNING"
				}
			}
		}
		if applicable == 0 {
			st.State = "FAIL"
			st.Next = "all GitVerse CI jobs skipped: no applicable verification"
		} else {
			st.State = state
		}
	}
	if st.State == "FAIL" && len(st.Failures) > 0 {
		st.Infra = true
		for _, ex := range st.Failures {
			if !infraRE.MatchString(ex) {
				st.Infra = false
			}
		}
		if st.Infra {
			st.Next = "CI site failed before the tests (not a code error): forge_ci provider=gitverse mode=rerun path=" + root + " commit=" + commit
		}
	}
	return st, nil
}

var infraRE = regexp.MustCompile(`(?i)HTTP[^\n0-9]{0,20}50[234]|Gateway Time-?out|Could not resolve host|Temporary failure in name resolution|ECONNRESET|rate limit`)

// Rerun starts the project's dispatchable workflows again on the same
// commit, which must already be the mirror's main (publish first). One POST
// per workflow, never retried: an ambiguous answer means "read status".
func (c *Client) Rerun(ctx context.Context, root, commit string) (map[string]any, error) {
	name, err := RepoName(root)
	if err != nil {
		return nil, err
	}
	if !shaRE.MatchString(commit) {
		return nil, errors.New("commit must be a full 40-hex SHA")
	}
	var br struct {
		Commit struct {
			ID  string `json:"id"`
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if _, err := c.call(ctx, http.MethodGet, "/repos/"+Owner+"/"+name+"/branches/main", nil, &br); err != nil {
		return nil, err
	}
	target, _, err := c.siteCommit(ctx, root, commit)
	if err != nil {
		return nil, err
	}
	if head := br.Commit.ID + br.Commit.SHA; head != target {
		return nil, fmt.Errorf("GitVerse main of %s is %.12s, not %.12s: forge_ci mode=publish this commit first", name, head, commit)
	}
	out, err := c.Git(ctx, root, nil, "ls-tree", "--name-only", commit, ".gitverse/workflows/")
	if err != nil {
		return nil, fmt.Errorf("git ls-tree: %s", strings.TrimSpace(string(out)))
	}
	res := map[string]any{"repository": Owner + "/" + name, "commit": commit}
	var started []string
	for _, f := range strings.Fields(string(out)) {
		body, err := c.Git(ctx, root, nil, "show", commit+":"+f)
		if err != nil || !bytes.Contains(body, []byte("workflow_dispatch")) {
			continue
		}
		wf := filepath.Base(f)
		if _, err := c.call(ctx, http.MethodPost, "/repos/"+Owner+"/"+name+"/actions/workflows/"+wf+"/dispatches?branch=main", map[string]any{}, nil); err != nil {
			res["started"], res["state"] = started, "UNKNOWN"
			res["next"] = "dispatch of " + wf + " unverified (" + err.Error() + "): read forge_ci mode=status, do not repeat blindly"
			return res, nil
		}
		started = append(started, wf)
	}
	if len(started) == 0 {
		return nil, errors.New("no workflow with `on: workflow_dispatch` in .gitverse/workflows of this commit: add it, commit, publish")
	}
	res["started"], res["state"] = started, "DISPATCHED"
	res["next"] = "forge_ci provider=gitverse mode=wait path=" + root + " commit=" + commit + ", then job_wait until done (the newest run counts)"
	return res, nil
}

// FailureExcerpt keeps the 40 log lines before the first error annotation
// (or the last 40 lines), at most 4 KiB.
func FailureExcerpt(log string) string {
	lines := strings.Split(strings.ReplaceAll(log, "\r", ""), "\n")
	end := len(lines)
	for i, l := range lines {
		if strings.Contains(l, "##[error]") {
			end = i + 1
			break
		}
	}
	start := end - 40
	if start < 0 {
		start = 0
	}
	return bounded(strings.Join(lines[start:end], "\n"), 4096)
}

func bounded(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// Wait polls the commit's CI until it ends (PASS/FAIL) or ctx expires, so a
// chat holds one job with job_wait instead of giving up on a one-shot
// RUNNING (Lane 09.11: «ChatGPT останавливается на каждом CI»). A run that
// has not started yet (NO_RUN right after publish) is waited for too.
func (c *Client) Wait(ctx context.Context, root, commit string, every time.Duration) (Status, error) {
	for {
		st, err := c.ReadStatus(ctx, root, commit)
		if err == nil && (st.State == "PASS" || st.State == "FAIL") {
			if st.State == "FAIL" && !st.Infra {
				st.Next = "code failure: read failures, fix, commit, forge_ci mode=publish, then mode=wait again"
			}
			return st, nil
		}
		select {
		case <-ctx.Done():
			st.Next = "CI still running: start forge_ci mode=wait again and keep job_wait; meanwhile continue independent work"
			return st, nil
		case <-time.After(every):
		}
	}
}

// Run is the forge-ci job entry for another project: publish, status, rerun or wait.
// It prints one JSON line; exit 1 only when the observation itself failed.
func Run(ctx context.Context, mode, commit, root string) ([]byte, int, error) {
	c := New()
	var v any
	var err error
	switch mode {
	case "publish":
		v, err = c.Publish(ctx, root, commit)
	case "status":
		v, err = c.ReadStatus(ctx, root, commit)
	case "rerun":
		v, err = c.Rerun(ctx, root, commit)
	case "wait":
		v, err = c.Wait(ctx, root, commit, 20*time.Second)
	default:
		return nil, 2, errors.New("project CI modes: publish, status, rerun, wait")
	}
	if err != nil {
		raw, _ := json.Marshal(map[string]any{"error": err.Error(), "result": v})
		return raw, 1, nil
	}
	raw, err := json.Marshal(v)
	return raw, 0, err
}

// PassedOn is off-H96 test evidence (0.9.6 load balancing): another
// project whose private GitVerse CI passed on this exact HEAD needs no
// second run on H96. Any doubt (no repo, no token, network) is "no".
func PassedOn(root, head string) (string, bool) {
	if _, err := RepoName(root); err != nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	st, err := New().ReadStatus(ctx, root, head)
	if err != nil || st.State != "PASS" {
		return "", false
	}
	return fmt.Sprintf("GitVerse CI run %d passed on %s (%s)", st.RunID, head[:12], st.Repository), true
}
