package codexcoord

// Admission is an advisory, side-effect-free planner. In particular, a
// READY_FOR_APPROVAL receipt never authorizes a worker, writer or Codex turn.
import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
)

type ScopeFacts struct {
	SessionID          string
	Worktree           string
	RoadmapSHA         string
	CatalogVerified    bool
	OwnerVerified      bool
	SessionIdle        bool
	WriterLeaseFree    bool
	ControlPathHealthy bool
	QuotaVerified      bool
	QuotaAvailable     bool
}
type ResourceBudget struct {
	WallSeconds int
	CPUPercent  int
	MemoryMiB   int
	OutputKiB   int
}
type AdmissionRequest struct {
	Task   Task
	Facts  ScopeFacts
	Budget ResourceBudget
}
type AdmissionDecision struct {
	TaskID          string   `json:"task_id"`
	Revision        uint64   `json:"revision"`
	IntentSHA256    string   `json:"intent_sha256"`
	Status          string   `json:"status"`
	Reasons         []string `json:"reasons"`
	DispatchEnabled bool     `json:"dispatch_enabled"`
}

func validResourceBudget(b ResourceBudget) bool {
	return b.WallSeconds >= 5 && b.WallSeconds <= 7200 &&
		b.CPUPercent >= 5 && b.CPUPercent <= 200 &&
		b.MemoryMiB >= 64 && b.MemoryMiB <= 2048 &&
		b.OutputKiB >= 1 && b.OutputKiB <= 4096
}
func admissionFingerprint(t Task, b ResourceBudget) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%q|%q|%q|%q|%q|%q|%q|%q|%q|%d|%d|%d|%d|%d",
		t.ID, t.SessionID, t.Worktree, t.RoadmapSHA, t.Message, t.Model, t.Effort, t.Provider,
		t.State, t.Revision, b.WallSeconds, b.CPUPercent, b.MemoryMiB, b.OutputKiB)))
	return hex.EncodeToString(h[:])
}

// validQueuedTask checks a persisted task at revision >= 1. In contrast,
// enqueue-time validation MUST reject supplied state/revision. Do not reuse
// enqueue validation here: that would make every real queued task ineligible
// once the two components are integrated.
func validQueuedTask(t Task, catalog []Model) bool {
	t.State, t.Revision, t.DispatchID = "", 0, ""
	return ValidateShape(t) == nil && catalogAccepts(catalog, t.Model, t.Effort)
}

func Assess(r AdmissionRequest, catalog []Model) AdmissionDecision {
	t, f := r.Task, r.Facts
	d := AdmissionDecision{TaskID: t.ID, Revision: t.Revision,
		IntentSHA256: admissionFingerprint(t, r.Budget), Status: "HOLD",
		DispatchEnabled: false, Reasons: []string{}}
	add := func(s string) { d.Reasons = append(d.Reasons, s) }
	if t.Revision == 0 || t.State != Queued || t.DispatchID != "" {
		add("task_not_queued")
	}
	if !validQueuedTask(t, catalog) {
		add("task_or_model_invalid")
	}
	if !validResourceBudget(r.Budget) {
		add("budget_invalid")
	}
	if f.SessionID != t.SessionID || f.Worktree != t.Worktree || f.RoadmapSHA != t.RoadmapSHA {
		add("scope_mismatch")
	}
	if !f.CatalogVerified {
		add("catalog_unverified")
	}
	if !f.OwnerVerified {
		add("session_owner_unverified")
	}
	if !f.SessionIdle {
		add("session_busy_or_unknown")
	}
	if !f.WriterLeaseFree {
		add("writer_lease_busy_or_unknown")
	}
	if !f.ControlPathHealthy {
		add("management_path_unverified")
	}
	if !f.QuotaVerified {
		add("quota_unknown")
	} else if !f.QuotaAvailable {
		add("quota_exhausted")
	}
	if len(d.Reasons) == 0 {
		d.Status = "READY_FOR_APPROVAL"
		d.Reasons = []string{"trusted_policy_and_worker_receipt_still_required"}
	}
	return d
}

// AssessBatch continues considering independent tasks even when an earlier
// session is busy. It never reserves or dispatches anything.
func AssessBatch(input []AdmissionRequest, catalog []Model, maxConcurrent int) ([]AdmissionDecision, error) {
	if len(input) > 32 || maxConcurrent < 1 || maxConcurrent > 32 {
		return nil, fmt.Errorf("batch budget exceeded")
	}
	requests := append([]AdmissionRequest(nil), input...)
	sort.SliceStable(requests, func(i, j int) bool { return requests[i].Task.ID < requests[j].Task.ID })
	out := make([]AdmissionDecision, 0, len(requests))
	seenIDs := map[string]bool{}
	sessions := map[string]bool{}
	trees := map[string]bool{}
	selected := 0
	for _, r := range requests {
		d := Assess(r, catalog)
		if seenIDs[r.Task.ID] {
			d.Status = "HOLD"
			d.Reasons = []string{"duplicate_task_id"}
		}
		seenIDs[r.Task.ID] = true
		if d.Status == "READY_FOR_APPROVAL" {
			switch {
			case sessions[r.Task.SessionID]:
				d.Status = "HOLD"
				d.Reasons = []string{"session_already_selected"}
			case trees[r.Task.Worktree]:
				d.Status = "HOLD"
				d.Reasons = []string{"worktree_already_selected"}
			case selected >= maxConcurrent:
				d.Status = "HOLD"
				d.Reasons = []string{"resource_capacity_reached"}
			default:
				sessions[r.Task.SessionID] = true
				trees[r.Task.Worktree] = true
				selected++
			}
		}
		out = append(out, d)
	}
	return out, nil
}
