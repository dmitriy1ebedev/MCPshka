package projectci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
)

// PublishFile is the commit's own choice of what a CI site receives.
//
// Owner 14.11.2026: local repositories are always full by every canon; a
// site gets either the full repository or «только код» — practically an
// empty repository: one snapshot without history, only what the build and
// the tests need, no README, changelogs, docs or plans.
const PublishFile = ".mcpshka/publish.json"

// PublishConfig is .mcpshka/publish.json: {"mode":"code_only","keep":[...],
// "exclude":[...]}. Patterns: "dir/" is a directory prefix, a pattern
// without "/" matches the base name anywhere, anything else the whole path
// (path.Match).
type PublishConfig struct {
	Mode    string   `json:"mode"` // full (default) or code_only
	Keep    []string `json:"keep,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

// CodeOnlyExclude is what «только код» drops before the project's own list.
var CodeOnlyExclude = []string{"*.md", "*.markdown", "*.rst", "*.adoc", "LICENSE*", "COPYING*", "NOTICE*", "AUTHORS*", "docs/", "doc/", "plans/", "roadmap/", "BUGLOG*", "CHANGELOG*", "ROADMAP*"}

func matches(p, file string) bool {
	if strings.HasSuffix(p, "/") {
		return strings.HasPrefix(file, p)
	}
	if !strings.Contains(p, "/") {
		ok, _ := path.Match(p, path.Base(file))
		return ok
	}
	ok, _ := path.Match(p, file)
	return ok
}

// Dropped reports whether a tracked file stays off a code-only site. CI
// workflows always stay: without them the site runs nothing.
func (cfg PublishConfig) Dropped(file string) bool {
	if strings.HasPrefix(file, ".gitverse/") {
		return false
	}
	for _, p := range cfg.Keep {
		if matches(p, file) {
			return false
		}
	}
	for _, p := range append(append([]string{}, CodeOnlyExclude...), cfg.Exclude...) {
		if matches(p, file) {
			return true
		}
	}
	return false
}

func (c *Client) publishConfig(ctx context.Context, root, commit string) (PublishConfig, error) {
	cfg := PublishConfig{Mode: "full"}
	if c.Git == nil { // status-only client: nothing to read the commit with
		return cfg, nil
	}
	out, err := c.Git(ctx, root, nil, "cat-file", "-e", commit+":"+PublishFile)
	if err != nil {
		return cfg, nil
	}
	if out, err = c.Git(ctx, root, nil, "show", commit+":"+PublishFile); err != nil {
		return cfg, fmt.Errorf("read %s: %s", PublishFile, strings.TrimSpace(string(out)))
	}
	if len(out) > 64<<10 || json.Unmarshal(out, &cfg) != nil || (cfg.Mode != "full" && cfg.Mode != "code_only") {
		return cfg, fmt.Errorf("%s: want {\"mode\":\"full\"|\"code_only\",\"keep\":[...],\"exclude\":[...]}", PublishFile)
	}
	return cfg, nil
}

// siteCommit is what the site's main carries for commit: the commit itself
// (full) or its code-only snapshot — a parentless commit of the filtered
// tree, deterministic (author, date and message fixed by the source), so
// status and rerun find the same SHA again without any stored mapping.
func (c *Client) siteCommit(ctx context.Context, root, commit string) (string, PublishConfig, error) {
	cfg, err := c.publishConfig(ctx, root, commit)
	if err != nil || cfg.Mode != "code_only" {
		return commit, cfg, err
	}
	idx, err := os.CreateTemp("", "mcpshka-ci-index-*")
	if err != nil {
		return "", cfg, err
	}
	idx.Close()
	defer os.Remove(idx.Name())
	env := []string{"GIT_INDEX_FILE=" + idx.Name()}
	git := func(env []string, args ...string) (string, error) {
		out, err := c.Git(ctx, root, env, args...)
		if err != nil {
			return "", fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(string(out)))
		}
		return strings.TrimSpace(string(out)), nil
	}
	if _, err = git(env, "read-tree", commit); err != nil {
		return "", cfg, err
	}
	files, err := git(env, "ls-files")
	if err != nil {
		return "", cfg, err
	}
	var drop []string
	for _, f := range strings.Split(files, "\n") {
		if f != "" && cfg.Dropped(f) {
			drop = append(drop, f)
		}
	}
	for len(drop) > 0 {
		n := min(len(drop), 200)
		if _, err = git(env, append([]string{"update-index", "--force-remove", "--"}, drop[:n]...)...); err != nil {
			return "", cfg, err
		}
		drop = drop[n:]
	}
	tree, err := git(env, "write-tree")
	if err != nil {
		return "", cfg, err
	}
	when, err := git(nil, "show", "-s", "--format=%ct", commit)
	if err != nil {
		return "", cfg, err
	}
	stamp := "@" + when + " +0000"
	id := []string{"GIT_AUTHOR_NAME=mcpshka", "GIT_AUTHOR_EMAIL=mcpshka@localhost", "GIT_AUTHOR_DATE=" + stamp, "GIT_COMMITTER_NAME=mcpshka", "GIT_COMMITTER_EMAIL=mcpshka@localhost", "GIT_COMMITTER_DATE=" + stamp}
	snap, err := git(id, "commit-tree", tree, "-m", "ci snapshot "+commit)
	if err != nil {
		return "", cfg, err
	}
	if !shaRE.MatchString(snap) {
		return "", cfg, errors.New("git commit-tree returned no SHA")
	}
	return snap, cfg, nil
}
