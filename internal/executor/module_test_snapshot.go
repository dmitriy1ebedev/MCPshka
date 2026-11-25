package executor

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"mcpshka/internal/distributedci"
	"mcpshka/internal/failure"
	"mcpshka/internal/gomodcache"
	"mcpshka/internal/snapshotfiles"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The whole-module profile is the integration test gate: `go test ./...` over
// the committed module, offline, with dependencies copied by root from its
// already verified module cache. Missing modules are never fetched.
const (
	moduleTestPackage = "module:all"
	ModuleTestPurpose = "project-module-test-v1"
	ModuleTestProfile = "module-test-v1"
	// Root's Go module cache, populated only by root builds verified against
	// the checksum database. The prepare unit binds it read-only.
	ModuleCacheRoot = gomodcache.Root
	moduleDepsDir   = ".gomodcache"
	// Disk-backed per-permit GOCACHE/TMPDIR; tmpfs would spend host RAM.
	ModuleScratchRoot = AuthorityRoot + "/scratch"
	// PID1 binds the permit scratch here, like the workspace: the guest UID
	// cannot traverse the root-only authority directory to reach it.
	ScratchMountPath  = "/run/mcpshka-executor-scratch"
	moduleMaxFiles    = 30000
	moduleMaxBytes    = 512 << 20
	moduleScratchFree = 2 << 30
)

// moduleCacheSource is the fixed root cache; tests point it at a fixture.
var moduleCacheSource = ModuleCacheRoot

var moduleTestCommand = []string{"go", "test", "-p", "1", "-count=1", "-timeout=45m", "./..."} // go's 10m default killed mcpserver on H96

// The affected profile (ROADMAP S5.FAST.3) runs the integration gate over the
// packages a merge touches. Root seals the list into the run environment;
// the release receipt refuses it, a release always needs module:all.
const moduleAffectedPackage = "module:affected"

func moduleAffectedRun(s RunSpec) bool {
	return s.Profile == ModuleTestProfile && distributedci.AffectedRun(s.Command, s.Env) && len(s.Runtime) == 0 && s.EnvBuild == nil
}

// moduleGateRun admits the two integration-gate forms of the module purpose.
func moduleGateRun(m Manifest, s RunSpec) bool {
	return s.Profile == ModuleTestProfile && m.Source != nil &&
		(m.Source.Package == moduleTestPackage && moduleTestCommandMatches(s.Command) && len(s.Env) == 0 ||
			m.Source.Package == moduleAffectedPackage && moduleAffectedRun(s))
}

// PrepareModuleGate seals the integration gate: the affected packages when
// the merge allows it, otherwise the full module suite.
func PrepareModuleGate(ctx context.Context, bootstrapID, repo, commit string) (Manifest, error) {
	if pkgs := distributedci.AffectedOrFull(ctx, repo, commit); pkgs != nil {
		return PrepareModuleTestAffected(ctx, bootstrapID, repo, commit, pkgs)
	}
	return PrepareModuleTest(ctx, bootstrapID, repo, commit)
}

// PrepareModuleTestAffected seals the integration gate over pkgs only.
func PrepareModuleTestAffected(ctx context.Context, bootstrapID, repo, commit string, pkgs []string) (Manifest, error) {
	list := strings.Join(pkgs, " ")
	if !distributedci.ValidAffectedPackages(list) {
		return Manifest{}, Hold(failure.PolicyDenied, "AFFECTED_PACKAGES_INVALID", "snapshot.PrepareModuleTestAffected", "affected package list invalid", nil)
	}
	return prepareSnapshotTest(ctx, bootstrapID, repo, commit, moduleAffectedPackage, ModuleTestPurpose, ModuleTestProfile, append([]string(nil), distributedci.AffectedCommand...), 0, func(s *RunSpec) {
		s.Env = map[string]string{distributedci.AffectedEnv: list}
	})
}

// PrepareModuleTest seals committed module inputs for the integration gate.
// It never runs Go tooling on the source; the sandboxed worker does.
func PrepareModuleTest(ctx context.Context, bootstrapID, repo, commit string) (Manifest, error) {
	return prepareSnapshotTest(ctx, bootstrapID, repo, commit, moduleTestPackage, ModuleTestPurpose, ModuleTestProfile, append([]string(nil), moduleTestCommand...), 0)
}

func moduleTestPath(name string) bool {
	return snapshotfiles.ModulePath(name, moduleDepsDir)
}

func moduleTestCommandMatches(command []string) bool {
	return strings.Join(command, "\x00") == strings.Join(moduleTestCommand, "\x00")
}

// snapshotLimits keeps the historical single-package bounds unchanged.
func snapshotLimits(pkg string) (int, int64) {
	if moduleSnapshot(pkg) {
		return moduleMaxFiles, moduleMaxBytes
	}
	if ciEvidencePackageValid(pkg) {
		return 2, ciEvidenceMaxBytes
	}
	return 10000, 64 << 20
}

// goSumModules reads only the committed go.sum already materialized in base.
func goSumModules(base *os.Root) ([]gomodcache.Ref, error) {
	f, err := base.Open("go.sum")
	if err != nil {
		return nil, ErrHold
	}
	defer f.Close()
	refs, err := gomodcache.ParseSum(f)
	if err != nil {
		return nil, ErrHold
	}
	return refs, nil
}

type moduleCopyBudget struct {
	files int
	bytes int64
}

func (b *moduleCopyBudget) take(size int64) error {
	b.files++
	b.bytes += size
	if b.files > moduleMaxFiles || b.bytes > moduleMaxBytes {
		return ErrHold
	}
	return nil
}

// copyModuleDeps copies the go.sum-listed modules that exist in the trusted
// cache into base/.gomodcache. Absent entries are skipped: a build that needs
// them fails inside the sandbox, which is an honest test failure.
func copyModuleDeps(ctx context.Context, base *os.Root, cacheRoot string) error {
	refs, err := goSumModules(base)
	if err != nil {
		return err
	}
	cache, err := os.OpenRoot(cacheRoot)
	if err != nil {
		return ErrHold
	}
	defer cache.Close()
	budget := &moduleCopyBudget{}
	for _, ref := range refs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		esc := gomodcache.Escape(ref.Path)
		download := path.Join("cache", "download", esc, "@v")
		for _, ext := range []string{".mod", ".info", ".ziphash"} {
			if err := copyModuleFile(cache, base, path.Join(download, ref.Version+ext), budget); err != nil {
				return err
			}
		}
		if err := copyModuleTree(ctx, cache, base, esc+"@"+ref.Version, budget); err != nil {
			return err
		}
	}
	return nil
}

func copyModuleFile(cache, base *os.Root, name string, budget *moduleCopyBudget) error {
	info, err := cache.Lstat(name)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return ErrHold
	}
	if err := budget.take(info.Size()); err != nil {
		return err
	}
	src, err := cache.Open(name)
	if err != nil {
		return ErrHold
	}
	defer src.Close()
	dest := path.Join(moduleDepsDir, name)
	if err := base.MkdirAll(path.Dir(dest), 0755); err != nil {
		return err
	}
	out, err := base.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0444)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(src, info.Size()+1))
	if err == nil && n != info.Size() {
		err = ErrHold
	}
	if err == nil {
		err = out.Chmod(0444)
	}
	if err == nil {
		err = out.Sync()
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	return err
}

func copyModuleTree(ctx context.Context, cache, base *os.Root, dir string, budget *moduleCopyBudget) error {
	info, err := cache.Lstat(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !info.IsDir() {
		return ErrHold
	}
	return fs.WalkDir(cache.FS(), dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil {
			return ErrHold
		}
		if d.Type()&fs.ModeSymlink != 0 || d.Name() == ".git" {
			return ErrHold
		}
		if d.IsDir() {
			return base.MkdirAll(path.Join(moduleDepsDir, p), 0755)
		}
		if !d.Type().IsRegular() {
			return ErrHold
		}
		return copyModuleFile(cache, base, p, budget)
	})
}

// sealModuleDeps makes copied directories read-only for the guest UID.
func sealModuleDeps(base *os.Root) error {
	return fs.WalkDir(base.FS(), moduleDepsDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return ErrHold
		}
		if !d.IsDir() {
			return nil
		}
		dir, err := base.Open(p)
		if err != nil {
			return err
		}
		err = dir.Chmod(0555)
		if closeErr := dir.Close(); err == nil {
			err = closeErr
		}
		return err
	})
}

// prepareModuleScratch creates the disk-backed writable area for one permit.
// Only the guest UID may write it; the worker empties it after the run.
func prepareModuleScratch(permit string) error {
	var st syscall.Statfs_t
	if syscall.Statfs(AuthorityRoot, &st) != nil {
		return fmt.Errorf("%w: scratch: filesystem statistics unavailable", ErrHold)
	}
	if available := st.Bavail * uint64(st.Bsize); available < moduleScratchFree {
		return fmt.Errorf("%w: scratch: need %d bytes free, have %d", ErrHold, uint64(moduleScratchFree), available)
	}
	if err := os.MkdirAll(ModuleScratchRoot, 0700); err != nil {
		return err
	}
	dir := filepath.Join(ModuleScratchRoot, permit)
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	}
	for _, sub := range []string{"tmp", "go-cache"} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0700); err != nil {
			return err
		}
		if err := os.Chown(filepath.Join(dir, sub), 994, 994); err != nil {
			return err
		}
	}
	return os.Chown(dir, 994, 994)
}

// openModuleScratch pins the permit scratch the guest may write: the unit's
// fixed bind of a real directory owned by the guest UID, never a symlink.
func openModuleScratch(permit string) (*os.File, error) {
	if !idPattern.MatchString(permit) {
		return nil, fmt.Errorf("%w: scratch: invalid permit identity", ErrHold)
	}
	fd, err := syscall.Open(ScratchMountPath, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: scratch: directory unavailable", ErrHold)
	}
	var st syscall.Stat_t
	if syscall.Fstat(fd, &st) != nil || st.Uid != 994 || st.Gid != 994 || st.Mode&0777 != 0700 {
		syscall.Close(fd)
		return nil, fmt.Errorf("%w: scratch: ownership or mode mismatch", ErrHold)
	}
	return os.NewFile(uintptr(fd), "module-scratch"), nil
}

// clearModuleScratch empties the scratch through os.Root, so guest-created
// symlinks cannot redirect removal outside it. Failure leaves files for cleanup.
func clearModuleScratch() {
	root, err := os.OpenRoot(ScratchMountPath)
	if err != nil {
		return
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return
	}
	for _, e := range entries {
		_ = root.RemoveAll(e.Name())
	}
}

// ModuleLaunchAdmission serializes whole-module tests and refuses to start
// one on a host that is already saturated (H96 runs production services).
// A non-empty go-cache in another permit's scratch means a build is running;
// entries untouched for longer than the unit deadline are stale leftovers.
func ModuleLaunchAdmission(permit string) error {
	if !idPattern.MatchString(permit) {
		return fmt.Errorf("%w: admission: invalid permit identity", ErrHold)
	}
	if err := moduleScratchBusy(ModuleScratchRoot, permit, time.Now()); err != nil {
		return err
	}
	load, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return fmt.Errorf("%w: load: reading unavailable", ErrHold)
	}
	fields := strings.Fields(string(load))
	if len(fields) == 0 {
		return fmt.Errorf("%w: load: empty reading", ErrHold)
	}
	one, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return fmt.Errorf("%w: load: invalid reading", ErrHold)
	}
	if one > moduleMaxLoad {
		return fmt.Errorf("%w: load %.2f exceeds limit %.2f", errModuleBusy, one, moduleMaxLoad)
	}
	available, err := memAvailable()
	if err != nil {
		return fmt.Errorf("%w: memory: reading unavailable", ErrHold)
	}
	if available < moduleMinAvailable {
		return fmt.Errorf("%w: memory: need %d bytes available, have %d", errModuleBusy, uint64(moduleMinAvailable), available)
	}
	return nil
}

func moduleScratchBusy(root, permit string, now time.Time) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("%w: scratch: cannot inspect active workspaces", ErrHold)
	}
	for _, e := range entries {
		if e.Name() == permit || !e.IsDir() {
			continue
		}
		cache := filepath.Join(root, e.Name(), "go-cache")
		info, err := os.Lstat(cache)
		if err != nil || !info.IsDir() || now.Sub(info.ModTime()) > 2*time.Hour {
			continue
		}
		if inner, err := os.ReadDir(cache); err == nil && len(inner) > 0 {
			return fmt.Errorf("%w: scratch: another module workspace is busy", errModuleBusy)
		}
	}
	return nil
}

const (
	moduleMaxLoad      = 8.0
	moduleMinAvailable = 700 << 20
)

var errModuleBusy = fmt.Errorf("%w: host busy; retry later", ErrHold)

func memAvailable() (uint64, error) {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if rest, ok := strings.CutPrefix(line, "MemAvailable:"); ok {
			kb, err := strconv.ParseUint(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), "kB")), 10, 64)
			return kb << 10, err
		}
	}
	return 0, ErrHold
}

// ModuleTestIdentity names an asynchronous preparation without replaying it.
func ModuleTestIdentity(bootstrapID string) (Pin, string, error) {
	if !idPattern.MatchString(bootstrapID) {
		return Pin{}, "", ErrHold
	}
	p, launch := snapshotIdentity(ModuleTestPurpose, bootstrapID)
	return p, launch, nil
}
