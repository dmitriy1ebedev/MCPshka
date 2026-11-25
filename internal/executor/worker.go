package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mcpshka/internal/budget"
	"mcpshka/internal/sandboxbind"
	"mcpshka/internal/unitfence"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mcpshka/internal/failure"
	"mcpshka/internal/heavyqueue"
	"mcpshka/internal/projectprofile"
)

// WorkspaceMountPath is a fixed per-unit namespace mount prepared by PID 1.
const WorkspaceMountPath = "/run/mcpshka-executor-workspace"

type StageSpec struct {
	Stage     string   `json:"stage,omitempty"`
	Artifacts []string `json:"artifacts,omitempty"`
}

type RunSpec struct {
	StageSpec
	TimeoutSeconds  int64    `json:"timeout_seconds,omitempty"`
	Profile         string   `json:"profile"`
	Command         []string `json:"command"`
	WorkspaceDevice uint64   `json:"workspace_device"`
	WorkspaceInode  uint64   `json:"workspace_inode"`
	// Runtime and Env carry a registered suite's out-of-commit dependencies
	// (resolved, allowlisted, read-only) and environment; recipe profile only.
	Runtime []RuntimeBind     `json:"runtime,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	// EnvBuild is the project's dependency environment, built by the worker
	// (networked sandbox) when not cached and mounted read-only at /env.
	EnvBuild *EnvSpec `json:"env_build,omitempty"`
}
type Manifest struct {
	Purpose string             `json:"purpose"`
	Pin     Pin                `json:"pin"`
	Runs    map[string]RunSpec `json:"runs"`
	Source  *SourceSnapshot    `json:"source,omitempty"`
}

var pkgPattern = regexp.MustCompile("^\\./internal/[a-z][a-z0-9]*(/\\.\\.\\.)?$")

func (s RunSpec) validate() error {
	if (s.Stage != "" && (s.Profile != "recipe-test-v1" || !projectprofile.ValidStage(s.Stage))) || projectprofile.ValidateArtifacts(s.Artifacts) != nil || (len(s.Artifacts) > 0 && s.Stage != "package") {
		return ErrHold
	}
	minArgs := 2
	if s.Profile == "recipe-test-v1" {
		minArgs = 1 // a registered argv may be a single committed script
	}
	if (s.Profile != "isolated-v1" && s.Profile != "project-test-v1" && s.Profile != "recipe-test-v1" && s.Profile != ModuleTestProfile && s.Profile != CIEvidenceProfile && s.Profile != LocalReleaseProfile) || s.WorkspaceDevice == 0 || s.WorkspaceInode == 0 || len(s.Command) < minArgs || len(s.Command) > 12 {
		return Hold(failure.PolicyDenied, "RUN_SPEC_INVALID", "executor.RunSpec.validate", "run profile workspace identity or command length invalid", nil)
	}
	if (len(s.Runtime) > 0 || len(s.Env) > 0 || s.EnvBuild != nil) && !moduleAffectedRun(s) && (s.Profile != "recipe-test-v1" || !sandboxbind.ValidBinds(s.Runtime) || !sandboxbind.ValidEnv(s.Env) || !s.EnvBuild.Valid()) {
		return Hold(failure.PolicyDenied, "RUN_RUNTIME_INVALID", "executor.RunSpec.validate", "runtime bindings environment or build specification invalid", nil)
	}
	total := 0
	for _, a := range s.Command {
		total += len(a)
		if strings.ContainsRune(a, 0) {
			return Hold(failure.PolicyDenied, "RUN_ARGUMENT_NUL", "executor.RunSpec.validate", "command argument contains NUL", nil)
		}
	}
	if total > 16384 {
		return Hold(failure.PolicyDenied, "RUN_ARGUMENT_LIMIT", "executor.RunSpec.validate", "command exceeds byte limit", nil)
	}
	if s.Profile != "recipe-test-v1" && s.TimeoutSeconds != 0 {
		return Hold(failure.PolicyDenied, "RUN_TIMEOUT_UNSUPPORTED", "executor.RunSpec.validate", "timeout only supported by recipe profile", nil)
	}
	if s.Profile == "recipe-test-v1" {
		if s.TimeoutSeconds < 1 || s.TimeoutSeconds > budget.CeilingSeconds {
			return Hold(failure.PolicyDenied, "RUN_TIMEOUT_INVALID", "executor.RunSpec.validate", "recipe timeout outside 1 s..the flexible-budget ceiling (6 h)", nil)
		}
		if ValidTreeCommand(s.Command) {
			return nil
		}
		return Hold(failure.PolicyDenied, "RUN_RECIPE_COMMAND_INVALID", "executor.RunSpec.validate", "recipe command outside sealed boundary", nil)
	}
	if s.Profile == LocalReleaseProfile {
		if localReleaseCommandMatches(s.Command) {
			return nil
		}
		return Hold(failure.PolicyDenied, "RUN_RELEASE_COMMAND_INVALID", "executor.RunSpec.validate", "release command outside sealed boundary", nil)
	}
	if s.Profile == ModuleTestProfile {
		if (moduleTestCommandMatches(s.Command) || distributedShardCommandMatches(s.Command)) && len(s.Env) == 0 || moduleAffectedRun(s) {
			return nil
		}
		return Hold(failure.PolicyDenied, "RUN_MODULE_COMMAND_INVALID", "executor.RunSpec.validate", "module command outside sealed boundary", nil)
	}
	if s.Profile == CIEvidenceProfile {
		if ciEvidenceCommandMatches(s.Command) {
			return nil
		}
		return Hold(failure.PolicyDenied, "RUN_CI_COMMAND_INVALID", "executor.RunSpec.validate", "CI command outside sealed boundary", nil)
	}
	if s.Profile == "isolated-v1" && len(s.Command) == 4 && s.Command[0] == "python3" && s.Command[1] == "-I" && s.Command[2] == "-c" {
		return nil
	}
	if len(s.Command) >= 5 && s.Command[0] == "go" && s.Command[1] == "test" && s.Command[2] == "-p" && s.Command[3] == "1" {
		for _, p := range s.Command[4:] {
			if !pkgPattern.MatchString(p) {
				return Hold(failure.PolicyDenied, "RUN_PACKAGE_INVALID", "executor.RunSpec.validate", "Go test package invalid", nil)
			}
		}
		return nil
	}
	return Hold(failure.PolicyDenied, "RUN_COMMAND_UNSUPPORTED", "executor.RunSpec.validate", "command outside sealed profile boundary", nil)
}
func readManifest(id string) (Manifest, error) {
	if os.Geteuid() != 0 || !idPattern.MatchString(id) {
		return Manifest{}, Hold(failure.PolicyDenied, "MANIFEST_ACCESS_INVALID", "executor.readManifest", "manifest read requires root and valid permit identity", nil)
	}
	fd, e := syscall.Open(filepath.Join(AuthorityRoot, id+".manifest.json"), syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if e != nil {
		return Manifest{}, Hold(failure.EvidenceMissing, "MANIFEST_OPEN_FAILED", "executor.readManifest", "sealed manifest unavailable", e)
	}
	f := os.NewFile(uintptr(fd), "executor-manifest")
	defer f.Close()
	var st syscall.Stat_t
	if e := syscall.Fstat(fd, &st); e != nil || st.Uid != 0 || st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Mode&0077 != 0 || st.Nlink != 1 {
		return Manifest{}, Hold(failure.FencedUnknown, "MANIFEST_METADATA_UNTRUSTED", "executor.readManifest", "manifest metadata does not establish trusted ownership", e)
	}
	b, e := io.ReadAll(io.LimitReader(f, 65537))
	if e != nil || len(b) > 65536 {
		return Manifest{}, Hold(failure.EvidenceMissing, "MANIFEST_READ_FAILED", "executor.readManifest", "manifest unreadable or exceeds byte limit", e)
	}
	var m Manifest
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if e := d.Decode(&m); e != nil || d.Decode(new(any)) != io.EOF || !validPin(m.Pin) || m.Pin.PermitID != id || (m.Purpose != "canary-v1" && m.Purpose != "project-test-v1" && m.Purpose != "project-recipe-test-v1" && m.Purpose != ModuleTestPurpose && m.Purpose != DistributedShardPurpose && m.Purpose != CIEvidencePurpose && m.Purpose != LocalReleasePurpose) || len(m.Runs) == 0 || len(m.Runs) > 32 {
		return Manifest{}, Hold(failure.FencedUnknown, "MANIFEST_INVALID", "executor.readManifest", "manifest encoding identity purpose or runs invalid", e)
	}
	if (m.Purpose == "project-test-v1" || m.Purpose == "project-recipe-test-v1" || m.Purpose == ModuleTestPurpose || m.Purpose == DistributedShardPurpose || m.Purpose == LocalReleasePurpose || m.Purpose == CIEvidencePurpose) && (!validSource(m.Source) || len(m.Runs) != 1) {
		return Manifest{}, Hold(failure.FencedUnknown, "MANIFEST_SOURCE_INVALID", "executor.readManifest", "manifest source identity or run count invalid", nil)
	}
	if m.Purpose == "project-test-v1" && !packageSnapshotPattern.MatchString(m.Source.Package) {
		return Manifest{}, Hold(failure.FencedUnknown, "MANIFEST_TEST_PACKAGE_INVALID", "executor.readManifest", "manifest test package invalid", nil)
	}
	if m.Purpose == "project-recipe-test-v1" && m.Source.Package != recipeTestPackage && m.Source.Package != treeTestPackage {
		return Manifest{}, Hold(failure.FencedUnknown, "MANIFEST_RECIPE_PACKAGE_INVALID", "executor.readManifest", "manifest recipe package invalid", nil)
	}
	if m.Purpose == DistributedShardPurpose && !shardPackageValid(m.Source.Package) {
		return Manifest{}, ErrHold
	}
	if m.Purpose == ModuleTestPurpose && m.Source.Package != moduleTestPackage && m.Source.Package != moduleAffectedPackage {
		return Manifest{}, Hold(failure.FencedUnknown, "MANIFEST_MODULE_PACKAGE_INVALID", "executor.readManifest", "manifest module package invalid", nil)
	}
	if m.Purpose == CIEvidencePurpose && !ciEvidencePackageValid(m.Source.Package) {
		return Manifest{}, Hold(failure.FencedUnknown, "MANIFEST_CI_PACKAGE_INVALID", "executor.readManifest", "manifest CI package invalid", nil)
	}
	if m.Purpose == "canary-v1" && m.Source != nil {
		return Manifest{}, Hold(failure.FencedUnknown, "MANIFEST_CANARY_SOURCE_INVALID", "executor.readManifest", "canary manifest must not carry a project source", nil)
	}
	if m.Purpose == LocalReleasePurpose && (m.Source.Package != localReleasePackage || !commitPattern.MatchString(m.Source.Tree) || !digestPattern.MatchString(m.Source.ReleaseDigest)) {
		return Manifest{}, Hold(failure.FencedUnknown, "MANIFEST_RELEASE_SOURCE_INVALID", "executor.readManifest", "release manifest package tree or digest invalid", nil)
	}
	for id, s := range m.Runs {
		if m.Purpose == DistributedShardPurpose && (s.Profile != ModuleTestProfile || !distributedShardCommandMatches(s.Command) || s.Command[5] != m.Source.Commit || m.Source.Package != "module:shard-"+s.Command[3]) {
			return Manifest{}, ErrHold
		}
		if (m.Purpose == LocalReleasePurpose) != (s.Profile == LocalReleaseProfile) || (m.Purpose == LocalReleasePurpose && !localReleaseCommandMatches(s.Command)) {
			return Manifest{}, Hold(failure.FencedUnknown, "MANIFEST_RELEASE_RUN_INVALID", "executor.readManifest", "release run does not match purpose", nil)
		}
		if m.Purpose == "project-test-v1" && (s.Profile != "project-test-v1" || strings.Join(s.Command, " ") != "go test -p 1 ./"+m.Source.Package) {
			return Manifest{}, Hold(failure.FencedUnknown, "MANIFEST_TEST_RUN_INVALID", "executor.readManifest", "test run does not match sealed source", nil)
		}
		if m.Purpose == ModuleTestPurpose && !moduleGateRun(m, s) {
			return Manifest{}, Hold(failure.FencedUnknown, "MANIFEST_MODULE_RUN_INVALID", "executor.readManifest", "module run does not match sealed purpose", nil)
		}
		if m.Purpose == CIEvidencePurpose && (s.Profile != CIEvidenceProfile || !ciEvidenceCommandMatches(s.Command)) {
			return Manifest{}, Hold(failure.FencedUnknown, "MANIFEST_CI_RUN_INVALID", "executor.readManifest", "CI run does not match sealed purpose", nil)
		}
		if m.Purpose == "project-recipe-test-v1" && (s.Profile != "recipe-test-v1" || !RecipeCommandMatches(m.Source.Package, s.Command)) {
			return Manifest{}, Hold(failure.FencedUnknown, "MANIFEST_RECIPE_RUN_INVALID", "executor.readManifest", "recipe run does not match sealed source", nil)
		}
		if m.Purpose != CIEvidencePurpose && s.Profile == CIEvidenceProfile {
			return Manifest{}, Hold(failure.FencedUnknown, "MANIFEST_CI_PROFILE_INVALID", "executor.readManifest", "CI profile does not match manifest purpose", nil)
		}
		if m.Purpose == "canary-v1" && s.Profile != "isolated-v1" {
			return Manifest{}, Hold(failure.FencedUnknown, "MANIFEST_CANARY_PROFILE_INVALID", "executor.readManifest", "canary profile does not match manifest purpose", nil)
		}
		if !idPattern.MatchString(id) {
			return Manifest{}, Hold(failure.FencedUnknown, "MANIFEST_RUN_INVALID", "executor.readManifest", "manifest run identity or specification invalid", nil)
		}
		if err := s.validate(); err != nil {
			return Manifest{}, err
		}
	}
	return m, nil
}

// suiteRecordMargin leaves time to record a stopped suite's result before
// systemd's RuntimeMaxSec ends the worker unit.
const suiteRecordMargin = 20 * time.Second

// sandboxPath keeps system Go first and adds sbin: kernel labs of projects
// (Lane netns_native_*) run iptables/ipset/ip inside their own user+net
// namespace and failed with FileNotFoundError (MCP-118). The sandbox already
// unshares the network and drops all capabilities, so the tools reach no host
// state; /etc/alternatives is bound read-only so their symlinks resolve.
const sandboxPath = "/usr/bin:/bin:/usr/sbin:/sbin:/usr/local/go/bin"

func workerArgs(_ string, s RunSpec) []string {
	args := []string{"--unshare-all", "--cap-drop", "ALL", "--die-with-parent", "--new-session", "--clearenv", "--ro-bind", "/usr", "/usr", "--symlink", "usr/bin", "/bin", "--symlink", "usr/sbin", "/sbin", "--symlink", "usr/lib", "/lib", "--symlink", "usr/lib64", "/lib64", "--ro-bind", "/etc/ld.so.cache", "/etc/ld.so.cache", "--ro-bind-try", "/etc/alternatives", "/etc/alternatives", "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--tmpfs", "/run", "--bind-fd", "3", "/workspace", "--chdir", "/workspace", "--setenv", "PATH", sandboxPath, "--setenv", "HOME", "/tmp", "--setenv", "LANG", "C", "--setenv", "GOMAXPROCS", "1", "--setenv", "GOTOOLCHAIN", "local", "--setenv", "GOWORK", "off", "--setenv", "GOPROXY", "off", "--setenv", "GOCACHE", "/tmp/go-cache", "--setenv", "GOMODCACHE", "/tmp/go-mod", "--"}
	if s.Profile == "recipe-test-v1" {
		return recipeWorkerArgs(args, s)
	}
	if s.Profile == "project-test-v1" {
		for i, a := range args {
			if a == "--bind-fd" {
				args[i] = "--ro-bind-fd"
			}
		}
		// Kernel tmpfs size and cgroup memory jointly bound all writable guest files.
		for i := 0; i < len(args); i++ {
			if args[i] == "--tmpfs" {
				size := "16777216"
				if args[i+1] == "/tmp" {
					size = "268435456"
				}
				args = append(args[:i], append([]string{"--size", size}, args[i:]...)...)
				i += 3
			}
		}
		args = args[:len(args)-1]
		args = append(args, "--setenv", "GOMEMLIMIT", "96MiB", "--setenv", "GOGC", "30", "--")
	}
	if s.Profile == ModuleTestProfile || s.Profile == LocalReleaseProfile {
		return moduleWorkerArgs(args, s)
	}
	return append(args, s.Command...)
}

// recipeWorkerArgs runs a project's own suite like a module test: read-only
// source, disk-backed scratch (fd 4) for build cache and temp, two CPUs. A
// tmpfs cache inside the 512 MiB cgroup timed DIAG's go test out at 30 min.
func recipeWorkerArgs(base []string, s RunSpec) []string {
	args := make([]string, 0, len(base)+16)
	for i := 0; i < len(base); i++ {
		switch base[i] {
		case "--bind-fd":
			args = append(args, "--ro-bind-fd")
			continue
		case "--tmpfs":
			size := "16777216"
			if base[i+1] == "/tmp" {
				size = "67108864"
			}
			args = append(args, "--size", size, "--tmpfs")
			continue
		case "GOMAXPROCS":
			args = append(args, base[i], "2")
			i++
			continue
		case "GOCACHE":
			args = append(args, base[i], "/scratch/go-cache")
			i++
			continue
		case "GOMODCACHE":
			args = append(args, base[i], "/workspace/"+moduleDepsDir)
			i++
			continue
		case "--":
			continue
		}
		args = append(args, base[i])
	}
	args = append(args, "--bind-fd", "4", "/scratch", "--setenv", "TMPDIR", "/scratch/tmp", "--setenv", "GOTMPDIR", "/scratch/tmp", "--setenv", "GOFLAGS", "-mod=readonly", "--setenv", "GOSUMDB", "off", "--setenv", "GOMEMLIMIT", "1200MiB", "--setenv", "PYTHONDONTWRITEBYTECODE", "1")
	// Descriptors 3 (workspace) and 4 (scratch) precede the runtime binds;
	// the built environment, if any, comes last.
	args = append(args, sandboxbind.SandboxArgs(s.Runtime, s.Env, 5)...)
	if s.EnvBuild != nil {
		args = append(args, "--ro-bind-fd", strconv.Itoa(5+len(s.Runtime)), EnvMount,
			"--setenv", "PATH", EnvMount+"/bin:"+EnvMount+"/node_modules/.bin:"+sandboxPath,
			"--setenv", "VIRTUAL_ENV", EnvMount)
	}
	args = append(args, "--")
	if s.Stage != "" {
		// Only this fixed wrapper runs shell syntax; profile arguments remain positional.
		args = append(args, "/bin/sh", "-ec", `cp -R /workspace /scratch/project; chmod -R u+w /scratch/project; cd /scratch/project; exec "$@"`, "pipeline")
	}
	return append(args, s.Command...)
}

// moduleWorkerArgs keeps the shared namespace flags and changes only what a
// whole-module offline test needs: a read-only source and module cache, a
// disk-backed scratch (fd 4) for build cache and temp files, two CPUs.
func moduleWorkerArgs(base []string, s RunSpec) []string {
	args := make([]string, 0, len(base)+16)
	for i := 0; i < len(base); i++ {
		switch base[i] {
		case "--bind-fd":
			args = append(args, "--ro-bind-fd")
			continue
		case "--tmpfs":
			size := "16777216"
			if base[i+1] == "/tmp" {
				size = "67108864"
			}
			args = append(args, "--size", size, "--tmpfs")
			continue
		case "GOMAXPROCS":
			args = append(args, base[i], "2")
			i++
			continue
		case "GOCACHE":
			args = append(args, base[i], "/scratch/go-cache")
			i++
			continue
		case "GOMODCACHE":
			args = append(args, base[i], "/workspace/"+moduleDepsDir)
			i++
			continue
		case "--":
			continue
		}
		args = append(args, base[i])
	}
	// Go creates GOCACHE; TMPDIR is the scratch root so it exists after cleanup.
	args = append(args, "--bind-fd", "4", "/scratch", "--setenv", "TMPDIR", "/scratch", "--setenv", "GOTMPDIR", "/scratch", "--setenv", "GOFLAGS", "-mod=readonly", "--setenv", "GOSUMDB", "off", "--setenv", "GOMEMLIMIT", "1200MiB", "--")
	if s.Profile == LocalReleaseProfile {
		args = args[:len(args)-1]
		args = append(args, "--setenv", "GOOS", "linux", "--setenv", "GOARCH", "arm64", "--setenv", "CGO_ENABLED", "1", "--setenv", "CC", "gcc", "--")
	}
	return append(args, s.Command...)
}

// Worker runs only as the fixed root-owned helper in its private systemd unit.
// It registers the unit before starting any user code. User code always starts
// as UID/GID 994 in a new bubblewrap namespace, never as host root.
func Worker(ctx context.Context, permitID, launchID string) (retErr error) {
	started := time.Now()
	defer func() { retErr = contextFailure(ctx, "executor.Worker", started, retErr) }()
	if err := ctx.Err(); err != nil {
		return err
	}

	c, e := OpenAuthority()
	if e != nil {
		return e
	}
	m, e := readManifest(permitID)
	if e != nil {
		return e
	}
	spec, ok := m.Runs[launchID]
	if !ok {
		return Hold(failure.PolicyDenied, "WORKER_LAUNCH_ABSENT", "executor.Worker", "launch absent from sealed manifest", nil)
	}
	unit, e := unitfence.UnitEvidence(ctx, launchID)
	if e != nil {
		return e
	}
	props := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(unit), "\n") {
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			return Hold(failure.FencedUnknown, "WORKER_UNIT_PROPERTIES_INVALID", "executor.Worker", "unit properties malformed", nil)
		}
		if _, dup := props[k]; dup {
			return Hold(failure.FencedUnknown, "WORKER_UNIT_PROPERTIES_DUPLICATE", "executor.Worker", "unit properties duplicated", nil)
		}
		props[k] = v
	}
	boot, e := unitfence.ReadSmall("/proc/sys/kernel/random/boot_id")
	if e != nil {
		return e
	}
	ticks, e := unitfence.ProcessTicks(os.Getpid())
	if e != nil {
		return e
	}
	cg := heavyqueue.ExecutorCgroup(launchID)
	info, e := os.Stat("/sys/fs/cgroup" + cg)
	if e != nil {
		return Hold(failure.EvidenceMissing, "WORKER_CGROUP_UNAVAILABLE", "executor.Worker", "worker cgroup unavailable", e)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return Hold(failure.EvidenceMissing, "WORKER_CGROUP_METADATA_MISSING", "executor.Worker", "worker cgroup metadata unavailable", nil)
	}
	identity := Identity{BootID: strings.TrimSpace(string(boot)), InvocationID: props["InvocationID"], Cgroup: cg, CgroupDevice: uint64(st.Dev), CgroupInode: st.Ino, PID: os.Getpid(), StartTicks: ticks}
	if !unitfence.ValidIdentity(identity) || unitfence.VerifyUnit(unit, identity, false) != nil {
		return Hold(failure.FencedUnknown, "WORKER_IDENTITY_UNVERIFIED", "executor.Worker", "worker unit identity unverified", nil)
	}
	var heavy *heavyqueue.Ticket
	if m.Purpose != "canary-v1" && m.Purpose != CIEvidencePurpose {
		if e = c.WithPermitLock(ctx, m.Pin, func() error { return c.Register(ctx, m.Pin, launchID, identity) }); e != nil {
			return e
		}
		queue := heavyqueue.Host()
		queue.Ready = func() (bool, string) {
			if ready, reason := heavyqueue.HostReady(); !ready {
				return false, reason
			}
			available, err := memAvailable()
			if err != nil {
				return false, "memory availability unknown"
			}
			if available < moduleMinAvailable {
				return false, "less than 700 MiB memory available"
			}
			return true, ""
		}
		heavy, e = queue.EnqueueScoped("executor:"+permitID, m.Source.Repo)
		if e != nil {
			return e
		}
		defer heavy.Close()
		if e = heavy.Wait(ctx, nil); e != nil {
			return e
		}
	}
	path := WorkspaceMountPath
	fd, e := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if e != nil {
		return Hold(failure.EvidenceMissing, "WORKER_WORKSPACE_UNAVAILABLE", "executor.Worker", "private workspace unavailable", e)
	}
	workspace := os.NewFile(uintptr(fd), "private-workspace")
	defer workspace.Close()
	if e := syscall.Fstat(fd, st); e != nil || uint64(st.Dev) != spec.WorkspaceDevice || st.Ino != spec.WorkspaceInode || st.Uid != 994 || st.Gid != 994 {
		return Hold(failure.FencedUnknown, "WORKER_WORKSPACE_IDENTITY_MISMATCH", "executor.Worker", "private workspace identity or ownership mismatch", e)
	}
	maxFiles, maxBytes := 10000, int64(64*1024*1024)
	if m.Source != nil {
		maxFiles, maxBytes = snapshotLimits(m.Source.Package)
		if m.Source.Package == treeTestPackage {
			maxFiles, maxBytes = moduleMaxFiles, moduleMaxBytes
		}
	}
	var scratch *os.File
	if m.Purpose == ModuleTestPurpose || m.Purpose == DistributedShardPurpose || m.Purpose == LocalReleasePurpose || m.Purpose == "project-recipe-test-v1" {
		if scratch, e = openModuleScratch(permitID); e != nil {
			return e
		}
		defer scratch.Close()
	}
	// Workspace preparation must omit shared Git metadata and copy writable
	// inodes. Refuse symlinks and hard links even inside the private workspace.
	files := 0
	var bytes int64
	if e = filepath.WalkDir(path, func(p string, d os.DirEntry, e error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		files++
		if files > maxFiles {
			return Hold(failure.PolicyDenied, "WORKER_FILE_LIMIT", "executor.Worker.workspace", "workspace exceeds file count limit", nil)
		}
		if e != nil {
			return Hold(failure.EvidenceMissing, "WORKER_WALK_FAILED", "executor.Worker.workspace", "workspace traversal failed", e)
		}
		if d.Name() == ".git" || d.Type()&os.ModeSymlink != 0 {
			return Hold(failure.PolicyDenied, "WORKER_WORKSPACE_ALIAS", "executor.Worker.workspace", "workspace contains Git metadata or symlink", nil)
		}
		i, e := d.Info()
		if e != nil {
			return Hold(failure.EvidenceMissing, "WORKER_FILE_INFO_MISSING", "executor.Worker.workspace", "workspace file metadata unavailable", e)
		}
		bytes += i.Size()
		if bytes > maxBytes {
			return Hold(failure.PolicyDenied, "WORKER_BYTE_LIMIT", "executor.Worker.workspace", "workspace exceeds byte limit", nil)
		}
		s, ok := i.Sys().(*syscall.Stat_t)
		if !ok {
			return Hold(failure.EvidenceMissing, "WORKER_FILE_METADATA_MISSING", "executor.Worker.workspace", "workspace inode metadata unavailable", nil)
		}
		if !d.IsDir() && (!i.Mode().IsRegular() || s.Nlink != 1) {
			return Hold(failure.PolicyDenied, "WORKER_FILE_TYPE_INVALID", "executor.Worker.workspace", "workspace contains special file or hard link", nil)
		}
		return nil
	}); e != nil {
		return e
	}
	if m.Source != nil {
		digest, e := snapshotDigestLimited(path, maxFiles, maxBytes)
		if e != nil || digest != m.Source.Digest {
			return Hold(failure.FencedUnknown, "WORKER_DIGEST_MISMATCH", "executor.Worker.workspace", "workspace digest changed or unverified", e)
		}
	}
	// Keep launch admission locked through registration and child start. A
	// revoker cannot mark this generation fenced before this process is visible.
	if m.Purpose == CIEvidencePurpose {
		return runCIEvidence(ctx, c, m, launchID, identity, path)
	}
	runtimeFiles, e := sandboxbind.Open(spec.Runtime)
	if e != nil {
		return e
	}
	var out suiteOutput
	registered := heavy != nil
	if spec.EnvBuild != nil {
		// Register first: the environment build is part of this launch, and a
		// failed build must be recorded as its result, not lost.
		if e := c.WithPermitLock(ctx, m.Pin, func() error {
			if registered {
				return nil
			}
			return c.Register(ctx, m.Pin, launchID, identity)
		}); e != nil {
			return e
		}
		registered = true
		envFile, buildErr := ensureProjectEnv(ctx, spec.EnvBuild, workspace, &out)
		if buildErr != nil {
			for _, f := range runtimeFiles {
				f.Close()
			}
			fmt.Fprintf(&out, "\n[mcpshka] environment build failed: %v\n", buildErr)
			result := ExecutionResult{Pin: m.Pin, LaunchID: launchID, Identity: identity, ExitCode: 1, Output: strings.ToValidUTF8(out.String(), "?"), ObservedAt: time.Now().UTC(), State: "EXITED_UNFENCED", Trust: "UNTRUSTED"}
			if e := persistWorkerExit(ctx, func(export context.Context) error {
				return c.WithPermitLock(export, m.Pin, func() error { return c.recordResult(export, result) })
			}); e != nil {
				return e
			}
			<-ctx.Done()
			return ctx.Err()
		}
		runtimeFiles = append(runtimeFiles, envFile)
	}
	defer func() {
		for _, f := range runtimeFiles {
			f.Close()
		}
	}()
	var cmd *exec.Cmd
	// A sealed suite stops itself shortly before the unit's RuntimeMaxSec, so
	// its output and a timeout exit are recorded instead of lost with the unit.
	executionSeconds := spec.TimeoutSeconds
	childCtx, stopChild := ctx, context.CancelFunc(func() {})
	if spec.TimeoutSeconds == 0 && heavy != nil {
		budget := 1780 * time.Second
		if m.Purpose == ModuleTestPurpose || m.Purpose == DistributedShardPurpose || m.Purpose == LocalReleasePurpose {
			budget = 3580 * time.Second
		}
		executionSeconds = int64(budget / time.Second)
		childCtx, stopChild = context.WithTimeout(ctx, budget)
	}
	if spec.TimeoutSeconds > 0 {
		budget := time.Duration(spec.TimeoutSeconds)*time.Second - suiteRecordMargin
		if budget < 10*time.Second {
			budget = 10 * time.Second
		}
		childCtx, stopChild = context.WithTimeout(ctx, budget)
	}
	defer stopChild()
	e = c.WithPermitLock(ctx, m.Pin, func() error {
		if !registered {
			if e := c.Register(ctx, m.Pin, launchID, identity); e != nil {
				return e
			}
		}
		s, e := c.Inspect(ctx, m.Pin)
		if e != nil {
			return e
		}
		if s.State != "ACTIVE" {
			return Hold(failure.FencedUnknown, "WORKER_PERMIT_NOT_ACTIVE", "executor.Worker.start", "worker launch requires ACTIVE permit", nil)
		}
		cmd = exec.CommandContext(childCtx, "/usr/bin/bwrap", workerArgs(permitID, spec)...)
		cmd.Dir = "/"
		cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
		cmd.ExtraFiles = []*os.File{workspace}
		if scratch != nil {
			cmd.ExtraFiles = append(cmd.ExtraFiles, scratch)
		}
		if len(spec.Runtime) > 0 && scratch == nil {
			return Hold(failure.PolicyDenied, "WORKER_RUNTIME_SCRATCH_MISSING", "executor.Worker.start", "runtime descriptors require scratch descriptor", nil) // runtime descriptors are numbered after the scratch
		}
		cmd.ExtraFiles = append(cmd.ExtraFiles, runtimeFiles...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 994, Gid: 994, NoSetGroups: false}}
		// Fixed bounded parent output; no authority descriptor is inherited.
		cmd.Stdout = &out
		cmd.Stderr = &out
		cmd.WaitDelay = 3 * time.Second
		if e := cmd.Start(); e != nil {
			status, _ := unitfence.ReadSmall("/proc/self/status")
			evidence := []string{}
			for _, line := range strings.Split(string(status), "\n") {
				if strings.HasPrefix(line, "CapEff:") || strings.HasPrefix(line, "NoNewPrivs:") || strings.HasPrefix(line, "Seccomp:") {
					evidence = append(evidence, line)
				}
			}
			return fmt.Errorf("worker start failed (uid=%d, %s): %w", os.Geteuid(), strings.Join(evidence, "; "), e)
		}
		return nil
	})
	if e != nil {
		return e
	}
	waitErr := cmd.Wait()
	if heavy != nil {
		heavy.Close()
		heavy = nil
	}
	if m.Purpose == LocalReleasePurpose && waitErr == nil {
		waitErr = sealLocalRelease(m, ScratchMountPath, filepath.Join(AuthorityRoot, "release-artifacts", permitID))
	}
	if spec.Stage == "package" && waitErr == nil {
		waitErr = sealPipelineArtifacts(spec.Artifacts, ScratchMountPath, filepath.Join(AuthorityRoot, "pipeline-artifacts", permitID), 994)
	}
	if scratch != nil {
		// Build cache and temp files are disposable; evidence is the result below.
		clearModuleScratch()
	}
	if m.Purpose == "project-test-v1" || m.Purpose == "project-recipe-test-v1" || m.Purpose == ModuleTestPurpose || m.Purpose == DistributedShardPurpose || m.Purpose == LocalReleasePurpose {
		code := -1
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		}
		output := strings.ToValidUTF8(out.String(), "?")
		if childCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
			code = 124
			output += fmt.Sprintf("\n[mcpshka] suite stopped at its %ds timeout; output above is partial — rerun with a longer timeout_seconds\n", executionSeconds)
		}
		result := ExecutionResult{Pin: m.Pin, LaunchID: launchID, Identity: identity, ExitCode: code, WaitFailed: waitErr != nil, Output: output, ObservedAt: time.Now().UTC(), State: "EXITED_UNFENCED", Trust: "UNTRUSTED"}
		if e := persistWorkerExit(ctx, func(export context.Context) error {
			return c.WithPermitLock(export, m.Pin, func() error { return c.recordResult(export, result) })
		}); e != nil {
			return e
		}
		// Keep the trusted registered leader alive until the broker drains the exact
		// cgroup. A successful test process alone must never release write admission.
		<-ctx.Done()
		return ctx.Err()
	}
	if waitErr != nil {
		return fmt.Errorf("worker execution failed: %w: %s", waitErr, strings.TrimSpace(out.String()))
	}
	return nil
}

// OpenPermit reads only root-owned immutable authority; it never issues a permit.
func OpenPermit(id string) (*Controller, Manifest, error) {
	c, e := OpenAuthority()
	if e != nil {
		return nil, Manifest{}, e
	}
	m, e := readManifest(id)
	if e != nil {
		return nil, Manifest{}, e
	}
	if _, e = c.Inspect(context.Background(), m.Pin); e != nil {
		return nil, Manifest{}, e
	}
	return c, m, nil
}
