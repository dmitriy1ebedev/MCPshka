package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mcpshka/internal/distributedci"
	"mcpshka/internal/failure"
	"mcpshka/internal/hostload"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mcpshka/internal/cievidence"
	"mcpshka/internal/releaseprep"
)

// MaxReleaseImage bounds one paired release image. 0.1.176's controller
// outgrew the old 32 MiB and could not be sealed (BUG-20261023-80).
const MaxReleaseImage = 64 << 20

const LocalReleasePurpose = "project-local-release-v1"
const LocalReleaseProfile = "local-release-v1"
const localReleasePackage = "release:paired"
const localBuildScript = "go version > /scratch/toolchain.txt\ngo build -p 2 -trimpath -buildvcs=false -o /scratch/controller ./cmd/mcpshka\ngo build -p 2 -trimpath -buildvcs=false -o /scratch/broker ./cmd/mcpshka-broker"

var localReleaseCommand = []string{"/bin/sh", "-ec", localBuildScript}

func moduleSnapshot(pkg string) bool {
	return pkg == moduleTestPackage || pkg == moduleAffectedPackage || pkg == localReleasePackage || shardPackageValid(pkg)
}
func localReleaseCommandMatches(command []string) bool {
	return strings.Join(command, "\x00") == strings.Join(localReleaseCommand, "\x00")
}
func PrepareLocalRelease(ctx context.Context, bootstrap, repo, commit string) (Manifest, error) {
	return prepareSnapshotTest(ctx, bootstrap, repo, commit, localReleasePackage, LocalReleasePurpose, LocalReleaseProfile, append([]string(nil), localReleaseCommand...), 0)
}
func LocalReleaseIdentity(bootstrap string) (Pin, string, error) {
	if !idPattern.MatchString(bootstrap) {
		return Pin{}, "", Hold(failure.PolicyDenied, "RELEASE_BOOTSTRAP_INVALID", "release.LocalReleaseIdentity", "release bootstrap invalid", nil)
	}
	p, l := snapshotIdentity(LocalReleasePurpose, bootstrap)
	return p, l, nil
}
func releaseSourcePath(name string) bool {
	if filepath.Clean(name) != name || filepath.IsAbs(name) || strings.Contains(name, "\\") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." || part == ".git" {
			return false
		}
	}
	if name == "go.mod" || name == "go.sum" || name == "Makefile" {
		return true
	}
	for _, prefix := range []string{"cmd/", "internal/", "packaging/", "skills/", ".github/workflows/", ".gitverse/workflows/"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// Same framing and paths as packaging/cloud/source_digest.py, over the sealed
// committed bytes. Dependencies have a separate environment digest.
func releaseSourceDigest(base string) (string, error) {
	paths := []string{}
	var total int64
	err := filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, e := filepath.Rel(base, p)
		if e != nil {
			return e
		}
		if d.IsDir() {
			if rel == moduleDepsDir {
				return filepath.SkipDir
			}
			return nil
		}
		if !releaseSourcePath(filepath.ToSlash(rel)) {
			// docs/, README.md and the like are not release source: CI skips them too.
			return nil
		}
		info, e := d.Info()
		if e != nil || !info.Mode().IsRegular() || info.Size() > 16<<20 {
			return Hold(failure.PolicyDenied, "RELEASE_SOURCE_UNSAFE", "release.releaseSourceDigest", "release source file unavailable or unsafe", e)
		}
		total += info.Size()
		if total > 128<<20 || len(paths) >= 20000 {
			return Hold(failure.PolicyDenied, "RELEASE_SOURCE_LIMIT", "release.releaseSourceDigest", "release source limit exceeded", nil)
		}
		paths = append(paths, rel)
		return nil
	})
	if err != nil || len(paths) == 0 {
		return "", Hold(failure.EvidenceMissing, "RELEASE_SOURCE_MISSING", "release.releaseSourceDigest", "release source unavailable or empty", err)
	}
	sort.Strings(paths)
	h := sha256.New()
	h.Write([]byte("mcpshka-release-source/v1\x00"))
	for _, p := range paths {
		raw, e := os.ReadFile(filepath.Join(base, p))
		if e != nil {
			return "", e
		}
		part := sha256.Sum256(raw)
		h.Write([]byte(filepath.ToSlash(p) + "\x00"))
		h.Write(part[:])
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func localRead(path string, limit int64, owner uint32) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var st syscall.Stat_t
	if syscall.Fstat(fd, &st) != nil || st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Uid != owner || st.Nlink != 1 || st.Size > limit {
		return nil, Hold(failure.PolicyDenied, "LOCAL_FILE_UNSAFE", "release.localRead", "local file owner, type, links or size invalid", nil)
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	var after syscall.Stat_t
	if err != nil || int64(len(raw)) != st.Size || syscall.Fstat(fd, &after) != nil || st.Mtim != after.Mtim || st.Ctim != after.Ctim || st.Size != after.Size {
		return nil, Hold(failure.FencedUnknown, "LOCAL_FILE_CHANGED", "release.localRead", "local file changed or incomplete read", err)
	}
	return raw, nil
}
func localWrite(path string, raw []byte) error {
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0400)
	if e != nil {
		return e
	}
	_, e = f.Write(raw)
	if e == nil {
		e = f.Sync()
	}
	if ce := f.Close(); e == nil {
		e = ce
	}
	return e
}
func sealLocalRelease(m Manifest, scratch, dest string) error {
	return collectLocalRelease(m, scratch, WorkspaceMountPath, dest, 994)
}
func collectLocalRelease(m Manifest, scratch, workspace, dest string, owner uint32) error {
	if m.Source == nil || m.Purpose != LocalReleasePurpose {
		return Hold(failure.PolicyDenied, "RELEASE_MANIFEST_INVALID", "release.collectLocalRelease", "release source or purpose invalid", nil)
	}
	version, e := localRead(filepath.Join(scratch, "toolchain.txt"), 512, owner)
	if e != nil || !strings.HasPrefix(string(version), "go version go") {
		return Hold(failure.EvidenceMissing, "TOOLCHAIN_EVIDENCE_INVALID", "release.collectLocalRelease", "toolchain evidence missing or invalid", e)
	}
	cache, e := snapshotDigestLimited(filepath.Join(workspace, moduleDepsDir), moduleMaxFiles, moduleMaxBytes)
	if e != nil {
		return e
	}
	env := sha256.Sum256([]byte(string(version) + "\x00GOFLAGS=-mod=readonly\x00GOOS=linux\x00GOARCH=arm64\x00CGO_ENABLED=1\x00CC=gcc\x00" + cache))
	r := releaseprep.LocalReceipt{Schema: releaseprep.LocalSchema, Provider: "h96-local", Repository: cievidence.Repository, SourceSHA: m.Source.Commit, HeadSHA: m.Source.Commit, Tree: m.Source.Tree, SourceDigest: m.Source.ReleaseDigest, BuildPermit: m.Pin.PermitID, BuildEnvDigest: hex.EncodeToString(env[:])}
	if e = os.MkdirAll(filepath.Dir(dest), 0700); e != nil {
		return e
	}
	if e = os.Mkdir(dest, 0700); e != nil {
		return e
	}
	for _, name := range []string{"controller", "broker"} {
		raw, e := localRead(filepath.Join(scratch, name), MaxReleaseImage, owner)
		if e != nil || len(raw) < 64 {
			return Hold(failure.EvidenceMissing, "RELEASE_IMAGE_MISSING", "release.collectLocalRelease", "paired release image missing or too short", e)
		}
		if e = localWrite(filepath.Join(dest, name), raw); e != nil {
			return e
		}
		hash := sha256.Sum256(raw)
		if name == "controller" {
			r.BinarySHA = hex.EncodeToString(hash[:])
		} else {
			r.BrokerSHA = hex.EncodeToString(hash[:])
		}
	}
	raw, e := json.Marshal(r)
	if e != nil {
		return e
	}
	return localWrite(filepath.Join(dest, "receipt.json"), raw)
}
func successfulLocalPermit(ctx context.Context, id string) (Manifest, error) {
	started := time.Now()
	c, m, e := OpenPermit(id)
	if e != nil {
		return m, contextFailure(ctx, "release.successfulLocalPermit", started, e)
	}
	s, e := c.Inspect(ctx, m.Pin)
	if e != nil || s.State != "FENCED" || s.Result == nil || s.Result.State != "SUCCEEDED" || s.Result.ExitCode != 0 || s.Result.WaitFailed || m.Source == nil {
		return m, Hold(failure.FencedUnknown, "PERMIT_SUCCESS_UNVERIFIED", "release.successfulLocalPermit", "permit must be fenced with successful result and source", contextFailure(ctx, "release.successfulLocalPermit", started, e))
	}
	return m, nil
}

// LocalReleaseReceipt independently binds two fenced sealed permits. A build
// cannot serve as test evidence, and GitHub evidence cannot masquerade as GitVerse.
func LocalReleaseReceipt(ctx context.Context, build, test string) (releaseprep.LocalReceipt, error) {
	var r releaseprep.LocalReceipt
	m, e := successfulLocalPermit(ctx, build)
	if e != nil || m.Purpose != LocalReleasePurpose {
		return r, Hold(failure.FencedUnknown, "BUILD_PERMIT_INVALID", "release.LocalReleaseReceipt", "build permit unavailable or wrong purpose", e)
	}
	t, e := successfulLocalPermit(ctx, test)
	if e != nil || t.Source.Repo != m.Source.Repo || t.Source.Commit != m.Source.Commit {
		return r, Hold(failure.FencedUnknown, "TEST_PERMIT_MISMATCH", "release.LocalReleaseReceipt", "test permit unavailable or source mismatch", e)
	}
	raw, e := localRead(filepath.Join(AuthorityRoot, "release-artifacts", build, "receipt.json"), 8192, 0)
	if e != nil || json.Unmarshal(raw, &r) != nil || r.BuildPermit != build || r.SourceSHA != m.Source.Commit || r.Tree != m.Source.Tree || r.SourceDigest != m.Source.ReleaseDigest {
		return r, Hold(failure.FencedUnknown, "BUILD_RECEIPT_INVALID", "release.LocalReleaseReceipt", "build receipt missing or source binding invalid", e)
	}
	r.TestEvidence = releaseprep.TestEvidence{ID: test, Tree: r.Tree, Conclusion: "success"}
	switch t.Purpose {
	case ModuleTestPurpose:
		if t.Source.Package != moduleTestPackage {
			return r, Hold(failure.EvidenceMissing, "TEST_EVIDENCE_PARTIAL", "release.LocalReleaseReceipt", "the integration test covered only the affected packages; a release needs the full module suite", nil)
		}
		r.TestEvidence.Kind = "module"
		r.TestEvidence.Suites = map[string]string{"module": "success"}
	case CIEvidencePurpose:
		ev, e := localGitVerseEvidence(t, filepath.Join(AuthorityRoot, "workspaces", test), r.SourceSHA, r.Tree, 0)
		if e != nil {
			return r, e
		}
		r.TestEvidence.Kind = map[bool]string{true: "gitverse", false: "github"}[ev.Provider != ""] // 0.9.8 fallback: GitHub
		r.TestEvidence.Suites = ev.Checks
		if ev.Provider == "distributed" {
			r.TestEvidence.Kind = "distributed"
			r.TestEvidence.Suites = distributedci.PassedSuites()
		}
	default:
		return r, Hold(failure.PolicyDenied, "EVIDENCE_KIND_INVALID", "release.LocalReleaseReceipt", "unsupported test evidence kind", nil)
	}
	raw, _ = json.Marshal(r)
	return releaseprep.ParseLocalReceipt(raw)
}
func LocalReleaseRead(ctx context.Context, permit, name string, offset int64) ([]byte, bool, error) {
	if name != "controller" && name != "broker" || offset < 0 || offset > MaxReleaseImage {
		return nil, false, Hold(failure.PolicyDenied, "RELEASE_READ_INPUT_INVALID", "release.LocalReleaseRead", "release image name or offset invalid", nil)
	}
	m, e := successfulLocalPermit(ctx, permit)
	if e != nil || m.Purpose != LocalReleasePurpose {
		return nil, false, Hold(failure.FencedUnknown, "READ_PERMIT_INVALID", "release.LocalReleaseRead", "release permit unavailable or wrong purpose", e)
	}
	path := filepath.Join(AuthorityRoot, "release-artifacts", permit, name)
	fd, e := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if e != nil {
		return nil, false, e
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	var st syscall.Stat_t
	if syscall.Fstat(fd, &st) != nil || st.Uid != 0 || st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Mode&0777 != 0400 || st.Nlink != 1 || st.Size > MaxReleaseImage || offset > st.Size {
		return nil, false, Hold(failure.PolicyDenied, "RELEASE_IMAGE_UNSAFE", "release.LocalReleaseRead", "release image owner, type, mode, links, size or offset invalid", nil)
	}
	raw := make([]byte, min(int64(32768), st.Size-offset))
	n, e := f.ReadAt(raw, offset)
	if e != nil && e != io.EOF {
		return nil, false, fmt.Errorf("release read: %w", e)
	}
	return raw[:n], offset+int64(n) == st.Size, nil
}

// The same scratch admission excludes concurrent module tests. The tighter
// local-release ceiling is 1.5 load per logical CPU, including host services.
func LocalReleaseAdmission(permit string) error {
	if err := ModuleLaunchAdmission(permit); err != nil {
		return err
	}
	if hostload.Read().Busy {
		return errModuleBusy
	}
	return nil
}

func missingGitVerseEvidence(m Manifest, cause error) error {
	e := failure.NewHold(failure.EvidenceMissing, "EVIDENCE_WORKSPACE_MISSING", "release.LocalReleaseReceipt", "sealed GitVerse evidence workspace or file missing", cause)
	// Match the existing fixed bootstrap protocol; never guess an attempt.
	for attempt := 1; attempt <= 9; attempt++ {
		seed := "integration-ci:gitverse:" + m.Source.Repo + ":" + m.Source.Commit
		if attempt > 1 {
			seed += ":attempt-" + strconv.Itoa(attempt)
		}
		sum := sha256.Sum256([]byte(seed))
		pin, _ := snapshotIdentity(CIEvidencePurpose, hex.EncodeToString(sum[:16]))
		if pin.PermitID == m.Pin.PermitID && attempt < 9 {
			e.NextSafeAction = failure.Action{Tool: "integration_test_prepare", Arguments: map[string]any{"path": m.Source.Repo, "commit": m.Source.Commit, "evidence": "gitverse", "attempt": attempt + 1}}
			break
		}
	}
	return e
}

func localGitVerseEvidence(m Manifest, base, commit, tree string, owner uint32) (cievidence.Evidence, error) {
	var ev cievidence.Evidence
	if _, err := os.Lstat(base); errors.Is(err, os.ErrNotExist) {
		return ev, missingGitVerseEvidence(m, err)
	}
	digest, err := snapshotDigestLimited(base, 2, ciEvidenceMaxBytes)
	if err != nil || digest != m.Source.Digest {
		return ev, Hold(failure.FencedUnknown, "EVIDENCE_DIGEST_MISMATCH", "release.LocalReleaseReceipt", "GitVerse evidence digest unavailable or mismatched", err)
	}
	raw, err := localRead(filepath.Join(base, ciEvidenceFile), ciEvidenceMaxBytes, owner)
	if errors.Is(err, os.ErrNotExist) {
		return ev, missingGitVerseEvidence(m, err)
	}
	if err != nil || json.Unmarshal(raw, &ev) != nil || (ev.Provider != cievidence.GitVerseProvider && ev.Provider != "" && ev.Provider != "distributed") || ev.Verify(commit, tree) != nil {
		return ev, Hold(failure.FencedUnknown, "EVIDENCE_BINDING_INVALID", "release.LocalReleaseReceipt", "CI evidence invalid or bound to another source", err)
	}
	return ev, nil
}
