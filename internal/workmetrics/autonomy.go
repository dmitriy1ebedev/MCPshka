package workmetrics

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"mcpshka/internal/audit"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/memory"
	"mcpshka/internal/mission"
	"mcpshka/internal/repopath"
	"mcpshka/internal/statestore"
	"mcpshka/internal/workledger"
)

type Period struct {
	Since        time.Time `json:"since"`
	Until        time.Time `json:"until"`
	Project      string    `json:"project,omitempty"`
	StuckSeconds int       `json:"stuck_seconds"`
}

type Average struct {
	Mean    *float64 `json:"mean"`
	Samples int      `json:"samples"`
}

func (a *Average) add(v float64) {
	mean := v
	if a.Mean != nil {
		mean = *a.Mean + (v-*a.Mean)/float64(a.Samples+1)
	}
	a.Mean = &mean
	a.Samples++
}

type Autonomy struct {
	Period
	At                        time.Time `json:"at"`
	OwnerNudges               *int      `json:"owner_nudges"`
	OwnerSource               string    `json:"owner_source"`
	FaceIDRequests            *int      `json:"face_id_requests"`
	Missions                  int       `json:"missions"`
	Accepted                  int       `json:"accepted"`
	WithoutManualResume       Average   `json:"without_manual_resume"`
	CallsPerAccepted          Average   `json:"calls_per_accepted"`
	FirstResultSeconds        Average   `json:"first_result_seconds"`
	AcceptanceSeconds         Average   `json:"acceptance_seconds"`
	StuckUnknownPending       int       `json:"stuck_unknown_pending"`
	DuplicateMutationKeys     int       `json:"duplicate_mutation_keys"`
	DoneWithoutEvidence       int       `json:"done_without_evidence"`
	RunsWithoutEffect         int       `json:"runs_without_effect"`
	AgentRunsWithoutGitChange int       `json:"agent_runs_without_git_change"`
	Host                      *Load     `json:"host,omitempty"`
	Notes                     []string  `json:"notes,omitempty"`
}

type Sources struct {
	DB        *statestore.DB
	Audit     *audit.Logger
	MemoryDir string
	AgentDir  string
}

func ProjectRoot(path string) string {
	if repo := repopath.Worktree(path); repo != "" {
		return repo
	}
	return path
}

func (p Period) contains(t time.Time) bool {
	return !t.IsZero() && !t.Before(p.Since) && t.Before(p.Until)
}

func (p Period) project(path string) bool {
	return p.Project == "" || p.Project == ProjectRoot(path)
}

func (p Period) stale(t time.Time) bool {
	return p.contains(t) && p.Until.Sub(t) >= time.Duration(p.StuckSeconds)*time.Second
}

func (s Sources) Measure(ctx context.Context, p Period, now time.Time) Autonomy {
	a := Autonomy{Period: p, At: now.UTC()}
	calls := a.readAudit(ctx, s.Audit)
	a.readState(ctx, s.DB, calls)
	a.RunsWithoutEffect = calls.noEffect
	a.readAgents(ctx, s.AgentDir)
	a.Notes = append(a.Notes, "agent no-change means unchanged Git HEAD; host samples describe the controller at capture")
	chat, err := memory.MeasureNudges(ctx, s.MemoryDir, p.Project, p.Since, p.Until)
	switch {
	case err == nil && chat.Chunks > 0:
		a.OwnerNudges, a.OwnerSource = &chat.Nudges, "imported chat owner turns (chunk timestamps)"
		if chat.Truncated {
			a.Notes = append(a.Notes, "imported chat scan reached its budget")
		}
		if chat.Unscoped {
			a.OwnerSource = "imported chat owner turns, all projects (no chat names this project)"
		} else if p.Project != "" {
			a.Notes = append(a.Notes, "imported chats are attributed through project-tagged records or by naming the project")
		}
	case calls.sessions > 0:
		a.OwnerNudges, a.OwnerSource = &calls.resumes, "session journal: explicit mission resume calls (proxy)"
	default:
		a.OwnerSource = "unavailable: no attributed chat or session coverage"
	}
	a.Host = observeLoad(ctx, s.DB)
	return a
}

func (a *Autonomy) scan(ctx context.Context, db *statestore.DB, prefix string, visit func(string, []byte) error) {
	n := 0
	if err := db.ScanContext(ctx, prefix, func(key string, raw []byte) error {
		n++
		if n > scanBudget {
			return errBudget
		}
		return visit(key, raw)
	}); err != nil {
		a.Notes = append(a.Notes, prefix+" scan incomplete")
	}
}

func (a *Autonomy) readState(ctx context.Context, db *statestore.DB, calls callIndex) {
	if db == nil {
		a.Notes = append(a.Notes, "SQLite state unavailable")
		return
	}
	a.scan(ctx, db, "missions/", func(_ string, raw []byte) error {
		var m mission.Snapshot
		if err := json.Unmarshal(raw, &m); err != nil {
			return err
		}
		if !a.project(m.Path) {
			return nil
		}
		a.mission(m, calls)
		return nil
	})
	if a.Project == "" {
		n, err := workledger.UnresolvedAt(ctx, db, a.Since, a.Until, time.Duration(a.StuckSeconds)*time.Second, scanBudget)
		if err != nil {
			a.Notes = append(a.Notes, "work ledger scan incomplete")
		} else {
			a.StuckUnknownPending += n
		}
	}
	counts := map[[2]string]int{}
	unscoped := false
	a.scan(ctx, db, "idempotency/", func(_ string, raw []byte) error {
		var e idempotency.Entry
		if err := json.Unmarshal(raw, &e); err != nil {
			return err
		}
		if !a.contains(e.StartedAt) {
			return nil
		}
		if a.Project != "" {
			dir := calls.mutations[e.KeyHash]
			unscoped = unscoped || dir == ""
			if !a.project(dir) {
				return nil
			}
		}
		if (e.State == idempotency.Pending || e.State == "unknown") && a.stale(e.StartedAt) {
			a.StuckUnknownPending++
		}
		if e.Fingerprint != "" && e.Operation != "" {
			counts[[2]string{e.Operation, e.Fingerprint}]++
		}
		return nil
	})
	for _, n := range counts {
		a.DuplicateMutationKeys += n - 1
	}
	if unscoped {
		a.Notes = append(a.Notes, "project mutation counts cover only receipts with an attributed audit event")
	}
}

func (a *Autonomy) mission(m mission.Snapshot, calls callIndex) {
	if len(m.History) == 0 || (m.History[0].Action != "start" && m.History[0].Action != "intake") {
		if a.contains(m.UpdatedAt) {
			a.Notes = appendOnce(a.Notes, "some mission start or resume history is missing")
			if m.State == "DONE" && !provenMission(m) {
				a.DoneWithoutEvidence++
			}
		}
		return
	}
	start := m.History[0].At
	if !a.contains(start) {
		return
	}
	a.Missions++
	resumed, done := false, time.Time{}
	for _, e := range m.History {
		if !a.contains(e.At) {
			continue
		}
		if e.Action == "resume" {
			resumed = true
		}
		if e.State == "DONE" && done.IsZero() {
			done = e.At
		}
	}
	first := time.Time{}
	for _, cp := range m.Checkpoints {
		if len(cp.Gates) > 0 && !cp.At.Before(start) && a.contains(cp.At) && (first.IsZero() || cp.At.Before(first)) {
			first = cp.At
		}
	}
	if !first.IsZero() {
		a.FirstResultSeconds.add(first.Sub(start).Seconds())
	}
	if done.IsZero() {
		return
	}
	if !provenMission(m) {
		a.DoneWithoutEvidence++
		return
	}
	a.Accepted++
	a.AcceptanceSeconds.add(done.Sub(start).Seconds())
	if resumed {
		a.WithoutManualResume.add(0)
	} else {
		a.WithoutManualResume.add(1)
	}
	if n, ok := calls.forMission(m.ID, start, done); ok {
		a.CallsPerAccepted.add(float64(n))
	}
}

func provenMission(m mission.Snapshot) bool {
	if len(m.Steps) == 0 || len(m.Checkpoints) != len(m.Steps) {
		return false
	}
	for i, cp := range m.Checkpoints {
		if cp.Step != m.Steps[i].ID || cp.At.IsZero() || len(cp.Gates) == 0 {
			return false
		}
	}
	return true
}

func appendOnce(old []string, note string) []string {
	for _, s := range old {
		if s == note {
			return old
		}
	}
	return append(old, note)
}

func (a Autonomy) Brief() string {
	nudges, approvals := "н/д", "н/д"
	if a.OwnerNudges != nil {
		nudges = fmt.Sprint(*a.OwnerNudges)
	}
	if a.FaceIDRequests != nil {
		approvals = fmt.Sprint(*a.FaceIDRequests)
	}
	return fmt.Sprintf("Автономность за 24 ч — вмешательства %s, Face ID %s, приёмка %d/%d, зависло %d, DONE без доказательств %d, без эффекта %d",
		nudges, approvals, a.Accepted, a.Missions, a.StuckUnknownPending, a.DoneWithoutEvidence, a.RunsWithoutEffect)
}

func BaselineKey(project string, day time.Time) string {
	scope := "all"
	if project != "" {
		id := sha256.Sum256([]byte(project))
		scope = fmt.Sprintf("project-%x", id[:16])
	}
	return "workmetrics/baseline/v1/" + scope + "/" + day.UTC().Format(time.DateOnly) + ".json"
}
