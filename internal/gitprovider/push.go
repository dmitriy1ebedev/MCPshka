package gitprovider

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const maxHistoryPatch = 64 << 20

var shaRE = regexp.MustCompile("^[a-f0-9]{40}$")

// A private-key header counts only with a key body after it (a real line
// break, a diff prefix, or an escaped \n inside a string): test fixtures
// such as Lane's "-----BEGIN …-----\n" + "A" * 80 blocked every backup (24.11).
var secretRE = regexp.MustCompile(`(?i)(?:gh[pousr]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,}|sk-[A-Za-z0-9_-]{24,}|-----BEGIN (?:[A-Z]+ )*PRIVATE KEY-----(?:\r?\n[+ -]?|\\n)[A-Za-z0-9+/=]{40,})`)
var forbiddenNameRE = regexp.MustCompile("(?i)(?:^|/)(?:id_rsa|id_ed25519|credentials[.]json|secrets[.]json|[.]env[.]local|[.]env[.]production|[^/]+[.]p12|[^/]+[.]pfx)$")

type PushResult struct {
	Provider     Site   `json:"provider"`
	Repo         string `json:"repo"`
	SourceHEAD   string `json:"source_head"`
	RemoteBefore string `json:"remote_before,omitempty"`
	RemoteAfter  string `json:"remote_after"`
	Preflight    string `json:"preflight"`
	State        string `json:"state"`
}

func remoteURL(site Site, owner, name string) (string, error) {
	if _, err := repoPath(site, owner, name); err != nil {
		return "", err
	}
	switch site {
	case GitHub:
		return "https://github.com/" + owner + "/" + name + ".git", nil
	case GitVerse:
		return "https://gitverse.ru/" + owner + "/" + name + ".git", nil
	case MosHub:
		return "https://hub.mos.ru/" + owner + "/" + name + ".git", nil
	}
	return "", ErrUnsupported
}
func checkWorkdir(dir string) error {
	if dir == "" || !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return errors.New("absolute canonical project path required")
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil || resolved != dir {
		return errors.New("project path is a symlink or inaccessible")
	}
	if !strings.HasPrefix(dir, "/root/repos/") {
		return errors.New("git operation outside allowed project roots")
	}
	if len(strings.Split(strings.TrimPrefix(dir, "/root/repos/"), "/")) > 2 {
		return errors.New("nested project path not supported")
	}
	return nil
}

type boundedOutput struct {
	buf      bytes.Buffer
	max      int
	exceeded bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.max {
		b.exceeded = true
		return len(p), errors.New("output exceeds limit")
	}
	return b.buf.Write(p)
}
func runGit(ctx context.Context, dir string, auth string, args ...string) (string, error) {
	return runGitWithAuth(ctx, dir, "http.extraHeader", auth, args...)
}

func runGitWithAuth(ctx context.Context, dir, authKey, auth string, args ...string) (string, error) {
	child, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(child, "/usr/bin/git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "credential.helper="}, args...)...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C", "HOME=/nonexistent", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	if auth != "" {
		cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0="+authKey, "GIT_CONFIG_VALUE_0="+auth)
	}
	var out boundedOutput
	out.max = 2 << 20
	cmd.Stdout = &out
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return "", errors.New("bounded git operation failed")
	}
	if out.exceeded {
		return "", errors.New("git output exceeds limit")
	}
	return strings.TrimSpace(out.buf.String()), nil
}

type secretWindow struct {
	last    []byte
	scanned int64
	match   bool
	exceed  bool
}

func (s *secretWindow) Write(p []byte) (int, error) {
	n := len(p)
	s.scanned += int64(n)
	if s.scanned > maxHistoryPatch {
		s.exceed = true
		return n, errors.New("history exceeds scanning budget")
	}
	sample := append(append([]byte{}, s.last...), p...)
	if secretRE.Match(sample) {
		s.match = true
		return n, errors.New("secret pattern in Git history")
	}
	if len(sample) > 256 {
		s.last = append([]byte{}, sample[len(sample)-256:]...)
	} else {
		s.last = sample
	}
	return n, nil
}
func preflightGitHistory(ctx context.Context, dir string) error {
	// Scan the whole history reachable from HEAD, including deleted patches;
	// detecting a probable secret returns a *count-free* HOLD with no bytes.
	child, cancel := context.WithTimeout(ctx, 75*time.Second)
	defer cancel()
	cmd := exec.CommandContext(child, "/usr/bin/git", "-c", "core.hooksPath=/dev/null", "log", "--format=", "--no-color", "--no-ext-diff", "-p", "HEAD")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1"}
	var scan secretWindow
	cmd.Stdout = &scan
	cmd.Stderr = nil
	err := cmd.Run()
	if scan.match {
		return errors.New("SECRET_PATTERN_DETECTED: full history upload blocked")
	}
	if scan.exceed {
		return errors.New("HISTORY_SCAN_LIMIT: full history upload blocked")
	}
	if err != nil {
		return errors.New("HISTORY_SCAN_UNKNOWN: full history upload blocked")
	}
	names, err := runGit(ctx, dir, "", "ls-tree", "-r", "--name-only", "HEAD")
	if err != nil {
		return err
	}
	for _, name := range strings.Split(names, "\n") {
		if forbiddenNameRE.MatchString(name) {
			return errors.New("SENSITIVE_FILE_NAME: full history upload blocked")
		}
	}
	return nil
}
func (c *Client) Push(ctx context.Context, dir, owner, name string) (PushResult, error) {
	u, err := remoteURL(c.site, owner, name)
	if err != nil {
		return PushResult{}, err
	}
	result := PushResult{Provider: c.site, Repo: owner + "/" + name, Preflight: "UNVERIFIED"}
	if err := checkWorkdir(dir); err != nil {
		return result, err
	}
	gotURL, err := runGit(ctx, dir, "", "remote", "get-url", string(c.site))
	if err != nil {
		return result, err
	}
	if gotURL != u {
		return result, errors.New("remote does not match exact approved HTTPS provider repository")
	}
	dirty, err := runGit(ctx, dir, "", "status", "--porcelain=v1")
	if err != nil {
		return result, err
	}
	if dirty != "" {
		return result, errors.New("working tree not clean; push held")
	}
	head, err := runGit(ctx, dir, "", "rev-parse", "HEAD")
	if err != nil || !shaRE.MatchString(head) {
		return result, errors.New("local HEAD unavailable")
	}
	result.SourceHEAD = head
	if err := preflightGitHistory(ctx, dir); err != nil {
		return result, err
	}
	result.Preflight = "PASS_HEAD_HISTORY_PATCH_SCAN"
	token, err := c.token(c.site)
	if err != nil {
		return result, err
	}
	auth := ""
	if c.site == GitVerse {
		auth = "Authorization: Bearer " + token
	} else {
		username := "x-access-token"
		if c.site == MosHub {
			username = "oauth2"
		}
		auth = "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+token))
	}
	// Never log or return git's credentials/header/command environment.
	before, err := runGit(ctx, dir, auth, "ls-remote", string(c.site), "refs/heads/main")
	if err != nil {
		return result, errors.New("remote auth, TLS or branch probe failed")
	}
	if before != "" {
		parts := strings.Fields(before)
		if len(parts) != 2 || !shaRE.MatchString(parts[0]) {
			return result, errors.New("remote branch identity invalid")
		}
		result.RemoteBefore = parts[0]
	}
	if result.RemoteBefore == head {
		result.RemoteAfter = head
		result.State = "ALREADY_SYNCED"
		return result, nil
	}
	if result.RemoteBefore != "" {
		if _, err := runGit(ctx, dir, "", "merge-base", "--is-ancestor", result.RemoteBefore, head); err != nil {
			return result, errors.New("remote history is not a verified fast-forward; HOLD")
		}
	}
	_, pushErr := runGit(ctx, dir, auth, "push", "--no-verify", string(c.site), head+":refs/heads/main")
	verified, verifyErr := runGit(ctx, dir, auth, "ls-remote", string(c.site), "refs/heads/main")
	if verifyErr != nil {
		return result, fmt.Errorf("%w: remote postcondition unavailable", ErrUnknownEffect)
	}
	parts := strings.Fields(verified)
	if len(parts) != 2 || parts[0] != head {
		if pushErr != nil {
			return result, errors.New("push failed and remote commit differs; no automatic replay")
		}
		return result, fmt.Errorf("%w: remote SHA differs", ErrUnknownEffect)
	}
	result.RemoteAfter = head
	if pushErr != nil {
		result.State = "RECONCILED"
	} else {
		result.State = "PUSHED"
	}
	return result, nil
}
