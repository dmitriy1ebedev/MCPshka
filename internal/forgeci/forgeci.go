// Package forgeci publishes immutable snapshots of a commit to the
// interchangeable CI sites (GitVerse, MosHub, SourceCraft, GitFlic) and reads
// their CI state. It is the native
// entry point behind the forge_ci tool: interpreters are not admitted as jobs,
// so the fixed packaging scripts run only through this argv allowlist.
package forgeci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"time"
)

const (
	Command  = "/usr/local/bin/mcpshka"
	Sub      = "forge-ci"
	Root     = "/root/repos/mcpshka"
	maxOut   = 16384
	deadline = 90 * time.Second
	// PublishTimeout covers three bounded attempts and the 5s/10s backoff.
	PublishTimeout = 5 * time.Minute
)

var (
	shaRE     = regexp.MustCompile(`^[a-f0-9]{40}$`)
	Providers = []string{"distributed", "gitverse", "moshub", "sourcecraft", "gitflic", "github", "gigaide"}
	Modes     = []string{"plan", "publish", "status", "mirror", "rerun", "pr", "runner"}
)

// Argv returns the fixed interpreter argv for provider/mode/commit. Scripts
// always come from the main checkout, never from a caller-chosen worktree.
func Argv(provider, mode, commit string) ([]string, error) {
	if mode == "runner" {
		switch provider {
		case "gigaide":
			return []string{Command, "browser-run"}, nil
		case "github", "gitverse", "moshub":
			return []string{Command, Sub, provider, "runner"}, nil // in-process: gitprovider.DispatchRunner
		default:
			return nil, errors.New("runner provider must be github, gitverse, moshub or gigaide")
		}
	}
	if !shaRE.MatchString(commit) {
		return nil, errors.New("commit must be a full lowercase 40-hex SHA")
	}
	py := func(script string, rest ...string) []string {
		return append([]string{"python3", "-I", "-B", Root + "/packaging/" + script}, rest...)
	}
	switch provider + "/" + mode {
	case "distributed/plan", "distributed/publish", "distributed/status":
		return []string{Command, Sub, provider, mode, commit}, nil
	case "gitverse/plan", "gitverse/publish", "gitverse/mirror", "gitverse/rerun", "gitverse/status":
		return []string{Command, Sub, provider, mode, commit}, nil // in-process: gvRun
	case "moshub/plan", "moshub/publish":
		return py("moshub/snapshot.py", mode, "--commit", commit), nil
	case "moshub/status":
		return py("moshub/ci_status.py", commit), nil
	case "moshub/rerun":
		return py("moshub/ci_status.py", commit, "--rerun"), nil
	case "sourcecraft/plan", "sourcecraft/publish", "sourcecraft/status", "sourcecraft/mirror",
		"gitflic/plan", "gitflic/publish", "gitflic/status",
		"github/plan", "github/publish", "github/status", "github/mirror", "github/pr":
		return py("forges/extra_forge.py", provider, mode, "--commit", commit), nil
	}
	return nil, errors.New("provider must be gitverse, moshub, sourcecraft, gitflic or github and mode plan, publish or status (mirror: gitverse, sourcecraft, github; rerun: gitverse, moshub; pr: github)")
}

// Run executes one script and returns its bounded JSON stdout and exit code.
// Scripts print only reason codes on failure; stderr is discarded because Git
// and HTTP errors may carry remote URLs.
func Run(ctx context.Context, provider, mode, commit string) ([]byte, int, error) {
	if provider == "distributed" {
		return runDistributed(ctx, mode, commit)
	}
	argv, err := Argv(provider, mode, commit)
	if err != nil {
		return nil, 2, err
	}
	if mode == "runner" {
		return wakeRunner(ctx, provider, argv)
	}
	if provider == "gitverse" && mode == "publish" {
		{
			if out, _, err := gvRun(ctx, "status", commit); err == nil {
				if stop, ok := RepeatedFailureStop(StatusDiagnosis(provider, out)); ok {
					return stop, 3, errors.New("CI_REPEATED_FAILURE: this commit failed the same way 3 times in a row; not republishing — file a card and fix the cause")
				}
			}
		}
		child, cancel := context.WithTimeout(ctx, PublishTimeout)
		defer cancel()
		return publishWithRetry(child, func(ctx context.Context) ([]byte, int, error) { return gvRun(ctx, mode, commit) }, waitRetry)
	}
	var out []byte
	var code int
	if provider == "gitverse" {
		ctx, cancel := context.WithTimeout(ctx, deadline)
		defer cancel()
		out, code, err = gvRun(ctx, mode, commit)
	} else {
		out, code, err = runScript(ctx, argv)
	}
	if mode == "status" {
		out = StatusDiagnosis(provider, out)
	}
	return out, code, err
}

func runScript(ctx context.Context, argv []string) ([]byte, int, error) {
	return runScriptEnv(ctx, argv, nil)
}

func runScriptEnv(ctx context.Context, argv, env []string) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = Root
	cmd.Env = append(os.Environ(), env...)
	var out bytes.Buffer
	cmd.Stdout = &limited{buf: &out}
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, 2, ctx.Err()
	}
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		return nil, 2, errors.New("forge script did not run")
	}
	if out.Len() > maxOut {
		return nil, 2, errors.New("forge script output exceeds budget")
	}
	return bytes.TrimSpace(out.Bytes()), code, nil
}

// publishWithRetry keeps the immutable publication runner and its wait injectable.
func publishWithRetry(ctx context.Context, run func(context.Context) ([]byte, int, error), wait func(context.Context, time.Duration) error) ([]byte, int, error) {
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, 2, err
		}
		out, code, err := run(ctx)
		var result struct {
			State  string `json:"state"`
			Reason string `json:"reason_code"`
		}
		if err != nil || code == 0 || attempt == 2 || json.Unmarshal(out, &result) != nil || result.State != "HOLD" {
			return out, code, err
		}
		switch result.Reason {
		case "ls_remote_network", "push_network", "ls_remote_timeout", "push_timeout":
		default:
			return out, code, nil
		}
		// Re-run the immutable protocol: ls-remote reconciles an ambiguous push
		// before another non-forced creation of the same snapshot is attempted.
		if err := wait(ctx, time.Duration(5*(attempt+1))*time.Second); err != nil {
			return out, code, err
		}
	}
}

func waitRetry(ctx context.Context, pause time.Duration) error {
	timer := time.NewTimer(pause)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type limited struct{ buf *bytes.Buffer }

func (l *limited) Write(p []byte) (int, error) {
	if room := maxOut + 1 - l.buf.Len(); room > 0 {
		if len(p) > room {
			l.buf.Write(p[:room])
		} else {
			l.buf.Write(p)
		}
	}
	return len(p), nil
}
