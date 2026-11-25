package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mcpshka/internal/projectenv"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// A project's dependencies (Python requirements, an npm lockfile) live outside
// its commit. MCPshka builds them itself — no per-project lists: the root
// constructor derives the environment from the commit's dependency files, the
// worker builds it once in a networked sandbox into a cache keyed by those
// files, and the offline test sandbox sees it read-only at /env.

// EnvRoot holds built environments, one directory per key.
const EnvRoot = AuthorityRoot + "/envs"

// EnvMount is where a suite sees its environment.
const EnvMount = projectenv.Mount

const envReady = ".mcpshka-env-ready"

// The worker unit binds the environment directories here (as PID 1 does for
// the workspace and scratch): the guest UID cannot traverse the 0700
// executor authority, so bwrap could not reach /proc/self/fd paths into it.
const (
	EnvReadyMountPath = "/run/mcpshka-executor-env"
	EnvBuildMountPath = "/run/mcpshka-executor-env-build"
)

type EnvSpec = projectenv.EnvSpec

var envKeyPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func detectEnv(ctx context.Context, repo, commit string) (*EnvSpec, error) {
	return projectenv.Detect(ctx, repo, commit, gitSnapshotRead)
}

// envReadyDir returns the built environment directory if it is complete.
func envReadyDir(key string) (string, bool) {
	if !envKeyPattern.MatchString(key) {
		return "", false
	}
	dir := filepath.Join(EnvRoot, key)
	info, err := os.Lstat(filepath.Join(dir, envReady))
	return dir, err == nil && info.Mode().IsRegular()
}

// prepareEnvBuild creates the private partial directory the guest fills.
func prepareEnvBuild(key string) (string, error) {
	if !envKeyPattern.MatchString(key) {
		return "", ErrHold
	}
	if err := os.MkdirAll(EnvRoot, 0711); err != nil {
		return "", err
	}
	partial := filepath.Join(EnvRoot, key+".partial")
	_ = os.RemoveAll(partial) // a crashed earlier build is never trusted
	if err := os.Mkdir(partial, 0700); err != nil {
		return "", err
	}
	if err := os.Chown(partial, 994, 994); err != nil {
		return "", err
	}
	return partial, nil
}

// sealEnvBuild publishes a successful build atomically.
func sealEnvBuild(key string) (string, error) {
	partial := filepath.Join(EnvRoot, key+".partial")
	final := filepath.Join(EnvRoot, key)
	if err := os.WriteFile(filepath.Join(partial, envReady), []byte(key+"\n"), 0444); err != nil {
		return "", err
	}
	if err := os.Rename(partial, final); err != nil {
		return "", errors.Join(err, os.RemoveAll(partial))
	}
	_ = os.Chmod(final, 0755)
	return final, nil
}

// envBuildArgs is the networked build sandbox: the commit tree read-only at
// /workspace (fd 3), the partial environment writable at /env (fd 4), DNS and
// CA certificates, no other host state.
func envBuildArgs(e *EnvSpec) []string {
	args := []string{"--unshare-all", "--share-net", "--cap-drop", "ALL", "--die-with-parent", "--new-session", "--clearenv",
		"--ro-bind", "/usr", "/usr", "--symlink", "usr/bin", "/bin", "--symlink", "usr/lib", "/lib", "--symlink", "usr/lib64", "/lib64",
		"--ro-bind", "/etc/ld.so.cache", "/etc/ld.so.cache", "--ro-bind-try", "/etc/resolv.conf", "/etc/resolv.conf",
		"--ro-bind-try", "/etc/ssl", "/etc/ssl", "--ro-bind-try", "/etc/ca-certificates", "/etc/ca-certificates",
		"--proc", "/proc", "--dev", "/dev", "--size", "268435456", "--tmpfs", "/tmp",
		"--ro-bind-fd", "3", "/workspace", "--bind-fd", "4", EnvMount, "--chdir", "/workspace",
		"--setenv", "PATH", "/usr/bin:/bin", "--setenv", "HOME", "/tmp", "--setenv", "LANG", "C.UTF-8", "--setenv", "TMPDIR", "/tmp", "--"}
	return append(args, e.Command...)
}

// ensureProjectEnv returns a read-only descriptor of the built environment,
// building it first in the networked sandbox when the cache lacks it. Build
// output goes to out so a failure explains itself.
func ensureProjectEnv(ctx context.Context, e *EnvSpec, workspace *os.File, out io.Writer) (*os.File, error) {
	_, ready := envReadyDir(e.Key)
	mount := EnvReadyMountPath
	if !ready {
		// The constructor created the partial directory; the unit bound it.
		mount = EnvBuildMountPath
		pf, err := openEnvMount(EnvBuildMountPath, false)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(out, "[mcpshka] building %s environment %s from %s\n", e.Kind, e.Key, strings.Join(e.Files, ", "))
		bctx, cancel := context.WithTimeout(ctx, envBuildTimeout)
		cmd := exec.CommandContext(bctx, "/usr/bin/bwrap", envBuildArgs(e)...)
		cmd.Dir = "/"
		cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
		cmd.ExtraFiles = []*os.File{workspace, pf}
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 994, Gid: 994}}
		cmd.Stdout, cmd.Stderr = out, out
		cmd.WaitDelay = 3 * time.Second
		runErr := cmd.Run()
		cancel()
		pf.Close()
		if runErr != nil {
			// The next constructor discards the partial directory.
			return nil, fmt.Errorf("%s environment build: %w", e.Kind, runErr)
		}
		if _, err = sealEnvBuild(e.Key); err != nil {
			return nil, err
		}
		fmt.Fprintf(out, "[mcpshka] environment %s ready\n", e.Key)
	} else {
		fmt.Fprintf(out, "[mcpshka] using cached %s environment %s\n", e.Kind, e.Key)
	}
	// After sealing, the build bind still names the same (renamed) inode.
	return openEnvMount(mount, true)
}

// openEnvMount opens a unit-bound environment directory: a real directory
// owned by the guest UID; ready ones carry the completion marker.
func openEnvMount(path string, ready bool) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("environment mount %s unavailable: %w", path, err)
	}
	var st syscall.Stat_t
	if syscall.Fstat(int(f.Fd()), &st) != nil || st.Uid != 994 {
		f.Close()
		return nil, ErrHold
	}
	if ready {
		if info, err := os.Lstat(filepath.Join(path, envReady)); err != nil || !info.Mode().IsRegular() {
			f.Close()
			return nil, errors.New("environment is not sealed: HOLD")
		}
	}
	return f, nil
}

// EnvUnitBinds are the optional unit binds for an environment key.
func EnvUnitBinds(key string) []string {
	if !envKeyPattern.MatchString(key) {
		return nil
	}
	return []string{"--property=BindPaths=-" + filepath.Join(EnvRoot, key) + ":" + EnvReadyMountPath,
		"--property=BindPaths=-" + filepath.Join(EnvRoot, key+".partial") + ":" + EnvBuildMountPath}
}

// envBuildTimeout bounds one dependency install.
const envBuildTimeout = 15 * time.Minute
