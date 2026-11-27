package forgeci

import (
	"errors"
	"mcpshka/internal/failure"
	"mcpshka/internal/jobs"
	"mcpshka/internal/projectci"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// JobInput validates an immutable provider CI operation without a shell.
func JobInput(provider, mode, commit, idempotencyKey, leaseToken, path string) ([]string, jobs.StartOptions, error) {
	if strings.TrimSpace(idempotencyKey) == "" {
		return nil, jobs.StartOptions{}, errors.New("forge_ci requires retry key")
	}
	if path != "" && filepath.Clean(path) != Root {
		_, err := projectci.RepoName(path)
		projectDistributed := provider == "distributed" && slices.Contains([]string{"plan", "publish", "status", "wait"}, mode)
		if filepath.Clean(path) != path || err != nil || !(projectDistributed || provider == "gitverse" && slices.Contains([]string{"publish", "status", "rerun", "wait"}, mode)) || !shaRE.MatchString(commit) {
			return nil, jobs.StartOptions{}, failure.Rejected("FORGE_CI_INPUT", "forge_ci", "project CI: DIAG/Lane provider=distributed mode=plan|publish|status|wait; other projects provider=gitverse mode=publish|status|rerun|wait, full 40-hex commit, path=/root/repos/<project>; nothing started", err)
		}
		timeout := 5 * time.Minute
		if mode == "wait" {
			timeout = 30 * time.Minute // one job holds a whole project CI run
		}
		return []string{Sub, provider, mode, commit, filepath.Clean(path)}, jobs.StartOptions{
			HolderToken: leaseToken, Timeout: timeout, OutputLimitBytes: 32768, MemoryLimitBytes: 256 << 20,
			CPUTimeLimit: 120 * time.Second, CPUQuotaPercent: 100, ProcessLimit: 64, PrivateTemp: true,
		}, nil
	}
	if _, err := Argv(provider, mode, commit); err != nil {
		return nil, jobs.StartOptions{}, failure.Rejected("FORGE_CI_INPUT", "forge_ci", "input rejected before any job started; correct it and retry with the same key", err)
	}
	timeout := 120 * time.Second
	if provider == "distributed" {
		timeout = 5 * time.Minute
	}
	if provider == "gitverse" && mode == "publish" {
		timeout = PublishTimeout + 30*time.Second
	}
	if provider == "distributed" && mode == "publish" {
		// Export, audit and package inventory are local CPU work even when
		// every test shard runs off-host. Admit this job through the heavy queue.
		return []string{Sub, provider, mode, commit}, jobs.StartOptions{
			Env: runnerJobEnv(mode), HolderToken: leaseToken, Timeout: 10 * time.Minute,
			OutputLimitBytes: 32768, MemoryLimitBytes: 1 << 30, CPUTimeLimit: 6 * time.Minute,
			CPUQuotaPercent: 100, ProcessLimit: 512, PrivateTemp: true, LowPriority: true,
		}, nil
	}
	if provider == "gigaide" && mode == "runner" {
		return []string{Sub, provider, mode, commit}, jobs.StartOptions{
			Env: runnerJobEnv(mode), HolderToken: leaseToken, Timeout: timeout,
			OutputLimitBytes: 32768, MemoryLimitBytes: 1 << 30, CPUTimeLimit: 120 * time.Second,
			CPUQuotaPercent: 150, ProcessLimit: 512, PrivateTemp: true,
		}, nil
	}
	// Go runtime threads count against ProcessLimit: 16 made the forge-ci
	// subcommand fail pthread_create at start (BUG-20261024-103).
	return []string{Sub, provider, mode, commit}, jobs.StartOptions{
		Env:         runnerJobEnv(mode),
		HolderToken: leaseToken,
		Timeout:     timeout, OutputLimitBytes: 32768, MemoryLimitBytes: 256 << 20,
		CPUTimeLimit: 60 * time.Second, CPUQuotaPercent: 100, ProcessLimit: 64, PrivateTemp: true,
	}, nil
}
