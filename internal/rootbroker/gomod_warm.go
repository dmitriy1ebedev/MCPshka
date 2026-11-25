package rootbroker

// MCP-197: before a sealed tree test the broker warms root's module cache
// (internal/gomodcache); the preparation itself stays network-less.

import (
	"context"
	"errors"
	"io"
	"mcpshka/internal/gomodcache"
	"mcpshka/internal/heavyqueue"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

const goModWarmDir = "/run/mcpshka-gomod-warm"

// warmGoModules is best effort: the scheduling RPC has 50 s, so it waits at
// most 30 s and a slower warm-up finishes for the next preparation.
func warmGoModules(ctx context.Context, repo, commit string) {
	sum, mod := &limitedBuffer{limit: 1 << 20}, &limitedBuffer{limit: 1 << 20}
	if repoGit(ctx, repo, sum, "cat-file", "blob", commit+":go.sum") != nil || sum.truncated {
		return // not a Go module at the root
	}
	if missing, err := gomodcache.Missing(sum.buf.Bytes(), gomodcache.Root); err != nil || len(missing) == 0 {
		return
	}
	if repoGit(ctx, repo, mod, "cat-file", "blob", commit+":go.mod") != nil || mod.truncated {
		return
	}
	unit := "mcpshka-gomod-warm-" + shaHex([]byte(repo + "\x00" + commit))[:24]
	dir := filepath.Join(goModWarmDir, unit)
	if os.MkdirAll(goModWarmDir, 0o700) != nil || os.Mkdir(dir, 0o700) != nil {
		return // a previous call owns it
	}
	if os.WriteFile(filepath.Join(dir, "go.mod"), mod.buf.Bytes(), 0o600) != nil || os.WriteFile(filepath.Join(dir, "go.sum"), sum.buf.Bytes(), 0o600) != nil {
		_ = os.RemoveAll(dir)
		return
	}
	child, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(child, "/usr/bin/systemd-run", heavyqueue.HeavyUnitArgs(gomodcache.WarmArgs(unit, dir))...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	if cmd.Run() != nil && child.Err() == nil {
		_ = os.RemoveAll(dir) // failed outright; ExecStopPost cleans the rest
	}
}

func repoGit(ctx context.Context, repo string, stdout io.Writer, args ...string) error {
	info, err := os.Lstat(repo)
	if err != nil || !info.IsDir() {
		return errors.New("repository is not a directory")
	}
	st := info.Sys().(*syscall.Stat_t)
	if st.Uid == 0 {
		return errors.New("refusing a root-owned repository")
	}
	full := append([]string{"-C", repo, "-c", "core.fsmonitor=false", "-c", "safe.directory=" + repo}, args...)
	cmd := exec.CommandContext(ctx, "/usr/bin/git", full...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: st.Uid, Gid: st.Gid, Groups: []uint32{}}}
	cmd.Stdout = stdout
	return cmd.Run()
}
