package gitprovider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mcpshka/internal/distributedci"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const ciWorkflow = "distributed.yml"
const ciJournal = "/var/lib/mcpshka/distributed-ci"

type CIProvider struct {
	quotaRemaining int
	Client         *Client
	Repo           string
	cache          map[int]distributedci.Observation
	loaded         bool
	loadErr        error
}

func DistributedProviders(repo string) []distributedci.Provider {
	out := []distributedci.Provider{}
	name := filepath.Base(filepath.Clean(repo))
	// Callers supply the canonical profile root, never a worktree basename.
	if !validSegment(name) {
		return out
	}
	for _, site := range []Site{GitHub, GitVerse, MosHub, SourceCraft} {
		c, e := New(site)
		if e == nil {
			c.ciProject = name
			out = append(out, &CIProvider{Client: c, Repo: repo})
		}
	}
	return out
}
func (p *CIProvider) Name() string { return string(p.Client.site) }
func (c *Client) ciRoute() string {
	if c.ciProject != "" && c.ciProject != "mcpshka" {
		if c.site == MosHub {
			return "/projects/" + url.PathEscape("lda6994/"+c.ciProject)
		}
		owner := "dmitriy1ebedev"
		if c.site == GitVerse {
			owner = "dmitriy_1ebedev"
		}
		if c.site == SourceCraft {
			owner = "lda6994"
		}
		return "/repos/" + owner + "/" + c.ciProject
	}
	if c.site == SourceCraft {
		return "/repos/lda6994/mcpshka-ci"
	}
	if c.site == MosHub {
		return "/projects/91385"
	}
	owner := "dmitriy1ebedev"
	if c.site == GitVerse {
		owner = "dmitriy_1ebedev"
	}
	return "/repos/" + owner + "/" + snapshotRepo(c.site)
}

// GitHubCIRepository names MCPshka's private CI repository on every site
// (0.19.1): the public "MCPshka" carries only the open-core export, never a
// snapshot of the canonical tree with the owner's journals.
const GitHubCIRepository = "mcpshka-ci"

// snapshotRepo is the private CI repository on every site (0.19.1): the
// public "MCPshka" carries only the open-core export.
func snapshotRepo(Site) string { return GitHubCIRepository }

// CI log downloads may redirect to a signed object URL. Credentials are sent
// only to the fixed API origin, never to that download or to arbitrary hosts.
func (c *Client) ciRequest(ctx context.Context, method, path string, payload any) ([]byte, error) {
	return c.ciDo(ctx, method, path, payload, false)
}

// ciFirstPage reads one page of a list for estimates (quota), where the
// unseen remainder is counted pessimistically; evidence reads never use it.
func (c *Client) ciFirstPage(ctx context.Context, path string, out any) error {
	b, e := c.ciDo(ctx, "GET", path, nil, true)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, out)
}
func (c *Client) ciDo(ctx context.Context, method, path string, payload any, firstPage bool) ([]byte, error) {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return nil, ErrUnsupported
	}
	token, e := c.token(c.site)
	if e != nil {
		return nil, e
	}
	var body []byte
	if payload != nil {
		body, e = json.Marshal(payload)
		if e != nil {
			return nil, e
		}
	}
	req, e := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if e != nil {
		return nil, e
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if c.site == MosHub {
		req.Header.Set("PRIVATE-TOKEN", token)
	} else {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if c.site == GitVerse {
		req.Header.Set("Accept", "application/vnd.gitverse.object+json;version=latest")
	}
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	res, e := c.http.Do(req)
	if e != nil {
		return nil, errors.New("CI API unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode == 302 && method == "GET" && c.site == GitHub && strings.HasSuffix(path, "/logs") {
		u, e := url.Parse(res.Header.Get("Location"))
		if e != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || !(strings.HasSuffix(u.Hostname(), ".blob.core.windows.net") || strings.HasSuffix(u.Hostname(), ".actions.githubusercontent.com")) {
			return nil, errors.New("CI download origin rejected")
		}
		r, e := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
		if e != nil {
			return nil, e
		}
		download, e := c.http.Do(r)
		if e != nil {
			return nil, errors.New("CI download unavailable")
		}
		defer download.Body.Close()
		res = download
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, &HTTPError{Status: res.StatusCode, Action: "CI " + method}
	}
	if !firstPage && (res.Header.Get("X-Next-Page") != "" || strings.Contains(res.Header.Get("Link"), "rel=\"next\"")) {
		return nil, errors.New("CI pagination exceeds budget")
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if e != nil || len(b) > 4<<20 {
		return nil, errors.New("CI response exceeds budget")
	}
	return b, nil
}
func (c *Client) ciJSON(ctx context.Context, method, path string, payload, out any) error {
	b, e := c.ciRequest(ctx, method, path, payload)
	if e != nil {
		return e
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}
func (p *CIProvider) Probe(ctx context.Context) error {
	c := p.Client
	if c.site == SourceCraft {
		_, e := c.ciRequest(ctx, "GET", c.ciRoute()+"/cicd/runs?page_size=1", nil)
		return e
	}
	if _, e := c.WhoAmI(ctx); e != nil {
		return e
	}
	var repo map[string]any
	if e := c.ciJSON(ctx, "GET", c.ciRoute(), nil, &repo); e != nil {
		return e
	}
	if c.site == GitHub {
		var permissions struct {
			Enabled bool `json:"enabled"`
		}
		if e := c.ciJSON(ctx, "GET", c.ciRoute()+"/actions/permissions", nil, &permissions); e != nil || !permissions.Enabled {
			return errors.New("GitHub Actions disabled or unauthorized")
		}
		remaining, e := c.freeCIMinutes(ctx)
		if e != nil || remaining < 60 {
			return errors.New("GitHub free quota unavailable or exhausted")
		}
		p.quotaRemaining = remaining
	}
	if c.site == GitVerse {
		var workflows map[string]any
		if err := c.ciJSON(ctx, "GET", c.ciRoute()+"/actions/workflows", nil, &workflows); err != nil {
			return err
		}
		return c.gitverseRunnerHealth(ctx)
	}
	if c.site == MosHub {
		var runners []struct {
			ID     int64    `json:"id"`
			Status string   `json:"status"`
			Paused bool     `json:"paused"`
			Tags   []string `json:"tag_list"`
		}
		if e := c.ciJSON(ctx, "GET", c.ciRoute()+"/runners?per_page=100", nil, &runners); e != nil {
			return e
		}
		details := 0
		for _, r := range runners {
			if r.Status == "online" && !r.Paused {
				tags := r.Tags
				if len(tags) == 0 && r.ID > 0 && details < 3 {
					details++
					detail := r
					if c.ciJSON(ctx, "GET", fmt.Sprintf("/runners/%d", r.ID), nil, &detail) == nil && detail.ID == r.ID && detail.Status == "online" && !detail.Paused {
						tags = detail.Tags
					}
				}
				for _, tag := range tags {
					if tag == "appsechub" {
						return nil
					}
				}
			}
		}
		return errors.New("CI_NO_RUNNER: no available MosHub runner with appsechub tag")
	}
	return nil
}
func (p *CIProvider) publish(ctx context.Context, plan distributedci.Plan) error {
	return p.publishRef(ctx, plan, distributedci.Ref(plan))
}
func (p *CIProvider) publishRef(ctx context.Context, plan distributedci.Plan, branch string) error {
	c := p.Client
	owner := "dmitriy1ebedev"
	if c.site == GitVerse {
		owner = "dmitriy_1ebedev"
	}
	if c.site == MosHub {
		owner = "lda6994"
	}
	name := c.ciProject
	if name == "" || name == "mcpshka" {
		name = snapshotRepo(c.site)
	}
	remote, e := remoteURL(c.site, owner, name)
	if c.site == SourceCraft {
		remote, e = "https://git.sourcecraft.dev/lda6994/"+name+".git", nil
	}
	if e != nil {
		return e
	}
	if c.ciProject != "" && c.ciProject != "mcpshka" || c.site == GitHub {
		// A snapshot is the private tree: it goes only to a private repository.
		var visibility struct {
			Private    bool   `json:"private"`
			Visibility string `json:"visibility"`
		}
		if c.ciJSON(ctx, "GET", c.ciRoute(), nil, &visibility) != nil || !visibility.Private && visibility.Visibility != "private" {
			return errors.New("CI snapshots go only to an existing private repository")
		}
	}
	token, e := c.token(c.site)
	if e != nil {
		return e
	}
	auth := "Authorization: Bearer " + token
	if c.site != GitVerse && c.site != SourceCraft {
		user := "x-access-token"
		if c.site == MosHub {
			user = "oauth2"
		}
		auth = "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+token))
	}
	ref := "refs/heads/" + branch
	probe := func() (string, error) {
		raw, e := runGit(ctx, p.Repo, auth, "ls-remote", "--heads", remote, ref)
		if e != nil || raw == "" {
			return "", e
		}
		f := strings.Fields(raw)
		if len(f) != 2 || f[1] != ref || !shaRE.MatchString(f[0]) {
			return "", errors.New("invalid CI ref")
		}
		return f[0], nil
	}
	before, e := probe()
	if e != nil {
		return e
	}
	if before != "" {
		if before != plan.Snapshot {
			return errors.New("immutable CI ref conflict")
		}
		return nil
	}
	// A snapshot has no parent, so it cannot export local history.
	raw, e := runGit(ctx, p.Repo, "", "cat-file", "commit", plan.Commit)
	if e != nil {
		return e
	}
	expected, e := distributedci.SnapshotSHA(plan.Commit, plan.Tree, raw)
	if e != nil || expected != plan.Snapshot {
		return errors.New("snapshot mismatch")
	}
	var author, committer string
	for _, line := range strings.Split(strings.SplitN(raw, "\n\n", 2)[0], "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		stamp := strings.Join(f[len(f)-2:], " ")
		if f[0] == "author" {
			author = stamp
		}
		if f[0] == "committer" {
			committer = stamp
		}
	}
	body := "tree " + plan.Tree + "\nauthor MCPshka Snapshot <snapshot@mcpshka.local> " + author + "\ncommitter MCPshka Snapshot <snapshot@mcpshka.local> " + committer + "\n\nMCPshka immutable snapshot\n\nSource-Commit: " + plan.Commit + "\nSource-Tree: " + plan.Tree + "\n"
	// hash-object is the only local Git write; no refs or working files change.
	cmd := execCIHash(ctx, p.Repo, body)
	if cmd != plan.Snapshot {
		return errors.New("snapshot object write failed")
	}
	_, pushErr := runGit(ctx, p.Repo, auth, "push", "--no-verify", remote, plan.Snapshot+":"+ref)
	after, e := probe()
	if e != nil {
		return ErrUnknownEffect
	}
	if after != plan.Snapshot {
		if pushErr != nil {
			return errors.New("CI push failed")
		}
		return ErrUnknownEffect
	}
	return nil
}
func (p *CIProvider) Start(ctx context.Context, plan distributedci.Plan, n int) error {
	if p.Client.site == SourceCraft {
		if e := p.publishRef(ctx, plan, distributedci.Ref(plan)+"/shard-"+strconv.Itoa(n)); e != nil {
			return e
		}
		dir := p.journalDir(plan.Commit)
		if e := os.MkdirAll(dir, 0700); e != nil {
			return e
		}
		f, e := os.OpenFile(filepath.Join(dir, p.Name()+"-"+strconv.Itoa(n)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if os.IsExist(e) {
			return distributedci.ErrDispatchUnknown
		}
		if e != nil {
			return e
		}
		_, e = f.WriteString(time.Now().UTC().Format(time.RFC3339Nano))
		if e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e != nil {
			return e
		}
		return ce
	}
	if p.Client.site == GitHub {
		if p.quotaRemaining < 50 {
			return errors.New("GitHub free quota reserved by other shards")
		}
		p.quotaRemaining -= 50
	}
	if e := p.publish(ctx, plan); e != nil {
		if strings.Contains(e.Error(), "conflict") || strings.Contains(e.Error(), "mismatch") {
			return distributedci.ErrEvidence
		}
		return e
	}
	// Reserve before dispatch: a lost response cannot submit the shard twice.
	dir := p.journalDir(plan.Commit)
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	path := filepath.Join(dir, p.Name()+"-"+strconv.Itoa(n))
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return distributedci.ErrDispatchUnknown
	}
	_, e = f.WriteString(time.Now().UTC().Format(time.RFC3339Nano))
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	c := p.Client
	inputs := map[string]string{"shard": strconv.Itoa(n), "source_commit": plan.Commit, "source_tree": plan.Tree, "snapshot": plan.Snapshot, "plan_hash": plan.Hash()}
	if c.site == MosHub {
		variables := []map[string]string{{"key": "MCPSHKA_DISTRIBUTED", "value": "1"}}
		for k, v := range inputs {
			variables = append(variables, map[string]string{"key": "CI_SHARD_" + strings.ToUpper(k), "value": v})
		}
		if c.ciJSON(ctx, "POST", c.ciRoute()+"/pipeline", map[string]any{"ref": distributedci.Ref(plan), "variables": variables}, nil) != nil {
			return distributedci.ErrDispatchUnknown
		}
		return nil
	}
	payload := map[string]any{"ref": distributedci.Ref(plan), "inputs": inputs}
	if c.site == GitVerse {
		payload = map[string]any{"branch": distributedci.Ref(plan), "inputs": inputs}
	}
	if c.ciJSON(ctx, "POST", c.ciRoute()+"/actions/workflows/"+ciWorkflow+"/dispatches", payload, nil) != nil {
		return distributedci.ErrDispatchUnknown
	}
	return nil
}

type ciRun struct {
	ID        int64     `json:"id"`
	HeadSHA   string    `json:"head_sha"`
	CommitSHA string    `json:"commit_sha"`
	SHA       string    `json:"sha"`
	Ref       string    `json:"ref"`
	Branch    string    `json:"head_branch"`
	Status    string    `json:"status"`
	Created   time.Time `json:"created_at"`
	Event     string    `json:"event"`
}
type ciJob struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	Failure    string    `json:"failure_reason"`
	Started    time.Time `json:"started_at"`
	Finished   time.Time `json:"completed_at"`
	Commit     struct {
		ID string `json:"id"`
	} `json:"commit"`
	Pipeline struct {
		ID int64 `json:"id"`
	} `json:"pipeline"`
}

func (p *CIProvider) Observe(ctx context.Context, plan distributedci.Plan, n int) (distributedci.Observation, bool, error) {
	if !p.loaded {
		p.loaded = true
		p.cache = map[int]distributedci.Observation{}
		if p.Client.site == SourceCraft {
			p.loadErr = p.loadSourceCraft(ctx, plan)
		} else {
			p.loadErr = p.load(ctx, plan)
		}
	}
	if p.loadErr != nil {
		return distributedci.Observation{}, false, p.loadErr
	}
	o, ok := p.cache[n]
	if ok {
		if o.State == "RUNNING" || o.State == "QUEUED" {
			if b, e := os.ReadFile(filepath.Join(p.journalDir(plan.Commit), p.Name()+"-"+strconv.Itoa(n))); e == nil {
				if stamp, e := time.Parse(time.RFC3339Nano, string(b)); e == nil {
					o.Seconds = min(6*3600, max(0, time.Since(stamp).Seconds()))
					limit := 50 * time.Minute
					if o.State == "QUEUED" {
						limit = 15 * time.Minute
					}
					if time.Since(stamp) > limit {
						o.State = "INFRA_FAIL"
					}
				}
			}
		}
		return o, true, nil
	}
	b, e := os.ReadFile(filepath.Join(p.journalDir(plan.Commit), p.Name()+"-"+strconv.Itoa(n)))
	if e == nil {
		stamp, e := time.Parse(time.RFC3339Nano, string(b))
		if e != nil {
			return o, false, distributedci.ErrEvidence
		}
		o = distributedci.Observation{Provider: p.Name(), Shard: n, Commit: plan.Commit, Tree: plan.Tree, Snapshot: plan.Snapshot, PlanHash: plan.Hash(), Packages: plan.Shards[n], State: "QUEUED", RunID: 1, Seconds: min(6*3600, max(0, time.Since(stamp).Seconds()))}
		if time.Since(stamp) > 15*time.Minute {
			o.State = "INFRA_FAIL"
		}
		return o, true, nil
	}
	if !os.IsNotExist(e) {
		return o, false, e
	}
	return o, false, nil
}
func (p *CIProvider) load(ctx context.Context, plan distributedci.Plan) error {
	c := p.Client
	route := c.ciRoute()
	var runs []ciRun
	if c.site == MosHub {
		if e := c.ciJSON(ctx, "GET", route+"/pipelines?ref="+url.QueryEscape(distributedci.Ref(plan))+"&per_page=100", nil, &runs); e != nil {
			return e
		}
	} else {
		var data struct {
			Runs         []ciRun `json:"runs"`
			WorkflowRuns []ciRun `json:"workflow_runs"`
		}
		if e := c.ciJSON(ctx, "GET", route+"/actions/workflows/"+ciWorkflow+"/runs?per_page=100&branch="+url.QueryEscape(distributedci.Ref(plan)), nil, &data); e != nil {
			return e
		}
		runs = data.WorkflowRuns
		if c.site == GitVerse && len(data.Runs) > 0 {
			runs = data.Runs
		}
	}
	for _, r := range runs {
		sha, ref := r.HeadSHA, r.Branch
		if c.site == GitVerse {
			sha, ref = r.CommitSHA, strings.TrimPrefix(r.Ref, "refs/heads/")
		}
		if c.site == MosHub {
			sha, ref = r.SHA, r.Ref
		}
		if ref != distributedci.Ref(plan) {
			continue
		}
		if sha != plan.Snapshot || r.ID <= 0 {
			return distributedci.ErrEvidence
		}
		var jobs []ciJob
		if c.site == MosHub {
			if e := c.ciJSON(ctx, "GET", fmt.Sprintf("%s/pipelines/%d/jobs?per_page=100&include_retried=false", route, r.ID), nil, &jobs); e != nil {
				return e
			}
		} else {
			var data struct {
				Jobs []ciJob `json:"jobs"`
			}
			if e := c.ciJSON(ctx, "GET", fmt.Sprintf("%s/actions/runs/%d/jobs?per_page=100", route, r.ID), nil, &data); e != nil {
				return e
			}
			jobs = data.Jobs
		}
		for _, j := range jobs {
			n := -1
			for i := range plan.Shards {
				if j.Name == fmt.Sprintf("distributed-shard-%d", i) {
					n = i
				}
			}
			if n < 0 {
				continue
			}
			if j.ID <= 0 {
				return distributedci.ErrEvidence
			}
			if c.site == MosHub && (j.Commit.ID != plan.Snapshot || j.Pipeline.ID != r.ID) {
				return distributedci.ErrEvidence
			}
			o := distributedci.Observation{Provider: p.Name(), Shard: n, Commit: plan.Commit, Tree: plan.Tree, Snapshot: plan.Snapshot, PlanHash: plan.Hash(), Packages: plan.Shards[n], State: "RUNNING", RunID: r.ID}
			terminal := j.Status == "completed" || j.Status == "success" || j.Status == "failure" || j.Status == "failed" || j.Status == "cancelled" || j.Status == "canceled" || j.Status == "skipped"
			if !terminal {
				limit := 50 * time.Minute
				switch j.Status {
				case "queued", "pending", "waiting", "created", "waiting_for_resource", "preparing":
					o.State = "QUEUED"
					limit = 15 * time.Minute
				}
				if !r.Created.IsZero() && time.Since(r.Created) > limit {
					o.State = "INFRA_FAIL"
				}
			} else {
				path := fmt.Sprintf("%s/actions/jobs/%d/logs", route, j.ID)
				if c.site == MosHub {
					path = fmt.Sprintf("%s/jobs/%d/trace", route, j.ID)
				}
				raw, e := c.ciRequest(ctx, "GET", path, nil)
				if e != nil {
					return e
				}
				parsed, e := ParseCIResult(raw)
				if e == nil {
					if parsed.Provider != p.Name() || parsed.RunID != r.ID || parsed.Shard != n || plan.Check(parsed) != nil {
						return distributedci.ErrEvidence
					}
					success := j.Status == "success" || j.Conclusion == "success"
					if parsed.State == "PASS" && !success {
						return distributedci.ErrEvidence
					}
					o = parsed
				} else if j.Status == "cancelled" || j.Status == "canceled" || j.Conclusion == "cancelled" || j.Conclusion == "timed_out" || j.Conclusion == "startup_failure" || j.Conclusion == "action_required" || j.Failure == "runner_system_failure" || j.Failure == "stuck_or_timeout_failure" || CIInfraLog(raw) {
					o.State = "INFRA_FAIL"
				} else if j.Status == "failure" || j.Status == "failed" || j.Conclusion == "failure" {
					// A failed job that never printed its result died before the
					// shard ran (bootstrap, toolchain): move the shard elsewhere.
					// It can never become PASS, so this cannot hide a code failure
					// — the shard is still tested on another site or on H96.
					o.State = "INFRA_FAIL"
				} else {
					// Success without a result line is not trustworthy.
					return distributedci.ErrEvidence
				}
			}
			prev, exists := p.cache[n]
			// Any code failure wins across attempts; otherwise newest run wins.
			if !exists || o.State == "CODE_FAIL" || prev.State != "CODE_FAIL" && o.RunID > prev.RunID {
				p.cache[n] = o
			}
		}
	}
	if c.site == GitHub {
		for _, o := range p.cache {
			if o.State == "RUNNING" || o.State == "QUEUED" {
				p.quotaRemaining -= 50
			}
		}
	}
	return nil
}
func ParseCIResult(raw []byte) (distributedci.Observation, error) {
	var o distributedci.Observation
	found := false
	for _, line := range bytes.Split(raw, []byte("\n")) {
		i := bytes.Index(line, []byte(distributedci.ResultMarker))
		if i < 0 {
			continue
		}
		if found {
			return o, errors.New("duplicate CI result")
		}
		found = true
		d := json.NewDecoder(bytes.NewReader(line[i+len(distributedci.ResultMarker):]))
		d.DisallowUnknownFields()
		if d.Decode(&o) != nil || d.Decode(new(any)) != io.EOF {
			return o, errors.New("malformed CI result")
		}
	}
	if !found {
		return o, errors.New("CI result missing")
	}
	return o, nil
}
func CIInfraLog(raw []byte) bool {
	for _, s := range []string{"dial tcp", "TLS handshake timeout", "no space left on device", "runner system failure", "Failed to pull image", "Cannot connect to the Docker daemon", "Temporary failure resolving", "Connection timed out", "Failed to fetch", "Could not resolve host", "CI infrastructure:", "Job execution failed (system failure)"} {
		if bytes.Contains(raw, []byte(s)) {
			return true
		}
	}
	return false
}

func (p *CIProvider) journalDir(commit string) string {
	if p.Client.ciProject != "" && p.Client.ciProject != "mcpshka" {
		return filepath.Join(ciJournal, p.Client.ciProject, commit)
	}
	return filepath.Join(ciJournal, commit)
}
