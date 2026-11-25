package releasequeue

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mcpshka/internal/notify"
	"mcpshka/internal/salvage"
)

var readReleaseGit = salvage.Git

// discover records a clean, versioned release worktree as a proposal. Only
// authenticated queue admission can attach owner and protected store authority.
func (w *Watcher) discover(ctx context.Context) {
	if w.DiscoveryIdle == nil || w.Installed == nil {
		return
	}
	version := w.Installed(ctx)
	if !VersionRe.MatchString(version) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	list, err := readReleaseGit(ctx, Repo, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return
	}
	for _, record := range strings.Split(list, "\x00\x00") {
		parts := strings.Split(record, "\x00")
		if len(parts) < 3 {
			continue
		}
		path, pathOK := strings.CutPrefix(parts[0], "worktree ")
		head, headOK := strings.CutPrefix(parts[1], "HEAD ")
		branch, branchOK := strings.CutPrefix(parts[2], "branch refs/heads/")
		candidate, named := strings.CutPrefix(filepath.Base(path), "mcpshka-release-")
		if !pathOK || !headOK || !branchOK || !named || filepath.Dir(path) != "/root/repos/wt" ||
			!commitRe.MatchString(head) || !branchRe.MatchString(branch) || !newerRelease(candidate, version) {
			continue
		}
		key := Prefix + candidate + ".json"
		if _, err := w.DB.GetContext(ctx, key); err == nil || !w.DiscoveryIdle(path) {
			continue
		}
		declared, err := readReleaseGit(ctx, path, "show", head+":VERSION")
		if err != nil || strings.TrimSpace(declared) != candidate {
			continue
		}
		dirty, err := readReleaseGit(ctx, path, "status", "--porcelain", "--untracked-files=normal")
		if err != nil || dirty != "" || !w.DiscoveryIdle(path) {
			continue
		}
		current, err := readReleaseGit(ctx, path, "rev-parse", "--verify", "HEAD")
		if err != nil || strings.TrimSpace(current) != head {
			continue
		}
		now := w.Now().UTC()
		it := Item{Version: candidate, Branch: branch, Commit: head, PreviousVersion: version, CreatedAt: now}
		it.move(now, "DISCOVERED", "clean release worktree; awaiting owner-bound release admission")
		raw, _ := json.Marshal(it)
		if inserted, err := w.DB.PutIfAbsent(key, raw); err == nil && inserted {
			_ = notify.Release(w.DB, candidate, it.Stage, it.Detail, now)
			return
		}
	}
}

func newerRelease(candidate, installed string) bool {
	if !VersionRe.MatchString(candidate) || !VersionRe.MatchString(installed) {
		return false
	}
	a, b := strings.Split(candidate, "."), strings.Split(installed, ".")
	for i := 1; i < 3; i++ {
		x, _ := strconv.Atoi(a[i])
		y, _ := strconv.Atoi(b[i])
		if x != y {
			return x > y
		}
	}
	return false
}
