// Package mission provides observations and durable mission state; execution stays with existing tools.
package mission

import (
	"mcpshka/internal/notify"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const HeartbeatThreshold = 30 * time.Minute
const CheckInterval = time.Minute

// ArchiveAfter hides stopped work whose last evidence is older than this:
// a run abandoned for days is history, not a blocker, and kept every
// mission degraded (old roadmap_run rows in every Lane/DIAG status).
const ArchiveAfter = 72 * time.Hour

type Observation struct {
	Kind, ID, Path, Phase, UnitState string
	EvidenceAt, ResetAt              time.Time
	BlockedBy                        []string
	Running                          bool
}

type Action struct {
	Tool     string            `json:"tool"`
	Args     map[string]string `json:"args"`
	Approval string            `json:"approval"`
}

type Item struct {
	Kind           string    `json:"kind"`
	ID             string    `json:"id"`
	Path           string    `json:"path"`
	CurrentPhase   string    `json:"current_phase"`
	State          string    `json:"state"`
	EvidenceAge    *int64    `json:"evidence_age"` // seconds; nil means unavailable or future evidence
	BlockedBy      []string  `json:"blocked_by"`
	UnitState      string    `json:"unit_state"`
	NextCheck      time.Time `json:"next_check"`
	NextSafeAction Action    `json:"next_safe_action"`
	// Stop is the exact call that stops this work (owner: one list of what
	// runs, each with its stop); approval names what that call still needs.
	Stop *Action `json:"stop,omitempty"`
}

type Report struct {
	notify.Progress
	Mission                   *Snapshot   `json:"mission,omitempty"`
	Missions                  []Reference `json:"missions,omitempty"`
	MissionsTruncated         bool        `json:"missions_truncated,omitempty"`
	Trust                     string      `json:"trust"`
	State                     string      `json:"state"`
	CapturedAt                time.Time   `json:"captured_at"`
	HeartbeatThresholdSeconds int64       `json:"heartbeat_threshold_seconds"`
	IndependentWorkAvailable  *bool       `json:"independent_work_available"`
	FreeProjects              []string    `json:"free_projects"`
	NextCheck                 time.Time   `json:"next_check"`
	Truncated                 bool        `json:"truncated"`
	Incomplete                []string    `json:"incomplete,omitempty"` // why Truncated: which source was unreadable
	Archived                  int         `json:"archived,omitempty"`
	Omitted                   int         `json:"omitted,omitempty"` // items evaluated but cut from the list (least urgent)
	Items                     []Item      `json:"items"`
}

func Evaluate(now time.Time, observations []Observation, free []string, complete bool) Report {
	r := Report{Trust: "UNTRUSTED", State: "ok", CapturedAt: now, HeartbeatThresholdSeconds: int64(HeartbeatThreshold / time.Second), FreeProjects: free, NextCheck: now.Add(CheckInterval), Truncated: !complete, Items: []Item{}}
	if r.FreeProjects == nil {
		r.FreeProjects = []string{}
	}
	if complete || len(free) > 0 {
		available := len(free) > 0
		r.IndependentWorkAvailable = &available
	}
	if !complete {
		r.State = "unknown"
	}
	for _, o := range observations {
		// Only abandoned workers age out; a stuck integration stays a blocker.
		if (o.Kind == "roadmap_run" || o.Kind == "agent_task") && !o.Running && !o.EvidenceAt.IsZero() && now.Sub(o.EvidenceAt) > ArchiveAfter {
			r.Archived++
			continue
		}
		i := Item{Kind: o.Kind, ID: o.ID, Path: o.Path, CurrentPhase: o.Phase, State: "ok", BlockedBy: append([]string{}, o.BlockedBy...), UnitState: o.UnitState, NextCheck: now.Add(CheckInterval), NextSafeAction: Action{Tool: "project_lease_status", Args: map[string]string{"path": o.Path}, Approval: "none"}}
		switch o.Kind {
		case "agent_task":
			i.NextSafeAction.Tool = "agent_task_status"
			i.NextSafeAction.Args = map[string]string{"id": o.ID}
		case "roadmap_run":
			i.NextSafeAction.Tool = "roadmap_run_status"
			i.NextSafeAction.Args = map[string]string{"id": o.ID}
		case "integration":
			i.NextSafeAction.Tool = "integration_status"
			i.NextSafeAction.Args = map[string]string{"path": o.Path, "id": o.ID}
			i.Stop = &Action{Tool: "integration_cancel", Args: map[string]string{"path": o.Path, "id": o.ID}, Approval: "none"}
		case "job":
			i.NextSafeAction.Tool = "job_status"
			i.NextSafeAction.Args = map[string]string{"job_id": o.ID}
			i.Stop = &Action{Tool: "job_cancel", Args: map[string]string{"job_id": o.ID}, Approval: "none"}
		}
		if o.Kind == "agent_task" && o.Running {
			i.Stop = &Action{Tool: "agent_task_cancel", Args: map[string]string{"id": o.ID}, Approval: "admin_session"}
		}
		if o.EvidenceAt.IsZero() || o.EvidenceAt.After(now) {
			i.State = "unknown"
		} else {
			age := int64(now.Sub(o.EvidenceAt) / time.Second)
			i.EvidenceAge = &age
		}
		if !o.Running && len(i.BlockedBy) == 0 && (o.Phase == "failed" || o.Phase == "interrupted" || o.Phase == "stopped" || strings.HasSuffix(o.Phase, ":stopped")) {
			i.BlockedBy = append(i.BlockedBy, "worker")
		}
		if len(i.BlockedBy) > 0 {
			i.State = "degraded"
		}
		if o.Running {
			if i.EvidenceAge != nil && now.Sub(o.EvidenceAt) >= HeartbeatThreshold {
				i.BlockedBy = append(i.BlockedBy, "stalled_heartbeat")
				i.State = "degraded"
			}
			if o.UnitState == "inactive" || o.UnitState == "failed" || o.UnitState == "not-found" {
				i.BlockedBy = append(i.BlockedBy, "worker")
				i.State = "degraded"
			} else if o.UnitState != "" && o.UnitState != "active" && i.State == "ok" {
				i.State = "unknown"
			}
		}
		if o.ResetAt.After(now) {
			i.NextCheck = o.ResetAt
		}
		if i.NextCheck.Before(r.NextCheck) {
			r.NextCheck = i.NextCheck
		}
		if i.State == "degraded" {
			r.State = "degraded"
		} else if i.State == "unknown" && r.State == "ok" {
			r.State = "unknown"
		}
		r.Items = append(r.Items, i)
	}
	return r
}

// ProjectRoot maps a job workdir to its immediate project directory.
func ProjectRoot(projects, workdir string) (string, bool) {
	rel, err := filepath.Rel(projects, filepath.Clean(workdir))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", false
	}
	name, _, _ := strings.Cut(rel, string(os.PathSeparator))
	return filepath.Join(projects, name), true
}

// Trim keeps the n most urgent items (degraded, then unknown, then ok, newest
// evidence first inside a state) and counts the rest in Omitted. State was
// already computed from every item, so a cut list hides nothing urgent.
func (r *Report) Trim(n int) {
	if len(r.Items) <= n {
		return
	}
	rank := map[string]int{"degraded": 0, "unknown": 1, "ok": 2}
	sort.SliceStable(r.Items, func(i, j int) bool {
		a, b := r.Items[i], r.Items[j]
		if rank[a.State] != rank[b.State] {
			return rank[a.State] < rank[b.State]
		}
		return a.EvidenceAge != nil && (b.EvidenceAge == nil || *a.EvidenceAge < *b.EvidenceAge)
	})
	r.Omitted += len(r.Items) - n
	r.Items = r.Items[:n]
}
