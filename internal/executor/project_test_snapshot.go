package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mcpshka/internal/failure"
	"mcpshka/internal/snapshotfiles"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type SourceSnapshot struct {
	Repo          string `json:"repo"`
	Commit        string `json:"commit"`
	Package       string `json:"package"`
	Digest        string `json:"digest"`
	Tree          string `json:"tree,omitempty"`
	ReleaseDigest string `json:"release_digest,omitempty"`
}

var commitPattern = regexp.MustCompile("^[0-9a-f]{40}$")
var packageSnapshotPattern = regexp.MustCompile("^internal/[a-z][a-z0-9]*$")
var digestPattern = regexp.MustCompile("^[0-9a-f]{64}$")

func validSource(s *SourceSnapshot) bool {
	return s != nil && filepath.Clean(s.Repo) == s.Repo && strings.HasPrefix(s.Repo, "/root/repos/") && commitPattern.MatchString(s.Commit) && preparablePackage(s.Package) && digestPattern.MatchString(s.Digest)
}
func snapshotDigest(path string) (string, error) {
	return snapshotDigestLimited(path, 10000, 64<<20)
}

func snapshotDigestLimited(path string, maxFiles int, maxBytes int64) (string, error) {
	paths := []string{}
	var total int64
	e := filepath.WalkDir(path, func(p string, d os.DirEntry, e error) error {
		if e != nil || d.Type()&os.ModeSymlink != 0 || d.Name() == ".git" {
			return Hold(failure.PolicyDenied, "SNAPSHOT_ENTRY_UNSAFE", "snapshot.snapshotDigestLimited", "snapshot entry unavailable or unsafe", e)
		}
		if d.IsDir() {
			return nil
		}
		if len(paths) >= maxFiles {
			return Hold(failure.PolicyDenied, "SNAPSHOT_FILES_LIMIT", "snapshot.snapshotDigestLimited", "snapshot file count exceeded", nil)
		}
		info, e := d.Info()
		if e != nil || !info.Mode().IsRegular() {
			return Hold(failure.PolicyDenied, "SNAPSHOT_TYPE_INVALID", "snapshot.snapshotDigestLimited", "snapshot file is not regular", e)
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Nlink != 1 {
			return Hold(failure.PolicyDenied, "SNAPSHOT_LINKS_INVALID", "snapshot.snapshotDigestLimited", "snapshot file has extra links", nil)
		}
		total += info.Size()
		if total > maxBytes {
			return Hold(failure.PolicyDenied, "SNAPSHOT_BYTES_LIMIT", "snapshot.snapshotDigestLimited", "snapshot byte limit exceeded", nil)
		}
		paths = append(paths, p)
		return nil
	})
	if e != nil {
		return "", e
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		rel, e := filepath.Rel(path, p)
		if e != nil {
			return "", e
		}
		f, e := os.Open(p)
		if e != nil {
			return "", e
		}
		part := sha256.New()
		_, e = io.Copy(part, io.LimitReader(f, maxBytes+1))
		f.Close()
		if e != nil {
			return "", e
		}
		fmt.Fprintf(h, "%s%c%x%c", filepath.ToSlash(rel), 0, part.Sum(nil), 0)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type snapshotBuffer struct{ data []byte }

func (b *snapshotBuffer) Write(p []byte) (int, error) {
	if len(b.data)+len(p) > 64<<20 {
		return 0, Hold(failure.PolicyDenied, "GIT_OUTPUT_LIMIT", "snapshot.snapshotBuffer.Write", "Git output exceeds 64 MiB", nil)
	}
	b.data = append(b.data, p...)
	return len(p), nil
}
func gitSnapshotRead(ctx context.Context, repo string, args ...string) ([]byte, error) {
	return gitSnapshotReadEnv(ctx, repo, nil, args...)
}

// gitSnapshotReadEnv adds fixed environment (e.g. a private object directory)
// to the same service-user Git read.
func gitSnapshotReadEnv(ctx context.Context, repo string, env []string, args ...string) ([]byte, error) {
	started := time.Now()
	if err := ctx.Err(); err != nil {
		return nil, contextFailure(ctx, "snapshot.gitSnapshotReadEnv", started, err)
	}
	fixed := []string{"--no-replace-objects", "--no-lazy-fetch", "-c", "safe.directory=" + repo, "-c", "protocol.allow=never", "-C", repo}
	fixed = append(fixed, args...)
	cmd := exec.CommandContext(ctx, "/usr/bin/git", fixed...)
	cmd.Env = append([]string{"PATH=/usr/bin:/bin", "LANG=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_LAZY_FETCH=1"}, env...)
	if os.Geteuid() == 0 {
		account, lookupErr := user.Lookup("mcpshka")
		credential, err := snapshotGitCredential(account, lookupErr)
		if err != nil {
			return nil, err
		}
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: credential}
	}
	var out snapshotBuffer
	cmd.Stdout = &out
	// Name the failing read: a bare HOLD hid a 160s deadline (BUG-20261023-81).
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, contextFailure(ctx, "snapshot.gitSnapshotReadEnv", started, err)
		}
		return nil, Hold(failure.FailedVerified, "GIT_READ_FAILED", "snapshot.gitSnapshotReadEnv", "Git snapshot read failed", err)
	}
	return out.data, nil
}

func projectTestTree(ctx context.Context, repo, commit, pkg string) ([]byte, error) {
	// Internal imports must resolve from the same immutable commit. No root
	// documents, instructions or unrelated worktree files enter the snapshot.
	if pkg == recipeTestPackage {
		return gitSnapshotRead(ctx, repo, "ls-tree", "-rz", commit, "--", "go.mod", "go.sum", "Makefile", "cmd", "internal", "schemas")
	}
	if pkg == treeTestPackage {
		return gitSnapshotRead(ctx, repo, "ls-tree", "-rz", commit)
	}
	if pkg == localReleasePackage {
		return gitSnapshotRead(ctx, repo, "ls-tree", "-rz", commit, "--", "go.mod", "go.sum", "Makefile", "cmd", "internal", "packaging", "skills", ".github/workflows", ".gitverse/workflows")
	}
	if moduleSnapshot(pkg) {
		return snapshotfiles.ModuleTree(func() ([]byte, error) {
			return gitSnapshotRead(ctx, repo, "ls-tree", "-rz", commit, "--",
				"go.mod", "go.sum", "VERSION", "cmd", "internal", "packaging", "README.md", "README.ru.md", "CHANGELOG.md", "BUGLOG.md", "docs")
		}, ErrHold)
	}
	return gitSnapshotRead(ctx, repo, "ls-tree", "-rz", commit, "--", "go.mod", "go.sum", "internal")
}

func materializeProjectTestSource(ctx context.Context, repo, pkg, base string, list []byte) (string, error) {
	root, err := os.OpenRoot(base)
	if err != nil {
		return "", err
	}
	defer root.Close()
	total := 0
	entries := map[string]bool{}
	files := 0
	mod := false
	source := false
	makefile, schema := false, false
	for _, entry := range strings.Split(string(list), "\x00") {
		if entry == "" {
			continue
		}
		meta, name, ok := strings.Cut(entry, "\t")
		parts := strings.Fields(meta)
		allowed := name == "go.mod" || name == "go.sum" || strings.HasPrefix(name, "internal/")
		if pkg == recipeTestPackage {
			allowed = recipeTestPath(name)
		}
		if moduleSnapshot(pkg) {
			allowed = moduleTestPath(name)
			if pkg == localReleasePackage {
				allowed = releaseSourcePath(name)
			}
		}
		if pkg == treeTestPackage {
			allowed = treeTestPath(name)
		}
		// Packaging helpers are committed executable; tests only read them and
		// every snapshot file is materialized read-only 0444 regardless. A whole
		// tree keeps committed executables runnable as read-only 0555.
		executable := len(parts) == 3 && parts[0] == "100755"
		mode := len(parts) == 3 && (parts[0] == "100644" || (moduleSnapshot(pkg) && executable) || (pkg == treeTestPackage && executable))
		if !ok || len(parts) != 3 || !mode || parts[1] != "blob" || !commitPattern.MatchString(parts[2]) || filepath.Clean(name) != name || strings.Contains(name, "\\") || !allowed {
			return "", Hold(failure.PolicyDenied, "GIT_TREE_ENTRY_INVALID", "snapshot.materializeProjectTest", "Git tree entry invalid or disallowed", nil)
		}
		if len(name) > 512 || len(strings.Split(name, "/")) > 16 {
			return "", Hold(failure.PolicyDenied, "GIT_TREE_PATH_LIMIT", "snapshot.materializeProjectTest", "Git tree path length or depth exceeded", nil)
		}
		for at := name; at != "."; at = filepath.Dir(at) {
			entries[at] = true
			if len(entries) > 10000 {
				return "", Hold(failure.PolicyDenied, "GIT_TREE_ENTRIES_LIMIT", "snapshot.materializeProjectTest", "Git tree directory count exceeded", nil)
			}
		}
		for _, part := range strings.Split(name, "/") {
			if part == ".git" {
				return "", Hold(failure.PolicyDenied, "GIT_METADATA_FORBIDDEN", "snapshot.materializeProjectTest", "Git metadata in snapshot", nil)
			}
		}
		if maxFiles, _ := snapshotLimits(pkg); files >= maxFiles {
			return "", Hold(failure.PolicyDenied, "GIT_TREE_FILES_LIMIT", "snapshot.materializeProjectTest", "Git tree file count exceeded", nil)
		}
		files++
		b, e := gitSnapshotRead(ctx, repo, "cat-file", "blob", parts[2])
		if e != nil {
			return "", e
		}
		total += len(b)
		if _, maxBytes := snapshotLimits(pkg); int64(total) > maxBytes {
			return "", Hold(failure.PolicyDenied, "GIT_TREE_BYTES_LIMIT", "snapshot.materializeProjectTest", "Git tree byte limit exceeded", nil)
		}
		if e = root.MkdirAll(filepath.Dir(name), 0755); e != nil {
			return "", e
		}
		for dir := filepath.Dir(name); dir != "."; dir = filepath.Dir(dir) {
			d, err := root.Open(dir)
			if err != nil {
				return "", err
			}
			e = d.Chmod(0755)
			closeErr := d.Close()
			if e != nil {
				return "", e
			}
			if closeErr != nil {
				return "", closeErr
			}
		}
		perm := os.FileMode(0444)
		if pkg == treeTestPackage && executable {
			perm = 0555
		}
		f, e := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
		if e != nil {
			return "", e
		}
		if e = f.Chmod(perm); e != nil {
			f.Close()
			return "", e
		}
		_, e = f.Write(b)
		if e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e != nil {
			return "", e
		}
		if ce != nil {
			return "", ce
		}
		mod = mod || name == "go.mod"
		source = source || (strings.HasSuffix(name, ".go") && (strings.HasPrefix(name, pkg+"/") || ((pkg == recipeTestPackage || moduleSnapshot(pkg)) && (strings.HasPrefix(name, "internal/") || strings.HasPrefix(name, "cmd/")))))
		makefile = makefile || name == "Makefile"
		schema = schema || name == "schemas/report-v1alpha1.schema.json"
	}
	withModuleDeps := moduleSnapshot(pkg)
	if pkg == treeTestPackage {
		// Registered Go workflows need the same committed go.sum dependency set
		// as the module gate; Python/Node and stdlib-only trees need no Go cache.
		if mod {
			if _, err := root.Stat("go.sum"); err == nil {
				withModuleDeps = true
			} else if !os.IsNotExist(err) {
				return "", err
			}
		}
		mod, source = files > 0, files > 0
	}
	if !mod || !source || (pkg == recipeTestPackage && (!makefile || !schema)) {
		return "", Hold(failure.EvidenceMissing, "SOURCE_FILES_MISSING", "snapshot.materializeProjectTest", "required module or source files missing", nil)
	}
	if withModuleDeps {
		if e := copyModuleDeps(ctx, root, moduleCacheSource); e != nil {
			return "", e
		}
		if e := sealModuleDeps(root); e != nil {
			return "", e
		}
	}
	maxFiles, maxBytes := snapshotLimits(pkg)
	if pkg == treeTestPackage && withModuleDeps {
		maxFiles, maxBytes = moduleMaxFiles, moduleMaxBytes
	}
	return snapshotDigestLimited(base, maxFiles, maxBytes)
}

// Root-only committed-source bootstrap. No repository mutation or project lease transfer.
// Partial preparations are retained as HOLD, never silently reminted.
func PrepareProjectTest(ctx context.Context, bootstrapID, repo, commit, pkg string) (Manifest, error) {
	if !packageSnapshotPattern.MatchString(pkg) {
		return Manifest{}, Hold(failure.PolicyDenied, "TEST_PACKAGE_INVALID", "snapshot.PrepareProjectTest", "package outside snapshot policy", nil)
	}
	return prepareSnapshotTest(ctx, bootstrapID, repo, commit, pkg, "project-test-v1", "project-test-v1", []string{"go", "test", "-p", "1", "./" + pkg}, 0)
}

func prepareSnapshotTest(ctx context.Context, bootstrapID, repo, commit, pkg, purpose, profile string, command []string, timeoutSeconds int64, extend ...func(*RunSpec)) (Manifest, error) {
	return prepareSealedTestWith(ctx, bootstrapID, repo, commit, pkg, purpose, profile, command, timeoutSeconds, extend, func(ctx context.Context) (func(string) (string, error), error) {
		list, e := projectTestTree(ctx, repo, commit, pkg)
		if e != nil {
			return nil, e
		}
		return func(base string) (string, error) { return materializeProjectTestSource(ctx, repo, pkg, base, list) }, nil
	})
}

// prepareSealedTest seals one permit around a root-built workspace. stage runs
// before the bootstrap claim and returns the writer for the private workspace;
// everything else (identity, replay, ownership, manifest) is shared by every
// integration evidence kind.
func prepareSealedTest(ctx context.Context, bootstrapID, repo, commit, pkg, purpose, profile string, command []string, timeoutSeconds int64, stage func(context.Context) (func(string) (string, error), error)) (Manifest, error) {
	return prepareSealedTestWith(ctx, bootstrapID, repo, commit, pkg, purpose, profile, command, timeoutSeconds, nil, stage)
}

// preparablePackage admits every evidence kind; GitVerse's ci:gitverse was
// missing, so GitVerse evidence never prepared (BUG-20261023-73).
func preparablePackage(pkg string) bool {
	return packageSnapshotPattern.MatchString(pkg) || pkg == recipeTestPackage || pkg == treeTestPackage || moduleSnapshot(pkg) || ciEvidencePackageValid(pkg)
}

// prepareSealedTestWith lets a caller add run-spec fields (runtime binds,
// environment) that are sealed into the manifest with everything else.
func prepareSealedTestWith(ctx context.Context, bootstrapID, repo, commit, pkg, purpose, profile string, command []string, timeoutSeconds int64, extend []func(*RunSpec), stage func(context.Context) (func(string) (string, error), error)) (Manifest, error) {
	if os.Geteuid() != 0 || !idPattern.MatchString(bootstrapID) || !commitPattern.MatchString(commit) || !preparablePackage(pkg) || filepath.Clean(repo) != repo || !strings.HasPrefix(repo, "/root/repos/") {
		return Manifest{}, Hold(failure.PolicyDenied, "PREPARE_IDENTITY_INVALID", "snapshot.prepareSealedTestWith", "prepare: invalid identity, repository path or privilege", nil)
	}
	if (RunSpec{Profile: profile, Command: command, WorkspaceDevice: 1, WorkspaceInode: 1, TimeoutSeconds: timeoutSeconds}).validate() != nil {
		return Manifest{}, Hold(failure.PolicyDenied, "RUN_SPEC_INVALID", "snapshot.prepareSealedTestWith", "prepare: invalid run specification", nil)
	}
	var fs syscall.Statfs_t
	if syscall.Statfs(AuthorityRoot, &fs) != nil {
		return Manifest{}, Hold(failure.Transient, "DISK_STATS_UNAVAILABLE", "snapshot.prepareSealedTestWith", "disk reserve: filesystem statistics unavailable", nil)
	}
	if err := checkSnapshotDisk(fs.Blocks*uint64(fs.Bsize), fs.Bavail*uint64(fs.Bsize), pkg); err != nil {
		return Manifest{}, err
	}
	resolved, e := filepath.EvalSymlinks(repo)
	if e != nil || resolved != repo {
		return Manifest{}, Hold(failure.PolicyDenied, "REPOSITORY_NOT_CANONICAL", "snapshot.prepareSealedTestWith", "prepare: repository path is not canonical", nil)
	}
	c, e := OpenAuthority()
	if e != nil {
		return Manifest{}, e
	}
	p, launch := snapshotIdentity(purpose, bootstrapID)
	if m, e := readManifest(p.PermitID); e == nil {
		if m.Purpose != purpose || m.Pin != p || m.Source == nil || m.Source.Repo != repo || m.Source.Commit != commit || m.Source.Package != pkg || m.Runs[launch].TimeoutSeconds != timeoutSeconds {
			return Manifest{}, Hold(failure.FencedUnknown, "MANIFEST_IDENTITY_MISMATCH", "snapshot.prepareSealedTestWith", "prepare: manifest identity mismatch", nil)
		}
		if _, e = c.Inspect(ctx, p); e != nil {
			return Manifest{}, e
		}
		return m, nil
	}
	kind, e := gitSnapshotRead(ctx, repo, "cat-file", "-t", commit)
	if e != nil || string(kind) != "commit\n" {
		return Manifest{}, Hold(failure.EvidenceMissing, "SOURCE_COMMIT_UNAVAILABLE", "snapshot.prepareSealedTestWith", "prepare: source commit unavailable", nil)
	}
	materialize, e := stage(ctx)
	if e != nil {
		return Manifest{}, e
	}
	claimFields := map[string]string{"permit_id": p.PermitID, "repo": repo, "commit": commit, "package": pkg}
	if timeoutSeconds != 0 {
		claimFields["timeout_seconds"] = strconv.FormatInt(timeoutSeconds, 10)
	}
	claim, _ := json.Marshal(claimFields)
	ok, e := c.db.PutIfAbsent("executor/bootstrap/"+bootstrapID+".json", claim)
	if e != nil {
		return Manifest{}, e
	}
	if !ok {
		return Manifest{}, Hold(failure.FencedUnknown, "BOOTSTRAP_ALREADY_CLAIMED", "snapshot.prepareSealedTestWith", "prepare: bootstrap already claimed; retry with a new attempt", nil)
	}
	base := filepath.Join(AuthorityRoot, "workspaces", p.PermitID)
	if e = os.MkdirAll(filepath.Dir(base), 0700); e != nil {
		return Manifest{}, e
	}
	if e = os.Mkdir(base, 0700); e != nil {
		return Manifest{}, e
	}
	digest, e := materialize(base)
	if e != nil {
		return Manifest{}, e
	}
	if usesScratch(pkg) {
		if e = prepareModuleScratch(p.PermitID); e != nil {
			return Manifest{}, e
		}
	}
	if e = os.Chown(base, 994, 994); e != nil {
		return Manifest{}, e
	}
	if e = os.Chmod(base, 0555); e != nil {
		return Manifest{}, e
	}
	info, e := os.Stat(base)
	if e != nil {
		return Manifest{}, e
	}
	st := info.Sys().(*syscall.Stat_t)
	spec := RunSpec{Profile: profile, Command: command, TimeoutSeconds: timeoutSeconds, WorkspaceDevice: uint64(st.Dev), WorkspaceInode: st.Ino}
	for _, f := range extend {
		f(&spec)
	}
	if spec.validate() != nil {
		return Manifest{}, Hold(failure.PolicyDenied, "SEALED_RUN_INVALID", "snapshot.prepareSealedTestWith", "prepare: sealed run specification invalid", nil)
	}
	m := Manifest{Purpose: purpose, Pin: p, Source: &SourceSnapshot{Repo: repo, Commit: commit, Package: pkg, Digest: digest}, Runs: map[string]RunSpec{launch: spec}}
	if purpose == LocalReleasePurpose {
		tree, err := gitSnapshotRead(ctx, repo, "rev-parse", "--verify", commit+"^{tree}")
		if err != nil || !commitPattern.MatchString(strings.TrimSpace(string(tree))) {
			return Manifest{}, Hold(failure.EvidenceMissing, "RELEASE_TREE_UNAVAILABLE", "snapshot.prepareSealedTestWith", "prepare: release tree identity unavailable", nil)
		}
		sourceDigest, err := releaseSourceDigest(base)
		if err != nil {
			return Manifest{}, err
		}
		m.Source.Tree = strings.TrimSpace(string(tree))
		m.Source.ReleaseDigest = sourceDigest
	}
	if e = c.Create(p); e != nil {
		return Manifest{}, e
	}
	b, _ := json.Marshal(m)
	f, e := os.OpenFile(filepath.Join(AuthorityRoot, p.PermitID+".manifest.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return Manifest{}, e
	}
	_, e = f.Write(b)
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return Manifest{}, e
	}
	if ce != nil {
		return Manifest{}, ce
	}
	d, e := os.Open(AuthorityRoot)
	if e != nil {
		return Manifest{}, e
	}
	defer d.Close()
	if e = d.Sync(); e != nil {
		return Manifest{}, e
	}
	return m, nil
}

func snapshotGitCredential(account *user.User, lookupErr error) (*syscall.Credential, error) {
	if lookupErr != nil || account == nil || account.Uid != "994" {
		return nil, Hold(failure.PolicyDenied, "GIT_ACCOUNT_INVALID", "snapshot.snapshotGitCredential", "Git service account unavailable or unexpected", lookupErr)
	}
	gid, err := strconv.ParseUint(account.Gid, 10, 32)
	if err != nil || gid == 0 || gid == uint64(^uint32(0)) {
		return nil, Hold(failure.PolicyDenied, "GIT_GROUP_INVALID", "snapshot.snapshotGitCredential", "Git service group invalid", err)
	}
	return &syscall.Credential{Uid: 994, Gid: uint32(gid), NoSetGroups: false}, nil
}

// snapshotIdentity derives the permit pin and launch from purpose and
// bootstrap only, so observers can name a preparation without replaying it.
func snapshotIdentity(purpose, bootstrapID string) (Pin, string) {
	derive := func(label string) string {
		sum := sha256.Sum256([]byte(purpose + ":" + bootstrapID + ":" + label))
		return hex.EncodeToString(sum[:16])
	}
	return Pin{PermitID: derive("permit"), LeaseID: derive("lease"), Generation: 1, ExecutionID: derive("execution")}, derive("launch")
}

// usesScratch marks snapshots whose suites build: Go needs a disk-backed
// cache and temp area, a tmpfs inside the memory cgroup cannot hold it.
func usesScratch(pkg string) bool {
	return moduleSnapshot(pkg) || pkg == recipeTestPackage || pkg == treeTestPackage
}

func checkSnapshotDisk(total, available uint64, pkg string) error {
	reserve := uint64(3) << 30
	tenPercent := total / 10
	if tenPercent > reserve {
		reserve = tenPercent
	}
	need := uint64(128 << 20)
	if moduleSnapshot(pkg) || pkg == treeTestPackage {
		need = moduleScratchFree + moduleMaxBytes
	}
	if pkg == recipeTestPackage {
		need = moduleScratchFree + 64<<20
	}
	if available < reserve+need {
		return fmt.Errorf("%w: disk reserve: need %d bytes free, have %d (reserve %d + scratch/source %d)", Hold(failure.PolicyDenied, "DISK_RESERVE_INSUFFICIENT", "snapshot.checkSnapshotDisk", "insufficient disk reserve", nil), reserve+need, available, reserve, need)
	}
	return nil
}
