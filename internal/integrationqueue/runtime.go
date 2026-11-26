package integrationqueue

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"mcpshka/internal/gitops"
	"mcpshka/internal/policy"
	"mcpshka/internal/roadmapscan"
	"mcpshka/internal/statestore"
)

const runtimeCapacity = 1000
const runtimeRecordLimit = 4 << 20

var roadmapDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)
var phaseScope = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var ownedBranchID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Request pins intent only. Admission never approves tests, review or Git mutation.
type Request struct {
	Owner         string `json:"owner"`
	Path          string `json:"path"`
	ID            string `json:"id"`
	Branch        string `json:"branch"`
	Commit        string `json:"commit"`
	TargetBranch  string `json:"target_branch"`
	TargetHead    string `json:"target_head"`
	RoadmapSHA    string `json:"roadmap_sha256"`
	Scope         string `json:"scope"`
	OwnedBranchID string `json:"owned_branch_id,omitempty"`
}

type RuntimeItem struct {
	Request     Request `json:"request"`
	ExecutionID string  `json:"execution_id"`
	State       State   `json:"state"`
}

type RuntimeStatus struct {
	Trust     string       `json:"trust"`
	Root      string       `json:"root"`
	QueueID   string       `json:"queue_id"`
	Found     bool         `json:"found"`
	Item      *RuntimeItem `json:"item,omitempty"`
	Count     int          `json:"count"`
	Pending   int          `json:"pending"`
	Cancelled int          `json:"cancelled"`
}

type runtimeRecord struct {
	Version int           `json:"version"`
	Items   []RuntimeItem `json:"items"`
}

// Runtime uses a single SQLite CAS record per common Git repository. Linked
// worktrees share atomic admission, capacity and cancellation without owning
// one another's writer leases. External mutations are deliberately not admitted.
type Runtime struct {
	db            *statestore.DB
	policy        *policy.Policy
	git           *gitops.Service
	capacity      int
	withAuthority func(context.Context, RuntimeItem, func() error) error
	beforeGit     func() error // crash-boundary injection; never set by API callers
}

func NewRuntime(db *statestore.DB, p *policy.Policy) *Runtime {
	if p == nil {
		p = policy.Default()
	}
	return &Runtime{db: db, policy: p, git: gitops.New(p), capacity: runtimeCapacity}
}

func runtimeDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (s *Runtime) queue(ctx context.Context, path string) (string, string, error) {
	if s.db == nil {
		return "", "", errors.New("integration SQLite authority unavailable: HOLD")
	}
	root, err := s.git.RepoRoot(path)
	if err != nil {
		return "", "", err
	}
	common, err := readGit(ctx, root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", "", err
	}
	resolved, err := s.policy.ResolveExisting(common)
	if err != nil || resolved != common || !filepath.IsAbs(common) {
		return "", "", errors.New("integration Git authority unverified: HOLD")
	}
	return root, "integration/v1/queues/" + runtimeDigest(common), nil
}

// ValidRequest shares candidate pin validation with trusted permit issuers.
// Passing validation conveys no execution or review authority.
func ValidRequest(r Request) bool { return RequestProblem(r) == "" }

// RequestProblem names the first invalid field so callers can correct a pin.
func RequestProblem(r Request) string {
	switch {
	case len(r.Owner) == 0 || len(r.Owner) > 256 || strings.ContainsAny(r.Owner, "\x00\r\n"):
		return "owner"
	case !filepath.IsAbs(r.Path) || filepath.Clean(r.Path) != r.Path || len(r.Path) > 4096:
		return "path"
	case !requestID.MatchString(r.ID):
		return "id"
	case !safeBranch(r.Branch) || r.Branch == r.TargetBranch:
		return "branch"
	case !hexCommit.MatchString(r.Commit):
		return "commit (full 40-hex SHA required)"
	case !safeTargetBranch(r.TargetBranch):
		return "target_branch"
	case !hexCommit.MatchString(r.TargetHead):
		return "target_head (full 40-hex SHA required)"
	case !roadmapDigest.MatchString(r.RoadmapSHA):
		return "roadmap_sha256"
	case !phaseScope.MatchString(r.Scope):
		return "scope"
	case r.OwnedBranchID != "" && !ownedBranchID.MatchString(r.OwnedBranchID):
		return "owned_branch_id"
	}
	return ""
}

func executionID(key, id string) string { return runtimeDigest(key + ":" + id)[:32] }

func (s *Runtime) read(ctx context.Context, key string) (runtimeRecord, []byte, error) {
	raw, err := s.db.GetContext(ctx, key)
	if errors.Is(err, os.ErrNotExist) {
		return runtimeRecord{Version: 1, Items: []RuntimeItem{}}, nil, nil
	}
	if err != nil {
		return runtimeRecord{}, nil, err
	}
	var record runtimeRecord
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if len(raw) > runtimeRecordLimit || dec.Decode(&record) != nil || dec.Decode(new(any)) != io.EOF || record.Version != 1 || len(record.Items) > runtimeCapacity {
		return runtimeRecord{}, nil, errors.New("invalid durable integration snapshot: HOLD")
	}
	seen := map[string]bool{}
	for _, item := range record.Items {
		if !ValidRequest(item.Request) || seen[item.Request.ID] || item.ExecutionID != executionID(key, item.Request.ID) || (item.State != Pending && item.State != Cancelled && item.State != Integrating && item.State != Integrated) {
			return runtimeRecord{}, nil, errors.New("invalid durable integration item: HOLD")
		}
		seen[item.Request.ID] = true
	}
	return record, raw, nil
}

func runtimeStatus(root, key, id string, record runtimeRecord) RuntimeStatus {
	out := RuntimeStatus{Trust: "UNTRUSTED", Root: root, QueueID: filepath.Base(key), Count: len(record.Items)}
	for _, item := range record.Items {
		if item.State == Pending {
			out.Pending++
		}
		if item.State == Cancelled {
			out.Cancelled++
		}
		if item.Request.ID == id {
			copy := item
			out.Found = true
			out.Item = &copy
		}
	}
	return out
}

func (s *Runtime) Status(ctx context.Context, path, id string) (RuntimeStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if id != "" && !requestID.MatchString(id) {
		return RuntimeStatus{}, errors.New("invalid integration ID")
	}
	root, key, err := s.queue(ctx, path)
	if err != nil {
		return RuntimeStatus{}, err
	}
	record, _, err := s.read(ctx, key)
	if err != nil {
		return RuntimeStatus{}, err
	}
	return runtimeStatus(root, key, id, record), nil
}

func (s *Runtime) verify(ctx context.Context, r Request) error {
	origin, err := readGit(ctx, r.Path, "rev-parse", "--verify", "refs/heads/"+r.Branch+"^{commit}")
	if err != nil || origin != r.Commit {
		return errors.New("candidate branch does not match pinned commit")
	}
	plan, err := PlanMerge(ctx, r.Path, r.TargetBranch, r.Commit)
	if err != nil {
		return err
	}
	if plan.TargetHead != r.TargetHead {
		return errors.New("integration target changed: reconcile")
	}
	roadmap, err := roadmapscan.Load(filepath.Join(r.Path, "ROADMAP.md"), r.RoadmapSHA)
	if err != nil {
		return err
	}
	for _, phase := range roadmap.Phases {
		if phase.ID == r.Scope {
			return nil
		}
	}
	return errors.New("integration scope is not a ROADMAP phase")
}

func (s *Runtime) write(key string, old []byte, record runtimeRecord) (bool, error) {
	raw, err := json.Marshal(record)
	if err != nil {
		return false, err
	}
	if len(raw) > runtimeRecordLimit {
		return false, errors.New("integration receipt budget exceeded")
	}
	if old == nil {
		return s.db.PutIfAbsent(key, raw)
	}
	return s.db.Replace(key, old, raw)
}

func (s *Runtime) Enqueue(ctx context.Context, request Request) (RuntimeStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	root, key, err := s.queue(ctx, request.Path)
	if err != nil {
		return RuntimeStatus{}, err
	}
	request.Path = root
	if problem := RequestProblem(request); problem != "" {
		return RuntimeStatus{}, errors.New("invalid pinned integration request: " + problem)
	}
	verified := false
	for range 32 {
		if err := ctx.Err(); err != nil {
			return RuntimeStatus{}, err
		}
		record, raw, err := s.read(ctx, key)
		if err != nil {
			return RuntimeStatus{}, err
		}
		before := runtimeStatus(root, key, request.ID, record)
		if before.Found {
			if before.Item.Request != request {
				return RuntimeStatus{}, errors.New("integration idempotency conflict")
			}
			return before, nil
		}
		if s.capacity < 1 || s.capacity > runtimeCapacity || len(record.Items) >= s.capacity {
			return RuntimeStatus{}, errors.New("integration queue capacity exceeded")
		}
		if !verified {
			if err := s.verify(ctx, request); err != nil {
				return RuntimeStatus{}, err
			}
			verified = true
		}
		record.Items = append(record.Items, RuntimeItem{Request: request, ExecutionID: executionID(key, request.ID), State: Pending})
		ok, err := s.write(key, raw, record)
		if err != nil {
			return RuntimeStatus{}, err
		}
		if ok {
			return runtimeStatus(root, key, request.ID, record), nil
		}
	}
	return RuntimeStatus{}, errors.New("integration CAS contention budget: reconcile")
}

func (s *Runtime) CancelAs(ctx context.Context, path, id, commit, owner string) (RuntimeStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if !requestID.MatchString(id) || !hexCommit.MatchString(commit) {
		return RuntimeStatus{}, errors.New("invalid integration cancellation pin")
	}
	root, key, err := s.queue(ctx, path)
	if err != nil {
		return RuntimeStatus{}, err
	}
	for range 32 {
		if err := ctx.Err(); err != nil {
			return RuntimeStatus{}, err
		}
		record, raw, err := s.read(ctx, key)
		if err != nil {
			return RuntimeStatus{}, err
		}
		found := false
		for i := range record.Items {
			item := &record.Items[i]
			if item.Request.ID != id {
				continue
			}
			if owner == "" || item.Request.Owner != owner {
				return RuntimeStatus{}, errors.New("integration cancellation owner mismatch")
			}
			if item.Request.Commit != commit {
				return RuntimeStatus{}, errors.New("integration cancellation commit mismatch")
			}
			if item.State == Cancelled {
				return runtimeStatus(root, key, id, record), nil
			}
			if item.State != Pending {
				return RuntimeStatus{}, errors.New("integration already admitted: reconcile")
			}
			item.State = Cancelled
			found = true
			break
		}
		if !found {
			return RuntimeStatus{}, errors.New("unknown integration request")
		}
		ok, err := s.write(key, raw, record)
		if err != nil {
			return RuntimeStatus{}, err
		}
		if ok {
			return runtimeStatus(root, key, id, record), nil
		}
	}
	return RuntimeStatus{}, errors.New("integration cancellation CAS contention: reconcile")
}

// MarkPublished closes a pending item after the protected pipeline (approval,
// root worker, publish) moved the target to its commit. That path does not
// run Execute, so without this the item stayed pending forever. Only the exact
// request and execution identity, observed at the candidate, are closed.
func (s *Runtime) MarkPublished(ctx context.Context, request Request, executionID string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	root, key, err := s.queue(ctx, request.Path)
	if err != nil {
		return err
	}
	head, err := directTargetHead(ctx, root, "refs/heads/"+request.TargetBranch)
	if err != nil || head != request.Commit {
		return errors.New("target is not at the published candidate: HOLD")
	}
	for range 32 {
		current, previous, err := s.read(ctx, key)
		if err != nil {
			return err
		}
		found := false
		for i := range current.Items {
			item := &current.Items[i]
			if item.Request.ID != request.ID {
				continue
			}
			if item.Request != request || item.ExecutionID != executionID {
				return errors.New("published item identity changed: HOLD")
			}
			if item.State == Integrated {
				return nil
			}
			if item.State != Pending && item.State != Integrating {
				return fmt.Errorf("published item is %s: HOLD", item.State)
			}
			item.State = Integrated
			found = true
		}
		if !found {
			return errors.New("published item missing from queue: HOLD")
		}
		ok, err := s.write(key, previous, current)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	return errors.New("queue completion contention: HOLD")
}
