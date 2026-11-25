// Package sandboxbind resolves and passes host runtime paths (a suite's venv,
// engine) read-only into the executor sandbox.
package sandboxbind

import (
	"bufio"
	"mcpshka/internal/failure"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// A project suite may need dependencies that live outside its commit, such as
// LANE's Python venv. The owner allowlists host prefixes in a root-owned file;
// a profile names paths; the root constructor resolves and checks each one
// and the root worker passes it to the sandbox read-only as a descriptor, so
// the guest UID never needs host permission to reach it.

// Allowlist is the root-owned list of host prefixes a suite may see.
const Allowlist = "/etc/mcpshka/runtime-paths.allow"

// Bind maps a profile path to its resolved host directory.
type Bind struct {
	Path   string `json:"path"`
	Target string `json:"target"`
}

const (
	maxRuntimeBinds = 8
	maxRuntimeEnv   = 16
)

var runtimeEnvKey = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// reservedRuntimeEnv are set by the sandbox itself and never by a profile.
var reservedRuntimeEnv = map[string]bool{"PATH": true, "HOME": true, "TMPDIR": true, "LANG": true, "GOCACHE": true, "GOMODCACHE": true, "GOFLAGS": true, "GOPROXY": true, "GOTOOLCHAIN": true, "GOTMPDIR": true}

func ValidEnv(env map[string]string) bool {
	if len(env) > maxRuntimeEnv {
		return false
	}
	for k, v := range env {
		if !runtimeEnvKey.MatchString(k) || reservedRuntimeEnv[k] || strings.HasPrefix(k, "LD_") || len(v) > 4096 || strings.ContainsAny(v, "\x00\r\n") {
			return false
		}
	}
	return true
}

// sandboxReservedPaths are owned by the sandbox itself; no bind may cover them.
var sandboxReservedPaths = []string{"/workspace", "/scratch", "/tmp", "/run", "/proc", "/dev", "/usr", "/etc", "/bin", "/lib", "/lib64"}

func cleanAbs(p string) bool {
	return filepath.IsAbs(p) && filepath.Clean(p) == p && p != "/" && !strings.ContainsAny(p, "\x00\r\n:")
}

func ValidBinds(binds []Bind) bool {
	if len(binds) > maxRuntimeBinds {
		return false
	}
	seen := map[string]bool{}
	for _, b := range binds {
		if !cleanAbs(b.Path) || !cleanAbs(b.Target) || seen[b.Path] || seen[b.Target] {
			return false
		}
		for _, reserved := range sandboxReservedPaths {
			if b.Path == reserved || strings.HasPrefix(b.Path, reserved+"/") || b.Target == reserved || strings.HasPrefix(b.Target, reserved+"/") {
				return false
			}
		}
		seen[b.Path], seen[b.Target] = true, true
	}
	return true
}

func readRuntimeAllowlist(path string) ([]string, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() > 16384 {
		return nil, failure.ErrHold
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, failure.ErrHold
	}
	defer f.Close()
	var prefixes []string
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !cleanAbs(line) {
			return nil, failure.ErrHold
		}
		prefixes = append(prefixes, line)
	}
	return prefixes, scan.Err()
}

func underAny(p string, prefixes []string) bool {
	for _, pre := range prefixes {
		if p == pre || strings.HasPrefix(p, pre+"/") {
			return true
		}
	}
	return false
}

// Resolve turns profile paths into checked host directories: the
// declared path and its symlink-resolved target must both fall under the
// tested repository (its own untracked data, e.g. private/, never .git) or an
// allowlisted prefix, and the target must be a real directory (MCP-146: an
// empty owner list rejected a project's own private engine without a reason).
func Resolve(paths []string, allowlist string, repo ...string) ([]Bind, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	const site = "executor.Resolve"
	if len(paths) > maxRuntimeBinds {
		return nil, hold(failure.PolicyDenied, "RUNTIME_PATHS_TOO_MANY", site, "at most 8 runtime_paths", nil)
	}
	prefixes, err := readRuntimeAllowlist(allowlist)
	if err != nil {
		return nil, hold(failure.PolicyDenied, "RUNTIME_ALLOWLIST_UNTRUSTED", site, allowlist+" must be a root-owned regular file of clean absolute prefixes", err)
	}
	var own []string
	for _, r := range repo {
		if cleanAbs(r) {
			own = append(own, r)
		}
	}
	allowed := func(p string) bool {
		for _, r := range own {
			if strings.HasPrefix(p, r+"/") && !underAny(p, []string{r + "/.git"}) {
				return true
			}
		}
		return underAny(p, prefixes)
	}
	var out []Bind
	for _, p := range paths {
		if !cleanAbs(p) || !allowed(p) {
			return nil, hold(failure.PolicyDenied, "RUNTIME_PATH_NOT_ALLOWED", site, "runtime path "+p+" is outside the tested repository and "+allowlist, nil)
		}
		target, err := filepath.EvalSymlinks(p)
		if err != nil || !allowed(target) {
			return nil, hold(failure.PolicyDenied, "RUNTIME_PATH_ESCAPES", site, "runtime path "+p+" is missing or resolves outside the allowed prefixes", err)
		}
		info, err := os.Stat(target)
		if err != nil || !info.IsDir() {
			return nil, hold(failure.PolicyDenied, "RUNTIME_PATH_NOT_DIR", site, "runtime path "+p+" must be a directory", err)
		}
		out = append(out, Bind{Path: p, Target: target})
	}
	if !ValidBinds(out) {
		return nil, hold(failure.PolicyDenied, "RUNTIME_PATH_RESERVED", site, "runtime paths must be distinct and must not cover sandbox paths", nil)
	}
	return out, nil
}

// SandboxArgs binds each resolved target read-only from inherited
// descriptor firstFD+i at its own path and links the declared path to it, then
// sets the profile environment in a stable order.
func SandboxArgs(binds []Bind, env map[string]string, firstFD int) []string {
	var args []string
	for i, b := range binds {
		args = append(args, "--ro-bind-fd", strconv.Itoa(firstFD+i), b.Target)
		if b.Path != b.Target {
			args = append(args, "--dir", filepath.Dir(b.Path), "--symlink", b.Target, b.Path)
		}
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--setenv", k, env[k])
	}
	return args
}

// Open opens each sealed target as root before the guest starts,
// re-checking that the declared path still resolves to the sealed directory.
func Open(binds []Bind) ([]*os.File, error) {
	var files []*os.File
	fail := func() ([]*os.File, error) {
		for _, f := range files {
			f.Close()
		}
		return nil, failure.ErrHold
	}
	for _, b := range binds {
		if target, err := filepath.EvalSymlinks(b.Path); err != nil || target != b.Target {
			return fail()
		}
		f, err := os.OpenFile(b.Target, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if err != nil {
			return fail()
		}
		files = append(files, f)
	}
	return files, nil
}

func hold(class failure.Class, code, site, message string, cause error) error {
	if failure.Details(cause) != nil {
		return cause
	}
	return failure.NewHold(class, code, site, message, cause)
}
