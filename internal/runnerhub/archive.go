package runnerhub

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Profiles are the only commands a runner executes. A job never carries a
// free-form shell line: the brain picks a profile and, for tests, packages.
var Profiles = map[string]struct {
	Argv  []string
	Needs string
}{
	"go-test":  {[]string{"go", "test", "-count=1"}, "go"},
	"go-vet":   {[]string{"go", "vet"}, "go"},
	"go-build": {[]string{"go", "build"}, "go"},
}

var (
	commitRE = regexp.MustCompile(`^[0-9a-f]{40}$`)
	pkgRE    = regexp.MustCompile(`^\./[A-Za-z0-9_./-]*$`)
)

// Plan validates a request and returns the job (without id or archive).
func Plan(project, commit, profile string, packages []string, timeout int) (Job, error) {
	p, ok := Profiles[profile]
	if !ok {
		return Job{}, fmt.Errorf("profile %q unknown; use go-test, go-vet or go-build", profile)
	}
	if !commitRE.MatchString(commit) {
		return Job{}, errors.New("commit must be a full 40-hex SHA (pin exactly what runs)")
	}
	if len(packages) == 0 {
		packages = []string{"./..."}
	}
	if len(packages) > 64 {
		return Job{}, errors.New("at most 64 packages")
	}
	for _, pkg := range packages {
		if !pkgRE.MatchString(pkg) || slices.Contains(strings.Split(pkg, "/"), "..") {
			return Job{}, fmt.Errorf("package %q: use ./path or ./path/...", pkg)
		}
	}
	if timeout <= 0 {
		timeout = 3600
	}
	timeout = min(timeout, 3*3600)
	argv := append(append([]string{}, p.Argv...), packages...)
	return Job{Project: filepath.Clean(project), Commit: commit, Kind: profile, Argv: argv, Needs: strings.TrimSpace(p.Needs + " " + projectNeeds(project)), Timeout: timeout}, nil
}

// projectNeeds reads the tools a project's tests need beyond its toolchain
// (.runner-needs, e.g. "sqlite3 pyyaml"): only a runner reporting all of
// them gets the job, the rest end NO_RUNNER → project_test on H96.
func projectNeeds(project string) string {
	raw, err := os.ReadFile(filepath.Join(project, ".runner-needs"))
	if err != nil || len(raw) > 256 {
		return ""
	}
	var out []string
	for _, f := range strings.Fields(string(raw)) {
		if capRE.MatchString(f) {
			out = append(out, f)
		}
	}
	return strings.Join(out, " ")
}

var capRE = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// NeedsMet reports whether caps carry every space-separated need.
func NeedsMet(needs string, caps map[string]string) bool {
	for _, n := range strings.Fields(needs) {
		if caps[n] == "" {
			return false
		}
	}
	return true
}

// Archive writes `git archive` of the pinned commit to BundlePath(id).
func (h *Hub) Archive(ctx context.Context, j Job) error {
	if err := os.MkdirAll(filepath.Dir(h.BundlePath(j.ID)), 0o700); err != nil {
		return err
	}
	out := h.BundlePath(j.ID)
	cmd := exec.CommandContext(ctx, "git", "-C", j.Project, "archive", "--format=tar.gz", "-o", out, j.Commit)
	if msg, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(out)
		return fmt.Errorf("git archive %s: %v: %s", j.Commit[:12], err, strings.TrimSpace(string(msg)))
	}
	return nil
}

// Agent is the pull-runner script served at /runner/agent.py.
//
//go:embed agent.py
var Agent []byte

//go:embed setup.sh
var setupScript string

// Setup is the one-line installer served at /runner/setup for origin.
func Setup(origin string) []byte { return []byte(strings.ReplaceAll(setupScript, "@ORIGIN@", origin)) }
