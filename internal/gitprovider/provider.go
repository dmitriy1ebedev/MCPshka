// Package gitprovider exposes a bounded, typed repository control plane.
// Tokens never enter MCP arguments, audit fields, subprocess argv or logs.
package gitprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

type Site string

const (
	GitHub      Site = "github"
	GitVerse    Site = "gitverse"
	MosHub      Site = "moshub"
	SourceCraft Site = "sourcecraft"
)

var segmentRE = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$")
var ErrUnsupported = errors.New("provider operation unsupported")
var ErrUnknownEffect = errors.New("provider mutation outcome UNKNOWN: reconcile before retry")

type HTTPError struct {
	Status int
	Action string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("%s: provider returned HTTP %d", e.Action, e.Status)
}
func (e *HTTPError) Is(target error) bool {
	t, ok := target.(*HTTPError)
	return ok && t.Status == e.Status
}

type Repository struct {
	ID            any               `json:"id,omitempty"`
	Name          string            `json:"name"`
	FullName      string            `json:"full_name"`
	HTMLURL       string            `json:"html_url"`
	CloneURL      string            `json:"clone_url"`
	Private       bool              `json:"private"`
	Visibility    string            `json:"visibility"`
	DefaultBranch string            `json:"default_branch,omitempty"`
	Archived      bool              `json:"archived,omitempty"`
	Permissions   map[string]bool   `json:"permissions,omitempty"`
	Fork          bool              `json:"fork,omitempty"`
	Parent        *RepositoryParent `json:"parent,omitempty"`
}
type RepositoryParent struct {
	FullName string `json:"full_name"`
}
type Result struct {
	Provider   Site       `json:"provider"`
	State      string     `json:"state"`
	Created    bool       `json:"created,omitempty"`
	Reconciled bool       `json:"reconciled,omitempty"`
	Repository Repository `json:"repository"`
}
type Capability struct {
	Provider          Site     `json:"provider"`
	Supported         []string `json:"supported"`
	CredentialPresent bool     `json:"credential_present"`
	CredentialReason  string   `json:"credential_reason"`
}

type Client struct {
	ciProject string
	site      Site
	base      string
	http      *http.Client
	token     func(Site) (string, error)
}

func New(site Site) (*Client, error) {
	var base string
	switch site {
	case GitHub:
		base = "https://api.github.com"
	case GitVerse:
		base = "https://api.gitverse.ru"
	case MosHub:
		base = "https://hub.mos.ru/api/v4"
	case SourceCraft:
		base = "https://api.sourcecraft.tech"
	default:
		return nil, fmt.Errorf("unknown git provider: %q", site)
	}
	return &Client{site: site, base: base, http: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}, token: tokenFromVault}, nil
}
func (c *Client) Site() Site { return c.site }
func tokenFromVault(site Site) (string, error) {
	var filename string
	switch site {
	case GitHub:
		filename = "/var/lib/mcpshka/github/api_token"
	case GitVerse:
		filename = "/var/lib/mcpshka/gitverse/api_token"
	case MosHub:
		filename = "/var/lib/mcpshka/moshub/api_token"
	case SourceCraft:
		filename = "/var/lib/mcpshka/sourcecraft/token"
	default:
		return "", errors.New("unsupported credential provider")
	}
	// The service-controlled directory is not caller-supplied.
	fi, err := os.Lstat(filename)
	if err != nil {
		return "", errors.New("provider credentials unavailable")
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0077 != 0 || fi.Size() > 4096 {
		return "", errors.New("provider credentials have unsafe permissions or size")
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Geteuid() {
		return "", errors.New("provider credential owner mismatch")
	}
	f, err := os.Open(filepath.Clean(filename))
	if err != nil {
		return "", errors.New("provider credentials unreadable")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(raw) > 4096 {
		return "", errors.New("provider credential read failure")
	}
	token := strings.TrimSpace(string(raw))
	if token == "" || len(token) > 4096 || strings.ContainsAny(token, " \r\n\t") {
		return "", errors.New("provider credentials invalid")
	}
	return token, nil
}
func (c *Client) Capability() Capability {
	k := Capability{Provider: c.site, Supported: []string{"whoami", "get", "list", "create", "update", "branches", "ci_runs"}}
	if c.site != MosHub {
		k.Supported = append(k.Supported, "collaborator")
	}
	if c.site == GitHub {
		k.Supported = append(k.Supported, "fork")
	}
	if _, err := c.token(c.site); err == nil {
		k.CredentialPresent = true
		k.CredentialReason = "credential file passed owner/mode/read checks; API scopes unverified"
	} else {
		k.CredentialReason = "provider credentials unavailable or invalid; API not verified"
	}
	return k
}
func validSegment(s string) bool {
	return segmentRE.MatchString(s) && s != "." && s != ".." && !strings.HasSuffix(s, ".git")
}
func repoPath(site Site, owner, name string) (string, error) {
	if !validSegment(owner) || !validSegment(name) {
		return "", errors.New("invalid owner or repository name")
	}
	if site == MosHub {
		return "/projects/" + url.PathEscape(owner+"/"+name), nil
	}
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name), nil
}
func (c *Client) request(ctx context.Context, method, route string, payload any, target any) error {
	if !strings.HasPrefix(route, "/") || strings.HasPrefix(route, "//") || strings.Contains(route, "#") {
		return errors.New("invalid provider route")
	}
	raw := []byte(nil)
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if len(b) > 32768 {
			return errors.New("provider payload too large")
		}
		raw = b
	}
	token, err := c.token(c.site)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+route, bytes.NewReader(raw))
	if err != nil {
		return errors.New("invalid provider request")
	}
	switch c.site {
	case GitHub:
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	case GitVerse:
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.gitverse.object+json;version=latest")
	case MosHub:
		req.Header.Set("PRIVATE-TOKEN", token)
		req.Header.Set("Accept", "application/json")
	}
	req.Header.Set("User-Agent", "MCPshka-GitProvider/1")
	if raw != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		if method != http.MethodGet {
			return fmt.Errorf("%w (%s)", ErrUnknownEffect, method)
		}
		return errors.New("provider request failed")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return &HTTPError{Status: res.StatusCode, Action: method}
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20+1))
	if err != nil || len(data) > 1<<20 {
		return errors.New("provider response exceeds budget or read failed")
	}
	if target != nil && len(data) > 0 {
		if err := json.Unmarshal(data, target); err != nil {
			return errors.New("provider response schema invalid")
		}
	}
	return nil
}
func (c *Client) WhoAmI(ctx context.Context) (string, error) {
	route := "/user"
	if c.site == MosHub {
		route = "/user"
	}
	var user struct {
		Login    string `json:"login"`
		Username string `json:"username"`
	}
	if err := c.request(ctx, http.MethodGet, route, nil, &user); err != nil {
		return "", err
	}
	if c.site == MosHub {
		return user.Username, nil
	}
	return user.Login, nil
}
func (c *Client) normalize(r *Repository) {
	if c.site == MosHub {
		r.Private = r.Visibility == "private"
		if r.CloneURL == "" {
			r.CloneURL = r.HTMLURL + ".git"
		}
	} else {
		if r.Visibility == "" {
			if r.Private {
				r.Visibility = "private"
			} else {
				r.Visibility = "public"
			}
		}
	}
}
func (c *Client) Get(ctx context.Context, owner, name string) (Repository, error) {
	route, err := repoPath(c.site, owner, name)
	if err != nil {
		return Repository{}, err
	}
	var r Repository
	if err := c.request(ctx, http.MethodGet, route, nil, &r); err != nil {
		return Repository{}, err
	}
	if c.site == MosHub && r.FullName == "" {
		var gitlab struct {
			PathWithNamespace string `json:"path_with_namespace"`
		}
		if err := c.request(ctx, http.MethodGet, route, nil, &gitlab); err != nil {
			return Repository{}, err
		}
		r.FullName = gitlab.PathWithNamespace
	}
	c.normalize(&r)
	if r.Name == "" || r.FullName == "" {
		return Repository{}, errors.New("provider repo schema incomplete")
	}
	return r, nil
}
func (c *Client) List(ctx context.Context) ([]Repository, error) {
	route := "/user/repos?per_page=100"
	if c.site == MosHub {
		route = "/projects?membership=true&per_page=100"
	}
	var out []Repository
	if err := c.request(ctx, http.MethodGet, route, nil, &out); err != nil {
		return nil, err
	}
	if len(out) > 100 {
		return nil, errors.New("provider list exceeds limit")
	}
	for i := range out {
		c.normalize(&out[i])
	}
	return out, nil
}
func (c *Client) Create(ctx context.Context, owner, name, description string, private bool) (Result, error) {
	if !validSegment(owner) || !validSegment(name) || len(description) > 2048 {
		return Result{}, errors.New("invalid repository specification")
	}
	if current, err := c.Get(ctx, owner, name); err == nil {
		if current.Private != private {
			return Result{}, errors.New("repository exists with different visibility")
		}
		return Result{Provider: c.site, State: "EXISTS", Repository: current}, nil
	} else {
		var h *HTTPError
		if !errors.As(err, &h) || h.Status != 404 {
			return Result{}, err
		}
	}
	route := "/user/repos"
	payload := map[string]any{"name": name, "description": description, "private": private, "auto_init": false}
	if c.site == MosHub {
		route = "/projects"
		payload = map[string]any{"name": name, "path": name, "description": description, "visibility": map[bool]string{true: "private", false: "public"}[private], "initialize_with_readme": false}
	}
	if c.site == GitHub || c.site == GitVerse {
		login, err := c.WhoAmI(ctx)
		if err != nil {
			return Result{}, err
		}
		if login != owner {
			route = "/orgs/" + url.PathEscape(owner) + "/repos"
		}
	}
	if c.site == MosHub {
		login, err := c.WhoAmI(ctx)
		if err != nil {
			return Result{}, err
		}
		if login != owner {
			var group struct {
				ID int `json:"id"`
			}
			if err := c.request(ctx, http.MethodGet, "/groups/"+url.PathEscape(owner), nil, &group); err != nil {
				return Result{}, err
			}
			if group.ID <= 0 {
				return Result{}, errors.New("provider namespace missing")
			}
			payload["namespace_id"] = group.ID
		}
	}
	var repo Repository
	err := c.request(ctx, http.MethodPost, route, payload, &repo)
	if err != nil {
		// Never replay an ambiguous POST. Observe the exact resource instead.
		if found, probeErr := c.Get(ctx, owner, name); probeErr == nil && found.Private == private {
			return Result{Provider: c.site, State: "RECONCILED", Reconciled: true, Repository: found}, nil
		}
		var h *HTTPError
		if errors.As(err, &h) && h.Status != 409 && h.Status != 422 {
			return Result{}, err
		}
		return Result{}, fmt.Errorf("%w: creation could not be reconciled", ErrUnknownEffect)
	}
	c.normalize(&repo)
	if repo.Name != name || repo.Private != private {
		return Result{}, fmt.Errorf("%w: create reply mismatch", ErrUnknownEffect)
	}
	verified, err := c.Get(ctx, owner, name)
	if err != nil || verified.Private != private {
		return Result{}, fmt.Errorf("%w: created resource not independently verified", ErrUnknownEffect)
	}
	return Result{Provider: c.site, State: "CREATED", Created: true, Repository: verified}, nil
}

// Fork creates a genuine public GitHub fork, preserving GitHub parent metadata.
// It never uploads an existing local history and never follows auth redirects.
// The POST is made at most once; an uncertain result is read back, not retried.
func (c *Client) Fork(ctx context.Context, parentOwner, parentName, owner, name string) (Result, error) {
	if c.site != GitHub {
		return Result{}, ErrUnsupported
	}
	if !validSegment(parentOwner) || !validSegment(parentName) || !validSegment(owner) || !validSegment(name) {
		return Result{}, errors.New("invalid fork repository identity")
	}
	login, err := c.WhoAmI(ctx)
	if err != nil {
		return Result{}, err
	}
	if !strings.EqualFold(login, owner) {
		return Result{}, errors.New("fork destination must be authenticated GitHub owner")
	}
	upstream, err := c.Get(ctx, parentOwner, parentName)
	if err != nil {
		return Result{}, err
	}
	if upstream.Private || !strings.EqualFold(upstream.FullName, parentOwner+"/"+parentName) {
		return Result{}, errors.New("only a verified public upstream can be forked")
	}
	matches := func(r Repository) bool {
		return strings.EqualFold(r.FullName, owner+"/"+name) &&
			r.Fork && !r.Private && r.Parent != nil &&
			strings.EqualFold(r.Parent.FullName, parentOwner+"/"+parentName)
	}
	current, err := c.Get(ctx, owner, name)
	if err == nil {
		if matches(current) {
			return Result{Provider: GitHub, State: "ALREADY_FORK", Repository: current}, nil
		}
		return Result{}, errors.New("destination exists and is not a verified fork of upstream")
	}
	var h *HTTPError
	// HTTP 301 may be a stale GitHub redirect from an owner-renamed repo.
	// Do not follow it with credentials; the exact fork POST will decide if
	// the path is available, followed by independent GET of the new path.
	if !errors.As(err, &h) || (h.Status != http.StatusNotFound && h.Status != http.StatusMovedPermanently) {
		return Result{}, err
	}
	var submitted Repository
	route := "/repos/" + url.PathEscape(parentOwner) + "/" + url.PathEscape(parentName) + "/forks"
	postErr := c.request(ctx, http.MethodPost, route, map[string]any{
		"name": name, "default_branch_only": false,
	}, &submitted)
	if postErr != nil {
		var rejected *HTTPError
		if errors.As(postErr, &rejected) &&
			rejected.Status != http.StatusConflict && rejected.Status != http.StatusUnprocessableEntity {
			return Result{}, postErr
		}
	}
	// Reconcile a provider timeout, 422 collision or a successful 202 by GET.
	// A missing or mismatched destination must never be treated as success.
	for i := 0; i < 12; i++ {
		found, getErr := c.Get(ctx, owner, name)
		if getErr == nil {
			if !matches(found) {
				return Result{}, errors.New("fork destination is not the expected public parent")
			}
			state := "CREATED"
			if postErr != nil {
				state = "RECONCILED"
			}
			return Result{Provider: GitHub, State: state, Created: postErr == nil,
				Reconciled: postErr != nil, Repository: found}, nil
		}
		var missing *HTTPError
		if !errors.As(getErr, &missing) ||
			(missing.Status != http.StatusNotFound && missing.Status != http.StatusMovedPermanently) {
			return Result{}, fmt.Errorf("%w: fork verification unavailable", ErrUnknownEffect)
		}
		if i < 11 {
			select {
			case <-ctx.Done():
				return Result{}, fmt.Errorf("%w: fork verification cancelled", ErrUnknownEffect)
			case <-time.After(time.Second):
			}
		}
	}
	return Result{}, fmt.Errorf("%w: GitHub fork not yet verifiable; inspect exact repository before any retry", ErrUnknownEffect)
}

func (c *Client) Update(ctx context.Context, owner, name string, changes map[string]any) (Result, error) {
	route, err := repoPath(c.site, owner, name)
	if err != nil {
		return Result{}, err
	}
	if len(changes) == 0 || len(changes) > 3 {
		return Result{}, errors.New("no updates or too many fields")
	}
	patch := map[string]any{}
	for k, v := range changes {
		switch k {
		case "description":
			s, ok := v.(string)
			if !ok || len(s) > 2048 {
				return Result{}, errors.New("invalid description")
			}
			patch[k] = s
		case "name":
			s, ok := v.(string)
			if !ok || !validSegment(s) {
				return Result{}, errors.New("invalid repository name")
			}
			patch[k] = s
		case "private":
			b, ok := v.(bool)
			if !ok {
				return Result{}, errors.New("invalid visibility")
			}
			if c.site == MosHub {
				patch["visibility"] = map[bool]string{true: "private", false: "public"}[b]
			} else {
				patch[k] = b
			}
		case "archived":
			b, ok := v.(bool)
			if !ok || c.site == MosHub {
				return Result{}, ErrUnsupported
			}
			patch[k] = b
		default:
			return Result{}, ErrUnsupported
		}
	}
	var out Repository
	if err := c.request(ctx, http.MethodPatch, route, patch, &out); err != nil {
		return Result{}, err
	}
	finalName := name
	if s, ok := changes["name"].(string); ok {
		finalName = s
	}
	observed, err := c.Get(ctx, owner, finalName)
	if err != nil {
		return Result{}, fmt.Errorf("%w: update verification failed", ErrUnknownEffect)
	}
	return Result{Provider: c.site, State: "UPDATED", Repository: observed}, nil
}
func (c *Client) Branches(ctx context.Context, owner, name string) ([]map[string]any, error) {
	route, err := repoPath(c.site, owner, name)
	if err != nil {
		return nil, err
	}
	if c.site == MosHub {
		route += "/repository/branches?per_page=100"
	} else {
		route += "/branches?per_page=100"
	}
	var out []map[string]any
	if err := c.request(ctx, http.MethodGet, route, nil, &out); err != nil {
		return nil, err
	}
	if len(out) > 100 {
		return nil, errors.New("branch list too large")
	}
	return out, nil
}
func (c *Client) CIRuns(ctx context.Context, owner, name string) ([]map[string]any, error) {
	route, err := repoPath(c.site, owner, name)
	if err != nil {
		return nil, err
	}
	if c.site == MosHub {
		route += "/pipelines?per_page=30"
		var out []map[string]any
		if err := c.request(ctx, http.MethodGet, route, nil, &out); err != nil {
			return nil, err
		}
		return out, nil
	}
	route += "/actions/runs?per_page=30"
	var response struct {
		WorkflowRuns []map[string]any `json:"workflow_runs"`
		Runs         []map[string]any `json:"runs"`
	}
	if err := c.request(ctx, http.MethodGet, route, nil, &response); err != nil {
		return nil, err
	}
	if len(response.WorkflowRuns) > 0 {
		return response.WorkflowRuns, nil
	}
	return response.Runs, nil
}
func (c *Client) Collaborator(ctx context.Context, owner, name, account, permission string) error {
	if c.site == MosHub {
		return ErrUnsupported
	}
	if !validSegment(account) || !strings.Contains("|pull|push|admin|maintain|triage|", "|"+permission+"|") {
		return errors.New("invalid collaborator or permission")
	}
	route, err := repoPath(c.site, owner, name)
	if err != nil {
		return err
	}
	return c.request(ctx, http.MethodPut, route+"/collaborators/"+url.PathEscape(account), map[string]string{"permission": permission}, nil)
}
