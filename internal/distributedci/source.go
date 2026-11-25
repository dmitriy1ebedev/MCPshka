package distributedci

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

var pinnedToolchain = regexp.MustCompile(`^go[0-9]+\.[0-9]+\.[0-9]+$`)

func command(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, name, args...)
	c.Dir = dir
	toolchain := "local"
	if pinnedToolchain.MatchString(os.Getenv("GOTOOLCHAIN")) {
		toolchain = os.Getenv("GOTOOLCHAIN")
	}
	c.Env = append(os.Environ(), "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN="+toolchain, "GOFLAGS=-mod=readonly -buildvcs=false")
	// CLI jobs may have no home; retain an explicit sealed-worker cache.
	if os.Geteuid() == 0 || os.Geteuid() == 994 && os.Getenv("GOCACHE") == "" {
		c.Env = append(c.Env, "HOME=/var/lib/mcpshka", "GOPATH=/var/lib/mcpshka/go", "GOCACHE=/var/lib/mcpshka/.cache/go-build")
	}
	if os.Geteuid() == 0 {
		c.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 994, Gid: 994}}
	}
	var b, stderr capBuffer
	b.limit, stderr.limit = 64<<20, 4<<10
	c.Stdout, c.Stderr = &b, &stderr
	err := c.Run()
	if err != nil || b.exceeded {
		// The temporary snapshot is not a Git checkout: without -buildvcs=false
		// go list failed silently here and distributed CI never planned.
		return nil, fmt.Errorf("bounded source command failed: %s %s: %s", name, firstArg(args), lastLine(stderr.String()))
	}
	return b.Bytes(), nil
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

// lastLine keeps a diagnosable, bounded tail of a tool error.
func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

type capBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *capBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		b.exceeded = true
		return 0, errors.New("output limit")
	}
	return b.Buffer.Write(p)
}
func git(ctx context.Context, repo string, args ...string) (string, error) {
	b, e := command(ctx, repo, "git", append([]string{"--no-replace-objects", "-c", "safe.directory=" + repo, "-c", "core.hooksPath=/dev/null"}, args...)...)
	return strings.TrimSpace(string(b)), e
}

// SnapshotSHA matches the existing parentless GitVerse snapshot protocol.
func SnapshotSHA(commit, tree, raw string) (string, error) {
	var author, committer string
	for _, line := range strings.Split(strings.SplitN(raw, "\n\n", 2)[0], "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		stamp := strings.Join(fields[len(fields)-2:], " ")
		if fields[0] == "author" {
			author = stamp
		}
		if fields[0] == "committer" {
			committer = stamp
		}
	}
	if !shaRE.MatchString(commit) || !shaRE.MatchString(tree) || author == "" || committer == "" {
		return "", errors.New("invalid snapshot identity")
	}
	const who = "MCPshka Snapshot <snapshot@mcpshka.local> "
	body := "tree " + tree + "\nauthor " + who + author + "\ncommitter " + who + committer + "\n\nMCPshka immutable snapshot\n\nSource-Commit: " + commit + "\nSource-Tree: " + tree + "\n"
	h := sha1.Sum([]byte("commit " + strconv.Itoa(len(body)) + "\x00" + body))
	return hex.EncodeToString(h[:]), nil
}
func inventory(ctx context.Context, dir string) ([]string, map[string]int, map[string]float64, error) {
	raw, e := command(ctx, dir, "go", "list", "./...")
	if e != nil {
		return nil, nil, nil, e
	}
	packages := strings.Fields(string(raw))
	moduleRaw, e := command(ctx, dir, "go", "list", "-m")
	if e != nil {
		return nil, nil, nil, e
	}
	module := strings.TrimSpace(string(moduleRaw))
	counts := map[string]int{}
	for _, pkg := range packages {
		if !pkgRE.MatchString(pkg) || pkg != module && !strings.HasPrefix(pkg, module+"/") {
			return nil, nil, nil, errors.New("unexpected package")
		}
		rel := strings.TrimPrefix(pkg, module)
		rel = strings.TrimPrefix(rel, "/")
		entries, _ := filepath.Glob(filepath.Join(dir, rel, "*_test.go"))
		for _, name := range entries {
			f, e := parser.ParseFile(token.NewFileSet(), name, nil, 0)
			if e != nil {
				return nil, nil, nil, e
			}
			for _, d := range f.Decls {
				if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") {
					counts[pkg]++
				}
			}
		}
	}
	var plan struct {
		Weights map[string]float64 `json:"weights"`
	}
	b, e := os.ReadFile(filepath.Join(dir, "packaging/ci/plan.json"))
	if os.IsNotExist(e) && module != "mcpshka" {
		return packages, counts, nil, nil
	}
	if e != nil {
		return nil, nil, nil, e
	}
	if e = json.Unmarshal(b, &plan); e != nil {
		return nil, nil, nil, e
	}
	sort.Strings(packages)
	return packages, counts, plan.Weights, nil
}

// LoadPlan materializes only committed files, bounded to 64 MiB, then uses go list.
// The temporary tree is always removed; Go tooling never runs as root.
func LoadPlan(ctx context.Context, repo, commit string) (Plan, error) {
	if !shaRE.MatchString(commit) {
		return Plan{}, errors.New("full commit required")
	}
	tree, e := git(ctx, repo, "rev-parse", commit+"^{tree}")
	if e != nil {
		return Plan{}, e
	}
	raw, e := git(ctx, repo, "cat-file", "commit", commit)
	if e != nil {
		return Plan{}, e
	}
	snap, e := SnapshotSHA(commit, tree, raw)
	if e != nil {
		return Plan{}, e
	}
	dir, e := materialize(ctx, repo, commit)
	if e != nil {
		return Plan{}, e
	}
	defer os.RemoveAll(dir)
	packages, counts, weights, e := inventory(ctx, dir)
	if e != nil {
		return Plan{}, e
	}
	return NewPlan(commit, tree, snap, packages, weights, counts, len(Sites))
}
func Ref(p Plan) string { return "mcpshka-distributed/" + p.Commit }
func identity(ctx context.Context, dir string) (string, string, string, error) {
	head, e := git(ctx, dir, "rev-parse", "HEAD")
	if e != nil {
		return "", "", "", e
	}
	tree, e := git(ctx, dir, "rev-parse", "HEAD^{tree}")
	if e != nil {
		return "", "", "", e
	}
	msg, e := git(ctx, dir, "show", "-s", "--format=%B")
	if e != nil {
		return "", "", "", e
	}
	var commits, trees []string
	for _, s := range strings.Split(msg, "\n") {
		if v, ok := strings.CutPrefix(s, "Source-Commit: "); ok {
			commits = append(commits, v)
		}
		if v, ok := strings.CutPrefix(s, "Source-Tree: "); ok {
			trees = append(trees, v)
		}
	}
	if len(commits) != 1 || len(trees) != 1 || trees[0] != tree || !shaRE.MatchString(commits[0]) {
		return "", "", "", fmt.Errorf("snapshot trailers invalid")
	}
	parents, e := git(ctx, dir, "rev-list", "--parents", "-n", "1", "HEAD")
	if e != nil || parents != head {
		return "", "", "", errors.New("snapshot has parents")
	}
	return commits[0], tree, head, nil
}

// materialize extracts the committed files of commit into a fresh temporary
// directory (bounded to 64 MiB / 30000 files) owned by the Go user; the
// caller removes it.
func materialize(ctx context.Context, repo, commit string) (string, error) {
	var fs syscall.Statfs_t
	if syscall.Statfs(os.TempDir(), &fs) != nil || fs.Bavail*uint64(fs.Bsize) < max(uint64(3<<30), fs.Blocks*uint64(fs.Bsize)/10)+(64<<20) {
		return "", errors.New("temporary disk reserve unavailable")
	}
	dir, e := os.MkdirTemp("", "mcpshka-shard-")
	if e != nil {
		return "", e
	}
	keep := false
	defer func() {
		if !keep {
			os.RemoveAll(dir)
		}
	}()
	if os.Geteuid() == 0 {
		if e = os.Chown(dir, 994, 994); e != nil {
			return "", e
		}
	}
	archive, e := command(ctx, repo, "git", "--no-replace-objects", "-c", "safe.directory="+repo, "archive", "--format=tar", commit)
	if e != nil {
		return "", e
	}
	tr := tar.NewReader(bytes.NewReader(archive))
	var used int64
	files := 0
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return "", e
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		name := strings.TrimSuffix(h.Name, "/")
		if name == "" || filepath.Clean(name) != name || filepath.IsAbs(name) || strings.HasPrefix(name, "../") || strings.Contains(name, "\\") {
			return "", errors.New("unsafe snapshot path")
		}
		dest := filepath.Join(dir, name)
		if h.Typeflag == tar.TypeDir {
			if e = os.MkdirAll(dest, 0755); e != nil {
				return "", e
			}
			continue
		}
		if h.Typeflag != tar.TypeReg {
			return "", errors.New("snapshot must contain regular files")
		}
		used += h.Size
		files++
		if used > 64<<20 || files > 30000 {
			return "", errors.New("snapshot budget exceeded")
		}
		if e = os.MkdirAll(filepath.Dir(dest), 0755); e != nil {
			return "", e
		}
		f, e := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if e != nil {
			return "", e
		}
		_, e = io.CopyN(f, tr, h.Size)
		ce := f.Close()
		if e != nil {
			return "", e
		}
		if ce != nil {
			return "", ce
		}
	}
	keep = true
	return dir, nil
}
