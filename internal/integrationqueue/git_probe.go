package integrationqueue

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

type gitProbeOutput struct {
	data     []byte
	overflow bool
}

func (b *gitProbeOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := 32768 - len(b.data)
	if len(p) > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	b.data = append(b.data, p...)
	return n, nil
}

func readGit(ctx context.Context, repo string, args ...string) (string, error) {
	fixed := []string{"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "-c", "core.fsync=reference", "-C", repo}
	cmd := exec.CommandContext(ctx, "/usr/bin/git", append(fixed, args...)...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_OPTIONAL_LOCKS=0", "GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1"}
	var out gitProbeOutput
	cmd.Stdout = &out
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if out.overflow {
		return "", errors.New("Git probe output exceeds 32 KiB")
	}
	return strings.TrimSpace(string(out.data)), err
}

func integrationReceipt(ctx context.Context, repo, ref string) (string, bool, error) {
	_, err := readGit(ctx, repo, "show-ref", "--verify", "--quiet", ref)
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return "", false, nil
		}
		return "", false, errors.New("integration effect receipt unverified: HOLD")
	}
	head, err := directTargetHead(ctx, repo, ref)
	return head, true, err
}

func isAncestor(ctx context.Context, repo, ancestor, descendant string) (bool, error) {
	_, err := readGit(ctx, repo, "merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// directTargetHead rejects symbolic refs and all ambiguous probe failures.
func directTargetHead(ctx context.Context, repo, ref string) (string, error) {
	_, err := readGit(ctx, repo, "symbolic-ref", "-q", ref)
	var exit *exec.ExitError
	if err == nil || !errors.As(err, &exit) || exit.ExitCode() != 1 {
		return "", errors.New("target is symbolic or unverified: HOLD")
	}
	head, err := readGit(ctx, repo, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil || !hexCommit.MatchString(head) {
		return "", errors.New("target unavailable: HOLD")
	}
	return head, nil
}
