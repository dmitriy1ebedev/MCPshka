package opencore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var ciGitEnv = []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}

var ciRefRE = regexp.MustCompile("^refs/heads/ci/[0-9a-f]{12}/shard-[0-3]$")

// PushCI writes only a parentless commit over the audited export.
func (s *CISnapshot) PushCI(ctx context.Context, target Target, ref, snapshot, body string) error {
	if !ciRefRE.MatchString(ref) || !sha40.MatchString(snapshot) || !strings.HasPrefix(body, "tree "+s.Tree+"\nauthor ") {
		return ErrBlocked
	}
	header, env, err := target.gitAuth()
	if err != nil {
		return err
	}
	git := func(input string, args ...string) (string, error) {
		child, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		cmd := gitCommand(child, s.gitDir, header, append(env, ciGitEnv...), append([]string{"-c", "http.followRedirects=false"}, args...))
		cmd.Stdin = strings.NewReader(input)
		out, err := cmd.Output()
		if err != nil {
			return "", errors.New("public CI Git operation failed")
		}
		return strings.TrimSpace(string(out)), nil
	}
	before, err := git("", "ls-remote", "--heads", target.URL, ref)
	if err != nil {
		return err
	}
	expected := snapshot + "\t" + ref
	if before != "" && before != expected {
		return ErrBlocked
	}
	// Reject a commit body that could introduce a parent or extra header.
	headerBlock, _, ok := strings.Cut(body, "\n\n")
	if !ok || len(strings.Split(headerBlock, "\n")) != 3 || strings.Contains(headerBlock, "\nparent ") {
		return ErrBlocked
	}
	if actual, err := git(body, "hash-object", "-w", "-t", "commit", "--stdin"); err != nil || actual != snapshot {
		return ErrBlocked
	}
	// StageCI audited these bytes. Recheck their identity before each push.
	if s.Tree != s.verifiedTree || s.verifyTree(ctx) != nil {
		return ErrBlocked
	}
	if before == expected {
		return nil
	}
	_, _ = git("", "push", "--no-verify", target.URL, snapshot+":"+ref)
	after, err := git("", "ls-remote", "--heads", target.URL, ref)
	if err != nil || after != expected {
		return ErrCIPushUnverified
	}
	return nil
}

var ErrCIPushUnverified = errors.New("public CI push effect unverified")

// PruneCI deletes only a matching immutable shard ref. The lease is an
// expected old value for deletion, protecting refs moved by another writer.
func (t Target) PruneCI(ctx context.Context, ref, snapshot string) error {
	if !ciRefRE.MatchString(ref) || !sha40.MatchString(snapshot) {
		return ErrBlocked
	}
	header, env, err := t.gitAuth(true)
	if err != nil {
		return err
	}
	probe := exec.CommandContext(ctx, "git", "-c", "credential.helper=", "-c", "http.followRedirects=false", "ls-remote", "--heads", t.URL, ref)
	env = append(env, ciGitEnv...)
	base := gitCommand(ctx, "", header, env, nil)
	probe.Env = base.Env
	raw, err := probe.Output()
	if err != nil {
		return errors.New("CI cleanup probe failed")
	}
	if len(raw) == 0 {
		return nil
	}
	if !bytes.Equal(bytes.TrimSpace(raw), []byte(snapshot+"\t"+ref)) {
		return ErrBlocked
	}
	dir, err := os.MkdirTemp("", "mcpshka-ci-prune-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if _, err = gitOut(ctx, dir, "init", "--bare", "--template="); err != nil {
		return err
	}
	push := gitCommand(ctx, dir, header, env, []string{"-c", "http.followRedirects=false", "push", "--no-verify", "--force-with-lease=" + ref + ":" + snapshot, t.URL, ":" + ref})
	if err := push.Run(); err != nil {
		return errors.New("CI cleanup deletion failed")
	}
	return nil
}
