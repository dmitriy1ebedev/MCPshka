package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"mcpshka/internal/integrationqueue"
)

// RepositoryIdentity pins the shared Git directory, not just one checkout.
type RepositoryIdentity struct {
	CommonDir string `json:"common_dir"`
	Device    uint64 `json:"device"`
	Inode     uint64 `json:"inode"`
}

// IntegrationReview is supplied exclusively by a root-authorized review
// producer. Its digest identifies the reviewed evidence; it is never inferred
// from worker output or a caller's Boolean approval.
type IntegrationReview struct {
	Reviewer string `json:"reviewer"`
	Digest   string `json:"digest"`
}

type IntegrationApproval struct {
	Version     int                      `json:"version"`
	ID          string                   `json:"id"`
	Request     integrationqueue.Request `json:"request"`
	ExecutionID string                   `json:"execution_id"`
	Repository  RepositoryIdentity       `json:"repository"`
	TestPin     Pin                      `json:"test_pin"`
	Source      SourceSnapshot           `json:"source"`
	LaunchID    string                   `json:"launch_id"`
	Identity    Identity                 `json:"identity"`
	Review      IntegrationReview        `json:"review"`
	IssuedAt    time.Time                `json:"issued_at"`
}

func integrationApprovalID(item integrationqueue.RuntimeItem) string {
	// State progresses during recovery; every authority-bearing pin stays fixed.
	b, _ := json.Marshal(struct {
		Request     integrationqueue.Request
		ExecutionID string
	}{item.Request, item.ExecutionID})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func integrationApprovalKey(item integrationqueue.RuntimeItem) string {
	return "executor/integration-approvals/" + integrationApprovalID(item) + ".json"
}

func validIntegrationPins(item integrationqueue.RuntimeItem, repo RepositoryIdentity, review IntegrationReview) bool {
	r := item.Request
	return integrationqueue.ValidRequest(r) && idPattern.MatchString(item.ExecutionID) && r.ID != "" && len(r.ID) <= 128 && r.Owner != "" && len(r.Owner) <= 256 && !strings.ContainsAny(r.Owner, "\x00\r\n") &&
		filepath.IsAbs(r.Path) && filepath.Clean(r.Path) == r.Path && strings.HasPrefix(r.Path, "/root/repos/") && commitPattern.MatchString(r.Commit) && commitPattern.MatchString(r.TargetHead) &&
		r.Branch != "" && r.TargetBranch != "" && r.Branch != r.TargetBranch && digestPattern.MatchString(r.RoadmapSHA) && r.Scope != "" && len(r.Scope) <= 64 &&
		filepath.IsAbs(repo.CommonDir) && filepath.Clean(repo.CommonDir) == repo.CommonDir && strings.HasPrefix(repo.CommonDir, "/root/repos/") && repo.Device != 0 && repo.Inode != 0 &&
		review.Reviewer != "" && len(review.Reviewer) <= 256 && !strings.ContainsAny(review.Reviewer, "\x00\r\n") && digestPattern.MatchString(review.Digest) &&
		(item.State == integrationqueue.Pending || item.State == integrationqueue.Integrating || item.State == integrationqueue.Integrated)
}

// ApproveIntegration is root-only issuance, not an MCP approval endpoint. The
// caller must be an authenticated trusted review producer. Opening the sealed
// authority and manifest happens here; daemon-supplied database paths, test
// results and manifests are never accepted.
func ApproveIntegration(ctx context.Context, testPermitID string, item integrationqueue.RuntimeItem, review IntegrationReview) (IntegrationApproval, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c, err := OpenAuthority()
	if err != nil {
		return IntegrationApproval{}, err
	}
	m, err := readManifest(testPermitID)
	if err != nil {
		return IntegrationApproval{}, err
	}
	repo, err := integrationRepository(ctx, item.Request.Path)
	if err != nil {
		return IntegrationApproval{}, err
	}
	return c.recordIntegrationApproval(ctx, m, item, repo, review)
}

// IntegrationApprovalStatus is a fixed-ID root consumption boundary. It
// recomputes repository identity and reloads the sealed test manifest on every
// cold inspection. A successful status is evidence, not a writer fence.
func IntegrationApprovalStatus(ctx context.Context, id string) (IntegrationApproval, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if !digestPattern.MatchString(id) {
		return IntegrationApproval{}, ErrHold
	}
	c, err := OpenAuthority()
	if err != nil {
		return IntegrationApproval{}, err
	}
	raw, err := c.db.GetContext(ctx, "executor/integration-approvals/"+id+".json")
	if err != nil || len(raw) > 16384 {
		return IntegrationApproval{}, ErrHold
	}
	var a IntegrationApproval
	if json.Unmarshal(raw, &a) != nil || a.ID != id {
		return IntegrationApproval{}, ErrHold
	}
	m, err := readManifest(a.TestPin.PermitID)
	if err != nil {
		return IntegrationApproval{}, ErrHold
	}
	repo, err := c.integrationApprovalRepository(ctx, a)
	if err != nil {
		return IntegrationApproval{}, ErrHold
	}
	return c.inspectIntegrationApprovalManifest(ctx, m, integrationqueue.RuntimeItem{Request: a.Request, ExecutionID: a.ExecutionID, State: integrationqueue.Pending}, repo)
}

func (c *Controller) inspectIntegrationApprovalManifest(ctx context.Context, m Manifest, item integrationqueue.RuntimeItem, repo RepositoryIdentity) (IntegrationApproval, error) {
	a, err := c.inspectIntegrationApproval(ctx, item, repo)
	if err != nil || !integrationTestPurpose(m.Purpose) || m.Pin != a.TestPin || !validSource(m.Source) || *m.Source != a.Source || len(m.Runs) != 1 {
		return IntegrationApproval{}, ErrHold
	}
	spec, ok := m.Runs[a.LaunchID]
	if !ok || !integrationTestRun(m, spec) {
		return IntegrationApproval{}, ErrHold
	}
	return a, nil
}

func integrationRepository(ctx context.Context, path string) (RepositoryIdentity, error) {
	if os.Geteuid() != 0 || !filepath.IsAbs(path) || filepath.Clean(path) != path || !strings.HasPrefix(path, "/root/repos/") {
		return RepositoryIdentity{}, ErrHold
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path {
		return RepositoryIdentity{}, ErrHold
	}
	root, err := gitSnapshotRead(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil || strings.TrimSpace(string(root)) != path {
		return RepositoryIdentity{}, ErrHold
	}
	raw, err := gitSnapshotRead(ctx, path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return RepositoryIdentity{}, ErrHold
	}
	common := strings.TrimSpace(string(raw))
	canonical, err = filepath.EvalSymlinks(common)
	if err != nil || canonical != common || !filepath.IsAbs(common) || !strings.HasPrefix(common, "/root/repos/") {
		return RepositoryIdentity{}, ErrHold
	}
	info, err := os.Lstat(common)
	if err != nil || !info.IsDir() {
		return RepositoryIdentity{}, ErrHold
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 994 {
		return RepositoryIdentity{}, ErrHold
	}
	return RepositoryIdentity{CommonDir: common, Device: uint64(st.Dev), Inode: st.Ino}, nil
}

// Only the sealed-manifest root boundary above calls this recorder. Keeping the
// state transition separate allows isolated SQLite tests without privileged
// writes to live authority storage.
func (c *Controller) recordIntegrationApproval(ctx context.Context, m Manifest, item integrationqueue.RuntimeItem, repo RepositoryIdentity, review IntegrationReview) (IntegrationApproval, error) {
	if ctx.Err() != nil {
		return IntegrationApproval{}, ctx.Err()
	}
	if !validIntegrationPins(item, repo, review) || !integrationTestPurpose(m.Purpose) || !validSource(m.Source) || m.Source.Repo != item.Request.Path || m.Source.Commit != item.Request.Commit || len(m.Runs) != 1 {
		return IntegrationApproval{}, ErrHold
	}
	s, err := c.Inspect(ctx, m.Pin)
	if err != nil || s.State != "FENCED" || s.Result == nil || s.Result.State != "SUCCEEDED" {
		return IntegrationApproval{}, ErrHold
	}
	spec, ok := m.Runs[s.Result.LaunchID]
	if !ok || !integrationTestRun(m, spec) {
		return IntegrationApproval{}, ErrHold
	}
	if item.Request.OwnedBranchID != "" {
		if err := c.validateOwnedBranchBinding(ctx, ProtectedRepositoryRoot, 0, item.Request, repo, m.Pin, s.Result.LaunchID, SystemdFence{}); err != nil {
			return IntegrationApproval{}, ErrHold
		}
	}
	a := IntegrationApproval{Version: 1, ID: integrationApprovalID(item), Request: item.Request, ExecutionID: item.ExecutionID, Repository: repo, TestPin: m.Pin, Source: *m.Source, LaunchID: s.Result.LaunchID, Identity: s.Result.Identity, Review: review, IssuedAt: time.Now().UTC()}
	if a.Request.OwnedBranchID != "" {
		if err := c.reserveOwnedBranchApproval(ctx, a.Request.OwnedBranchID, a.ID); err != nil {
			return IntegrationApproval{}, ErrHold
		}
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return IntegrationApproval{}, err
	}
	ok, err = c.db.PutIfAbsent(integrationApprovalKey(item), raw)
	if err != nil {
		return IntegrationApproval{}, err
	}
	if !ok {
		existing, err := c.inspectIntegrationApproval(ctx, item, repo)
		if err != nil || existing.TestPin != a.TestPin || existing.Source != a.Source || existing.LaunchID != a.LaunchID || existing.Identity != a.Identity || existing.Review != a.Review {
			return IntegrationApproval{}, ErrHold
		}
		return existing, nil
	}
	return a, nil
}

func (c *Controller) inspectIntegrationApproval(ctx context.Context, item integrationqueue.RuntimeItem, repo RepositoryIdentity) (IntegrationApproval, error) {
	if c.db == nil {
		return IntegrationApproval{}, ErrHold
	}
	raw, err := c.db.GetContext(ctx, integrationApprovalKey(item))
	if err != nil {
		return IntegrationApproval{}, ErrHold
	}
	var a IntegrationApproval
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if len(raw) > 16384 || dec.Decode(&a) != nil || dec.Decode(new(any)) != io.EOF || a.Version != 1 || a.ID != integrationApprovalID(item) || a.Request != item.Request || a.ExecutionID != item.ExecutionID || a.Repository != repo || !validIntegrationPins(item, repo, a.Review) || !validSource(&a.Source) || a.Source.Repo != item.Request.Path || a.Source.Commit != item.Request.Commit || a.IssuedAt.IsZero() || a.IssuedAt.After(time.Now().Add(time.Minute)) {
		return IntegrationApproval{}, ErrHold
	}
	// Historical approvals remain inspectable after journaled retirement;
	// issuance requires live provenance, inspection verifies immutable pins.
	if a.Request.OwnedBranchID != "" {
		if err := c.inspectOwnedBranchApproval(ctx, a.Request.OwnedBranchID, a.ID); err != nil {
			return IntegrationApproval{}, ErrHold
		}
		if _, err := c.ownedBranchApprovalPins(ctx, a.Request, repo, a.TestPin, a.LaunchID); err != nil {
			return IntegrationApproval{}, ErrHold
		}
	}
	s, err := c.Inspect(ctx, a.TestPin)
	if err != nil || s.State != "FENCED" || s.Result == nil || s.Result.State != "SUCCEEDED" || s.Result.LaunchID != a.LaunchID || s.Result.Identity != a.Identity {
		return IntegrationApproval{}, ErrHold
	}
	return a, nil
}

// ApproveProtectedIntegration keeps tested source identity separate from the
// root-born integration destination. This root-only producer is not an MCP
// endpoint and does not import objects, update refs or grant an execution.
func ApproveProtectedIntegration(ctx context.Context, testPermitID string, item integrationqueue.RuntimeItem, review IntegrationReview, storeID string) (IntegrationApproval, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if os.Geteuid() != 0 || !idPattern.MatchString(storeID) {
		return IntegrationApproval{}, ErrHold
	}
	c, err := OpenAuthority()
	if err != nil {
		return IntegrationApproval{}, err
	}
	m, err := readManifest(testPermitID)
	if err != nil {
		return IntegrationApproval{}, err
	}
	// Preserve validation of the source worktree; it is not the destination.
	if _, err := integrationRepository(ctx, item.Request.Path); err != nil {
		return IntegrationApproval{}, ErrHold
	}
	repo, err := protectedDirectoryIdentity(filepath.Join(ProtectedRepositoryRoot, storeID+".git"), 0)
	if err != nil {
		return IntegrationApproval{}, ErrHold
	}
	if _, err := c.protectedRepositoryBirth(ctx, ProtectedRepositoryRoot, repo, 0); err != nil {
		return IntegrationApproval{}, ErrHold
	}
	if err := verifyProtectedRepositoryStorage(ctx, repo); err != nil {
		return IntegrationApproval{}, ErrHold
	}
	return c.recordIntegrationApproval(ctx, m, item, repo, review)
}

func (c *Controller) integrationApprovalRepository(ctx context.Context, a IntegrationApproval) (RepositoryIdentity, error) {
	if filepath.Dir(a.Repository.CommonDir) == ProtectedRepositoryRoot {
		if !c.authority || os.Geteuid() != 0 {
			return RepositoryIdentity{}, ErrHold
		}
		if _, err := c.protectedRepositoryBirth(ctx, ProtectedRepositoryRoot, a.Repository, 0); err != nil {
			return RepositoryIdentity{}, ErrHold
		}
		if err := verifyProtectedRepositoryStorage(ctx, a.Repository); err != nil {
			return RepositoryIdentity{}, ErrHold
		}
		return a.Repository, nil
	}
	return integrationRepository(ctx, a.Request.Path)
}

// Integration evidence is the historical single-package permit, the
// whole-module gate or sealed GitHub CI evidence; each needs the exact sealed
// command of its profile.
func integrationTestPurpose(purpose string) bool {
	return purpose == "project-test-v1" || purpose == ModuleTestPurpose || purpose == CIEvidencePurpose
}

func integrationTestRun(m Manifest, spec RunSpec) bool {
	if spec.validate() != nil || m.Source == nil {
		return false
	}
	if m.Purpose == ModuleTestPurpose {
		return moduleGateRun(m, spec)
	}
	if m.Purpose == CIEvidencePurpose {
		return spec.Profile == CIEvidenceProfile && ciEvidencePackageValid(m.Source.Package) && ciEvidenceCommandMatches(spec.Command)
	}
	return m.Purpose == "project-test-v1" && spec.Profile == "project-test-v1" && strings.Join(spec.Command, " ") == "go test -p 1 ./"+m.Source.Package
}
