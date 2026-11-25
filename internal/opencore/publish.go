package opencore

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// Target is one public repository the open core goes to (25.11, owner:
// GitHub, GitVerse, MosHub and SourceCraft). Auth: "basic:<user>" or
// "bearer" with TokenFile as an http.extraHeader, or "ssh" with Key and a
// pinned KnownHosts file. Credentials never reach argv or output.
type Target struct {
	Site       string `json:"site"`
	URL        string `json:"url"`
	Auth       string `json:"auth"`
	TokenFile  string `json:"token_file,omitempty"`
	Key        string `json:"key,omitempty"`
	KnownHosts string `json:"known_hosts,omitempty"`
}

// GitHubTarget is the default target of a config without "targets".
func GitHubTarget(repo string) Target {
	return Target{Site: "github", URL: "https://github.com/" + repo + ".git", Auth: "basic:x-access-token", TokenFile: "/var/lib/mcpshka/github/api_token"}
}

// Publish commits one export tree on top of the target's public main and
// pushes it without force (S5.GO: replaces packaging/forges/opencore_push.py).
// It never sees the private repository. A tree equal to the public main is
// UNCHANGED. The result is the mcpshka.opencore-publish/v1 JSON.
type Publish struct {
	Tree, Commit, Digest, Version string
	Target                        Target
	GitDir                        string // default /var/lib/mcpshka/opencore/publish-<site>.git
}

var siteRE = regexp.MustCompile(`^[a-z]{3,16}$`)

var (
	sha40   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sha64   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	authorE = []string{"GIT_AUTHOR_NAME=MCPshka", "GIT_AUTHOR_EMAIL=mcpshka@users.noreply.github.com",
		"GIT_COMMITTER_NAME=MCPshka", "GIT_COMMITTER_EMAIL=mcpshka@users.noreply.github.com"}
)

func (p Publish) Run(ctx context.Context) (json.RawMessage, error) {
	out := map[string]any{"schema": "mcpshka.opencore-publish/v1", "trust": "UNTRUSTED"}
	hold := func(code string) (json.RawMessage, error) {
		out["state"], out["code"] = "HOLD", code
		raw, _ := json.Marshal(out)
		return raw, errors.New("open core push did not complete: " + code)
	}
	t := p.Target
	out["site"], out["repository"] = t.Site, t.URL
	if p.GitDir == "" {
		p.GitDir = "/var/lib/mcpshka/opencore/publish-" + t.Site + ".git"
	}
	if st, err := os.Stat(p.Tree); err != nil || !st.IsDir() || !sha40.MatchString(p.Commit) || !sha64.MatchString(p.Digest) || !siteRE.MatchString(t.Site) || strings.HasPrefix(t.URL, "-") {
		return hold("invalid_input")
	}
	url := t.URL
	header, extraEnv := "", []string(nil)
	switch {
	case t.Auth == "bearer" || strings.HasPrefix(t.Auth, "basic:"):
		tok, err := readToken(t.TokenFile)
		if err != nil {
			return hold(err.Error())
		}
		header = "Authorization: Bearer " + tok
		if user, ok := strings.CutPrefix(t.Auth, "basic:"); ok {
			header = "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+tok))
		}
	case t.Auth == "ssh":
		if _, err := readToken(t.KnownHosts); err != nil && err.Error() == "token_missing" {
			return hold("known_hosts_missing")
		}
		extraEnv = []string{"GIT_SSH_COMMAND=ssh -F /dev/null -i " + t.Key + " -o IdentitiesOnly=yes -o BatchMode=yes" +
			" -o UserKnownHostsFile=" + t.KnownHosts + " -o StrictHostKeyChecking=yes -o HostKeyAlgorithms=rsa-sha2-512,rsa-sha2-256,ssh-ed25519 -o ConnectTimeout=8"}
	case t.Auth == "none": // local fixtures only
	default:
		return hold("invalid_auth")
	}
	scratch, err := os.MkdirTemp("", "opencore-index-")
	if err != nil {
		return hold("tempdir")
	}
	defer os.RemoveAll(scratch)
	git := func(env []string, args ...string) (string, error) {
		argv := []string{"--git-dir", p.GitDir, "-c", "credential.helper="}
		if header != "" {
			argv = append(argv, "-c", "http.extraHeader="+header)
		}
		cmd := exec.CommandContext(ctx, "git", append(argv, args...)...)
		cmd.Env = append(append([]string{"PATH=/usr/bin:/bin", "GIT_TERMINAL_PROMPT=0", "HOME=/nonexistent", "LANG=C"}, extraEnv...), env...)
		var b bytes.Buffer
		cmd.Stdout = &b
		if err := cmd.Run(); err != nil {
			return "", errors.New(args[0] + "_failed")
		}
		return strings.TrimSpace(b.String()), nil
	}
	if _, err := os.Stat(p.GitDir); err != nil {
		if err := exec.CommandContext(ctx, "git", "init", "-q", "--bare", "-b", "main", p.GitDir).Run(); err != nil {
			return hold("init_failed")
		}
	}
	remoteMain := func() (string, error) {
		line, err := git(nil, "ls-remote", url, "refs/heads/main")
		if f := strings.Fields(line); err == nil && len(f) > 0 {
			return f[0], nil
		}
		return "", err
	}
	base, err := remoteMain()
	if err != nil {
		return hold(err.Error())
	}
	if base != "" {
		if _, err := git(nil, "fetch", "-q", "--depth=1", url, "+refs/heads/main:refs/remotes/public/main"); err != nil {
			return hold(err.Error())
		}
		if got, _ := git(nil, "rev-parse", "refs/remotes/public/main"); got != base {
			return hold("fetch_mismatch")
		}
	}
	idx := []string{"GIT_INDEX_FILE=" + filepath.Join(scratch, "index"), "GIT_WORK_TREE=" + p.Tree}
	if _, err := git(idx, "add", "-A", "--force", "."); err != nil {
		return hold(err.Error())
	}
	tree, err := git(idx, "write-tree")
	if err != nil {
		return hold(err.Error())
	}
	out["tree"], out["source_commit"] = tree, p.Commit
	if base != "" {
		if bt, _ := git(nil, "rev-parse", base+"^{tree}"); bt == tree {
			out["state"], out["public_commit"] = "UNCHANGED", base
			raw, _ := json.Marshal(out)
			return raw, nil
		}
	}
	title := "MCPshka open core"
	if p.Version != "" {
		title = "MCPshka " + p.Version + " open core"
	}
	args := []string{"commit-tree", tree, "-m", title + "\n\nSource-Commit: " + p.Commit + "\nTree-Digest: " + p.Digest + "\n"}
	if base != "" {
		args = append(args, "-p", base)
	}
	next, err := git(authorE, args...)
	if err != nil {
		return hold(err.Error())
	}
	// Non-forced: a public main that moved meanwhile rejects the push.
	_, _ = git(nil, "push", "-q", url, next+":refs/heads/main")
	if got, _ := remoteMain(); got != next {
		return hold("push_unverified")
	}
	out["state"], out["public_commit"], out["parent"], out["tree_digest"] = "PUBLISHED", next, base, p.Digest
	raw, _ := json.Marshal(out)
	return raw, nil
}

func readToken(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", errors.New("token_missing")
	}
	if s, ok := st.Sys().(*syscall.Stat_t); !ok || int(s.Uid) != os.Geteuid() || st.Mode().Perm()&0o077 != 0 {
		return "", errors.New("token_permissions")
	}
	b, err := os.ReadFile(path)
	v := strings.TrimSpace(string(b))
	if err != nil || v == "" || len(v) > 512 || strings.ContainsAny(v, " \t\r\n") {
		return "", errors.New("token_invalid")
	}
	return v, nil
}
