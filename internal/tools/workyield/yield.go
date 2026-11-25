package workyield

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/deploy"
	"mcpshka/internal/gitops"
	"mcpshka/internal/jobs"
	"mcpshka/internal/projectlease"
	"mcpshka/internal/roadmapscan"
	"mcpshka/internal/tooldef"
	"mcpshka/internal/workflow"
	"os"
	"path/filepath"
	"time"
)

type WorkYieldInput struct {
	Path             string `json:"path"`
	Token            string `json:"token"`
	LeaseID          string `json:"lease_id"`
	Generation       uint64 `json:"generation"`
	ExecutionID      string `json:"execution_id"`
	ExpectedRevision string `json:"expected_revision"`
	IdempotencyKey   string `json:"idempotency_key"`
}
type WorkYieldStatusInput struct {
	YieldID string `json:"yield_id"`
}
type WorkYieldResult struct {
	State       string              `json:"state"`
	YieldID     string              `json:"yield_id"`
	ProjectRoot string              `json:"project_root"`
	LeaseID     string              `json:"lease_id"`
	Generation  uint64              `json:"generation"`
	ExecutionID string              `json:"execution_id"`
	Revision    string              `json:"revision"`
	Git         gitops.StatusResult `json:"git"`
	RoadmapSHA  string              `json:"roadmap_sha256,omitempty"`
	CapturedAt  time.Time           `json:"captured_at"`
	Reason      string              `json:"reason,omitempty"`
}
type Journal struct {
	OwnerHash string          `json:"owner_hash"`
	InputHash string          `json:"input_hash"`
	Result    WorkYieldResult `json:"result"`
}

func Hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func Proof(m *jobs.Manager, ctx context.Context, id string, identity func(context.Context) string) (Journal, error) {
	var out Journal
	if len(id) != 64 {
		return out, errors.New("invalid yield id")
	}
	if _, e := hex.DecodeString(id); e != nil {
		return out, errors.New("invalid yield id")
	}
	if m.Database() == nil {
		return out, errors.New("durable SQLite journal unavailable")
	}
	raw, e := m.Database().Get("project-yields/v1/" + id)
	if e != nil {
		return out, e
	}
	if e = json.Unmarshal(raw, &out); e != nil {
		return out, e
	}
	if out.OwnerHash != Hash(identity(ctx)) || out.Result.YieldID != id || out.Result.State != "RELEASED" {
		return Journal{}, errors.New("yield proof unavailable to this identity")
	}
	return out, nil
}
func Yield(ctx context.Context, m *jobs.Manager, s *projectlease.Service, in WorkYieldInput, identity func(context.Context) string) (WorkYieldResult, error) {
	var out WorkYieldResult
	if in.Path == "" || in.Token == "" || in.LeaseID == "" || in.Generation == 0 || in.ExecutionID == "" || in.ExpectedRevision == "" || len(in.IdempotencyKey) < 1 || len(in.IdempotencyKey) > 256 {
		return out, errors.New("pinned holder, revision and bounded retry key required")
	}
	if e := ctx.Err(); e != nil {
		return out, e
	}
	raw, e := json.Marshal(in)
	if e != nil || len(raw) > 4096 {
		return out, errors.New("yield input exceeds budget")
	}
	owner := Hash(identity(ctx))
	id := Hash(owner + "\x00" + in.IdempotencyKey)
	fingerprint := Hash(string(raw))
	prior, e := Proof(m, ctx, id, identity)
	if e == nil {
		if prior.InputHash != fingerprint {
			return out, errors.New("yield identity reused with changed input")
		}
		return prior.Result, nil
	}
	if !os.IsNotExist(e) {
		return out, e
	}
	git := gitops.New(m.Policy())
	root, e := git.RepoRoot(in.Path)
	if e != nil {
		return out, e
	}
	out = WorkYieldResult{State: "HOLD", YieldID: id, ProjectRoot: root, LeaseID: in.LeaseID, Generation: in.Generation, ExecutionID: in.ExecutionID}
	activity := m.ProjectActivity(root, 16)
	if activity.Count != 0 || activity.Truncated {
		out.Reason = "active or UNKNOWN project jobs"
		return out, nil
	}
	e = m.WithIdleProject(root, func() error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if e := workflow.New(m).Guard(root, ""); e != nil {
			return e
		}
		pending, e := deploy.New(m.Policy(), filepath.Join(filepath.Dir(m.StateDir()), "deploys")).Pending()
		if e != nil {
			return e
		}
		if pending {
			return errors.New("deployment pending: HOLD")
		}
		revision, e := git.Revision(root)
		if e != nil {
			return e
		}
		if revision != in.ExpectedRevision {
			return errors.New("Git revision changed: HOLD")
		}
		status, e := git.Status(root, 8192)
		if e != nil {
			return e
		}
		if status.Truncated {
			return errors.New("Git evidence truncated: HOLD")
		}
		out.Revision = revision
		out.Git = status
		out.CapturedAt = time.Now().UTC()
		roadmapPath := filepath.Join(root, "ROADMAP.md")
		if _, statErr := os.Lstat(roadmapPath); statErr == nil {
			scan, scanErr := roadmapscan.Load(roadmapPath, "")
			if scanErr != nil {
				return scanErr
			}
			out.RoadmapSHA = scan.SHA256
		} else if !os.IsNotExist(statErr) {
			return statErr
		}
		out.State = "RELEASED"
		journal, e := json.Marshal(Journal{OwnerHash: owner, InputHash: fingerprint, Result: out})
		if e != nil || len(journal) > 32768 {
			return errors.New("yield checkpoint exceeds budget")
		}
		if e := ctx.Err(); e != nil {
			return e
		}
		_, e = s.ReleasePinnedWithJournal(root, in.Token, in.LeaseID, in.Generation, in.ExecutionID, "project-yields/v1/"+id, journal)
		return e
	})
	if e != nil {
		// A lost commit reply is reconciled against the immutable atomic proof.
		if prior, probe := Proof(m, ctx, id, identity); probe == nil && prior.InputHash == fingerprint {
			return prior.Result, nil
		}
		out.State = "HOLD"
		out.Reason = e.Error()
		return out, nil
	}
	return out, nil
}
func Recover(ctx context.Context, m *jobs.Manager, tool, key string, input map[string]any, identity func(context.Context) string) (WorkYieldResult, error) {
	var request WorkYieldInput
	if len(input) == 0 {
		return WorkYieldResult{}, errors.New("exact yield input required for recovery")
	}
	if tool == "exec_start" {
		args, ok := input["args"].([]any)
		if !ok || len(args) != 2 {
			return WorkYieldResult{}, errors.New("exact compatibility yield input required")
		}
		raw, ok := args[1].(string)
		if !ok || len(raw) > 4096 {
			return WorkYieldResult{}, errors.New("invalid yield input")
		}
		if e := json.Unmarshal([]byte(raw), &request); e != nil {
			return WorkYieldResult{}, e
		}
	} else if tool == "project_work_yield" {
		raw, e := json.Marshal(input)
		if e != nil || len(raw) > 4096 {
			return WorkYieldResult{}, errors.New("invalid yield input")
		}
		if e = json.Unmarshal(raw, &request); e != nil {
			return WorkYieldResult{}, e
		}
	} else {
		return WorkYieldResult{}, errors.New("invalid yield tool")
	}
	if request.IdempotencyKey != key {
		return WorkYieldResult{}, errors.New("yield retry identity changed")
	}
	proof, e := Proof(m, ctx, Hash(Hash(identity(ctx))+"\x00"+key), identity)
	if e != nil {
		return WorkYieldResult{}, e
	}
	raw, e := json.Marshal(request)
	if e != nil || proof.InputHash != Hash(string(raw)) {
		return WorkYieldResult{}, errors.New("historical proof does not match this request")
	}
	return proof.Result, nil
}

func Register(add func(tooldef.Def), m *jobs.Manager, s *projectlease.Service, identity func(context.Context) string) {

	add(tooldef.New(&mcp.Tool{Name: "project_work_yield", Description: "Voluntary end of an AI work segment before its final user report. Close mutation admission, verify idle jobs/workflow/deploy and pinned Git/holder, atomically save a bounded checkpoint and release only that holder. HOLD active/UNKNOWN. No takeover, process fencing or DONE acceptance.", Annotations: &mcp.ToolAnnotations{}}, func(ctx context.Context, req *mcp.CallToolRequest, in WorkYieldInput) (*mcp.CallToolResult, WorkYieldResult, error) {
		out, e := Yield(ctx, m, s, in, identity)
		return nil, out, e
	}))
	add(tooldef.New(&mcp.Tool{Name: "project_work_yield_status", Description: "Read immutable historical voluntary-completion proof scoped to the caller. Does not assert the project is currently free.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in WorkYieldStatusInput) (*mcp.CallToolResult, WorkYieldResult, error) {
		out, e := Proof(m, ctx, in.YieldID, identity)
		return nil, out.Result, e
	}))
}
func AuthorizeReceipt(ctx context.Context, m *jobs.Manager, id string, identity func(context.Context) string) error {
	if err := m.AuthorizeInteractive(id, identity(ctx)); err != nil {
		return err
	}
	snapshot, e := m.Get(id)
	if e != nil {
		return e
	}
	if snapshot.Command != "mcpshka" || len(snapshot.Args) == 0 {
		return nil
	}
	expected := 0
	switch snapshot.Args[0] {
	case "work-yield":
		expected = 3
	case "recovery-diagnose":
		expected = 4
	default:
		return nil
	}
	if len(snapshot.Args) != expected || snapshot.Args[2] != "owner-sha256:"+Hash(identity(ctx)) {
		return errors.New("work receipt owner mismatch")
	}
	return nil
}
