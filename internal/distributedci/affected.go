package distributedci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Affected selection for the integration gate (ROADMAP S5.FAST.3): a merge
// into main tests the packages its diff touches, their reverse dependencies
// and the architecture checks. A release always needs the full suite, so
// any change to build inputs, CI, packaging, release notes or the version
// selects the full suite, and the release receipt refuses partial evidence.

// AffectedEnv carries the sealed package list into the sandboxed run.
const AffectedEnv = "MCPSHKA_TEST_PACKAGES"

const (
	affectedMaxPackages = 120
	affectedMaxBytes    = 4000
	archlintDir         = "./internal/archlint"
)

var affectedPkgRE = regexp.MustCompile(`^\./(internal|cmd)(/[A-Za-z0-9_][A-Za-z0-9_.-]*)+$`)

// AffectedCommand runs the sealed list inside the sandbox (cmd/ci-shard).
var AffectedCommand = []string{"go", "run", "./cmd/ci-shard", "affected"}

// AffectedRun reports the exact affected command with only the sealed list
// in its environment.
func AffectedRun(command []string, env map[string]string) bool {
	return strings.Join(command, "\x00") == strings.Join(AffectedCommand, "\x00") && len(env) == 1 && ValidAffectedPackages(env[AffectedEnv])
}

// ValidAffectedPackages accepts the sealed list: sorted, unique, relative
// package directories under internal/ or cmd/, within the env budget.
func ValidAffectedPackages(v string) bool {
	if v == "" || len(v) > affectedMaxBytes {
		return false
	}
	pkgs := strings.Split(v, " ")
	if len(pkgs) > affectedMaxPackages {
		return false
	}
	for i, p := range pkgs {
		if !affectedPkgRE.MatchString(p) || strings.Contains(p, "/../") || strings.HasSuffix(p, "/..") || (i > 0 && p <= pkgs[i-1]) {
			return false
		}
	}
	return true
}

// needsFull reports a changed path that only the full suite covers.
func needsFull(p string) bool {
	switch p {
	case "go.mod", "go.sum", "Makefile", "VERSION", ".gitlab-ci.yml", "gitflic-ci.yaml",
		"internal/mcpserver/server.go", "internal/rootbroker/health.go":
		return true
	}
	for _, prefix := range []string{"packaging/", ".github/", ".gitverse/", ".sourcecraft/", ".mcpshka/", "docs/releases/", "cmd/ci-shard/", "internal/distributedci/"} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// ignorable paths are documentation that no Go test reads.
func ignorable(p string) bool {
	return strings.HasPrefix(p, "docs/") || strings.HasPrefix(p, "skills/") || p == "LICENSE" || p == "NOTICE" || (strings.HasSuffix(p, ".md") && !strings.Contains(p, "/testdata/") && !strings.HasPrefix(p, "internal/"))
}

type listedPackage struct {
	Dir, ImportPath                    string
	Imports, TestImports, XTestImports []string
	Error                              *struct{ Err string }
}

// SelectAffected maps changed files onto the package graph (relative dirs)
// and closes over reverse dependencies. ok=false selects the full suite.
func SelectAffected(graph map[string][]string, changed []string) ([]string, bool) {
	selected := map[string]bool{}
	for _, f := range changed {
		if f == "" {
			continue
		}
		if needsFull(f) {
			return nil, false
		}
		dir, found := path.Dir(f), false
		for d := dir; d != "." && d != "/"; d = path.Dir(d) {
			if _, ok := graph["./"+d]; ok {
				selected["./"+d] = true
				found = true
				break
			}
		}
		if !found && !ignorable(f) {
			return nil, false
		}
	}
	reverse := map[string][]string{}
	for pkg, deps := range graph {
		for _, d := range deps {
			reverse[d] = append(reverse[d], pkg)
		}
	}
	queue := make([]string, 0, len(selected))
	for p := range selected {
		queue = append(queue, p)
	}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		for _, r := range reverse[p] {
			if !selected[r] {
				selected[r] = true
				queue = append(queue, r)
			}
		}
	}
	if _, ok := graph[archlintDir]; ok {
		selected[archlintDir] = true
	}
	out := make([]string, 0, len(selected))
	for p := range selected {
		out = append(out, p)
	}
	sort.Strings(out)
	if len(out) == 0 || len(out) > affectedMaxPackages || len(out)*10 > len(graph)*6 || !ValidAffectedPackages(strings.Join(out, " ")) {
		return nil, false
	}
	return out, true
}

// Affected selects the packages commit changes relative to its merge base
// with ref. nil without error means the full suite.
func Affected(ctx context.Context, repo, commit, ref string) ([]string, error) {
	if !shaRE.MatchString(commit) {
		return nil, errors.New("full commit required")
	}
	base, e := git(ctx, repo, "merge-base", commit, ref)
	if e != nil || !shaRE.MatchString(base) || base == commit {
		return nil, nil
	}
	raw, e := command(ctx, repo, "git", "--no-replace-objects", "-c", "safe.directory="+repo, "diff", "--no-renames", "--name-only", "-z", base, commit, "--")
	if e != nil {
		return nil, e
	}
	changed := strings.Split(string(raw), "\x00")
	dir, e := materialize(ctx, repo, commit)
	if e != nil {
		return nil, e
	}
	defer os.RemoveAll(dir)
	listed, e := command(ctx, dir, "go", "list", "-e", "-json", "./...")
	if e != nil {
		return nil, e
	}
	graph, e := packageGraph(dir, listed)
	if e != nil {
		return nil, e
	}
	pkgs, ok := SelectAffected(graph, changed)
	if !ok {
		return nil, nil
	}
	return pkgs, nil
}

// AffectedOrFull is Affected for the gate: any selection failure or slow
// graph means the full suite, never less.
func AffectedOrFull(ctx context.Context, repo, commit string) []string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	pkgs, err := Affected(ctx, repo, commit, "main")
	if err != nil {
		log.Printf("integration gate: affected selection failed, full suite: %v", err)
	}
	return pkgs
}

// packageGraph turns go list -json output into relative package dirs and
// their in-module imports (production and test).
func packageGraph(root string, raw []byte) (map[string][]string, error) {
	var listed []listedPackage
	dec := json.NewDecoder(bytes.NewReader(raw))
	for {
		var p listedPackage
		e := dec.Decode(&p)
		if e == io.EOF {
			break
		}
		if e != nil || p.Error != nil {
			return nil, errors.New("incomplete package graph")
		}
		listed = append(listed, p)
	}
	byImport := map[string]string{}
	for _, p := range listed {
		rel, e := filepath.Rel(root, p.Dir)
		if e != nil || rel == "." || strings.HasPrefix(rel, "..") {
			continue
		}
		byImport[p.ImportPath] = "./" + filepath.ToSlash(rel)
	}
	graph := map[string][]string{}
	for _, p := range listed {
		self, ok := byImport[p.ImportPath]
		if !ok {
			continue
		}
		deps := []string{}
		for _, imp := range append(append(append([]string{}, p.Imports...), p.TestImports...), p.XTestImports...) {
			if d, ok := byImport[imp]; ok && d != self {
				deps = append(deps, d)
			}
		}
		graph[self] = deps
	}
	if len(graph) == 0 {
		return nil, errors.New("empty package graph")
	}
	return graph, nil
}

// RunAffected vets and tests the sealed package list inside the sandbox and
// returns the process exit code.
func RunAffected(ctx context.Context, list string, out io.Writer) int {
	if !ValidAffectedPackages(list) {
		io.WriteString(out, "affected package list invalid\n")
		return 2
	}
	pkgs := strings.Split(list, " ")
	for _, args := range [][]string{append([]string{"vet", "-p", "1"}, pkgs...), append([]string{"test", "-p", "1", "-count=1", "-timeout=45m"}, pkgs...)} {
		c := exec.CommandContext(ctx, "go", args...)
		c.Stdout, c.Stderr = out, out
		if e := c.Run(); e != nil {
			var exit *exec.ExitError
			if errors.As(e, &exit) && exit.ExitCode() > 0 {
				return exit.ExitCode()
			}
			return 2
		}
	}
	return 0
}
