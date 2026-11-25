package missiontools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/agents"
	"mcpshka/internal/healthlayer"
	"mcpshka/internal/integrationqueue"
	"mcpshka/internal/jobs"
	"mcpshka/internal/mission"
	"mcpshka/internal/projectlease"
	"mcpshka/internal/roadmaprun"
	"mcpshka/internal/tooldef"
	"mcpshka/internal/unitbus"
)

// missionLimit items are shown, most urgent first; up to missionObserveCap
// observations are evaluated so a cut list never hides a blocker (MCP-169).
const (
	missionLimit      = 16
	missionObserveCap = 256
)

// Fixed, bounded sources only. No prompts, tails, tokens, owner identifiers or
// raw errors leave this projection; status reads never reconcile ownership.
type Deps struct {
	Manager               *jobs.Manager
	Tasks, Runs, Projects string
	Unit                  func(context.Context, string) string
}

type Reader struct{ d Deps }

var missionRunID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,100}-[0-9]{8}-[0-9]{6}-[0-9a-f]{4}$`)

func New(d Deps) *Reader {
	if d.Unit == nil {
		d.Unit = missionUnitState
	}
	return &Reader{d: d}
}

func missionUnitState(ctx context.Context, unit string) string {
	ctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	raw, err := unitbus.Output(ctx, "show", "--property=ActiveState", "--value", unit+".service")
	if err != nil {
		return "unknown"
	}
	switch s := strings.TrimSpace(string(raw)); s {
	case "active", "inactive", "failed", "activating", "deactivating":
		return s
	}
	return "unknown"
}

func missionBlockers(text string) []string {
	text = strings.ToLower(text)
	var out []string
	for _, b := range []struct {
		name    string
		needles []string
	}{
		{"lease", []string{"lease", "writer"}}, {"quota", []string{"quota", "usage limit", "лимит", "лимиты"}}, {"Face ID", []string{"face id", "approval", "admin_session", "admin session"}}, {"CI", []string{"ci ", "ci_", "test_failed", "test failed", "github", "gitverse"}}, {"worker", []string{"worker", "interrupted", "signal:"}},
	} {
		for _, n := range b.needles {
			if strings.Contains(text, n) {
				out = append(out, b.name)
				break
			}
		}
	}
	return out
}

// supersededAgentTask drops terminal agent history that no longer blocks:
// a quota after its reset or after a later run of the same agent, and any
// other failure older than a day.
func supersededAgentTask(o mission.Observation, agentLatestStart, finished, now time.Time) bool {
	if o.Phase == "quota" {
		return (!o.ResetAt.IsZero() && !o.ResetAt.After(now)) || (!finished.IsZero() && agentLatestStart.After(finished))
	}
	return !finished.IsZero() && now.Sub(finished) > 24*time.Hour
}

func latestMissionTime(times ...time.Time) time.Time {
	var at time.Time
	for _, t := range times {
		if t.After(at) {
			at = t
		}
	}
	return at
}

func (s *Reader) Read(ctx context.Context) (mission.Report, error) {
	ctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	now := time.Now().UTC()
	complete := true
	var reasons []string
	// miss records why the report is incomplete, so "unknown" names its cause.
	miss := func(reason string) {
		complete = false
		if !slices.Contains(reasons, reason) && len(reasons) < 8 {
			reasons = append(reasons, reason)
		}
	}
	observations := []mission.Observation{}
	busy := map[string]bool{}
	latest := map[string]time.Time{}
	ownerEvidence := map[string]time.Time{}
	// Quota is per agent account, not per worktree: a later run of the same
	// agent or a passed reset resolves it (BUG-20261024-87).
	agentStarted := map[string]time.Time{}
	taskAgent := map[string]string{}
	taskFinished := map[string]time.Time{}
	leased := map[string]bool{}
	add := func(o mission.Observation) {
		if len(o.Path) > 512 || len(o.ID) > 128 || len(o.Phase) > 160 {
			miss("observation_oversize")
			return
		}
		busy[o.Path] = true
		if len(observations) < missionObserveCap {
			observations = append(observations, o)
		} else {
			miss("observation_limit")
		}
	}
	for _, kind := range []string{"agent_task", "roadmap_run"} {
		dir := s.d.Tasks
		if kind == "roadmap_run" {
			dir = s.d.Runs
		}
		entries, err := mission.Records(dir)
		if err != nil {
			miss(kind + "_dir")
			continue
		}
		for _, entry := range entries {
			if ctx.Err() != nil {
				miss("timeout")
				break
			}
			id, ok := strings.CutSuffix(entry.Name(), ".json")
			if !ok || entry.IsDir() {
				continue
			}
			if kind == "agent_task" {
				if !agents.ValidTaskID(id) {
					continue
				}
				var t agents.Task
				if mission.ReadJSON(filepath.Join(dir, entry.Name()), &t) != nil || t.ID != id {
					miss("agent_task_record")
					continue
				}
				latestKey := kind + ":" + t.Path
				latest[latestKey] = latestMissionTime(latest[latestKey], t.StartedAt, t.FinishedAt)
				agentStarted[t.Agent] = latestMissionTime(agentStarted[t.Agent], t.StartedAt)
				taskAgent[id], taskFinished[id] = t.Agent, t.FinishedAt
				if t.State == "done" {
					continue
				}
				if _, err := s.d.Manager.Policy().CheckWorkdir(t.Path); err != nil {
					// A finished run whose worktree was cleaned up is history,
					// not missing evidence (MCP-166).
					if errors.Is(err, os.ErrNotExist) && !(t.State == "running") {
						continue
					}
					miss("agent_task_workdir")
					continue
				}
				// Keep unresolved terminal blockers until superseded by a later run.
				o := mission.Observation{Kind: kind, ID: id, Path: t.Path, Phase: t.State, EvidenceAt: latestMissionTime(t.StartedAt, t.FinishedAt), ResetAt: t.ResetAt, Running: t.State == "running", BlockedBy: missionBlockers(t.Error)}
				if t.State == "quota" && len(o.BlockedBy) == 0 {
					o.BlockedBy = []string{"quota"}
				}
				if o.Running {
					o.UnitState = s.d.Unit(ctx, "mcpshka-agent-task-"+id)
				}
				add(o)
			} else {
				if !missionRunID.MatchString(id) {
					continue
				}
				var j roadmaprun.Journal
				if mission.ReadJSON(filepath.Join(dir, entry.Name()), &j) != nil || j.ID != id {
					miss("roadmap_run_record")
					continue
				}
				latestKey := kind + ":" + j.Path
				latest[latestKey] = latestMissionTime(latest[latestKey], j.StartedAt, j.UpdatedAt)
				if j.State == "done" {
					continue
				}
				if _, err := s.d.Manager.Policy().CheckWorkdir(j.Path); err != nil {
					// A finished run whose worktree was cleaned up is history,
					// not missing evidence (MCP-166).
					if errors.Is(err, os.ErrNotExist) && !(j.State == "running") {
						continue
					}
					miss("roadmap_run_workdir")
					continue
				}
				o := mission.Observation{Kind: kind, ID: id, Path: j.Path, Phase: j.State, EvidenceAt: latestMissionTime(j.StartedAt, j.UpdatedAt), Running: j.State == "running", BlockedBy: missionBlockers(j.StopReason)}
				if len(j.Phases) > 0 {
					p := j.Phases[len(j.Phases)-1]
					o.Phase = p.ID + ":" + j.State
					o.EvidenceAt = latestMissionTime(o.EvidenceAt, p.StartedAt, p.FinishedAt)
					o.BlockedBy = append(o.BlockedBy, missionBlockers(p.Note)...)
					if strings.HasPrefix(p.Note, "ждём до ") {
						fields := strings.Fields(p.Note)
						if len(fields) > 2 {
							o.ResetAt, _ = time.Parse(time.RFC3339, fields[2])
						}
					}
				}
				if o.Running {
					o.UnitState = s.d.Unit(ctx, "mcpshka-roadmap-"+id)
				}
				add(o)
			}
		}
	}
	db := s.d.Manager.Database()
	if db == nil {
		miss("state_db")
	} else {
		for _, prefix := range []string{"leases/", "integration/v1/queues/"} {
			// Leases of removed worktrees stay in the store: page past the first 32.
			keys, truncated, err := db.ListKeys(ctx, prefix, 512)
			if err != nil || truncated {
				miss(strings.TrimSuffix(prefix, "/") + "_scan")
			}
			for _, key := range keys {
				raw, err := db.GetContext(ctx, key)
				if err != nil {
					miss("state_read")
					continue
				}
				if prefix == "leases/" {
					// Decode only public fields, never the stored holder token.
					var l projectlease.Status
					if json.Unmarshal(raw, &l) != nil || l.ProjectRoot == "" {
						miss("lease_record")
						continue
					}
					if _, err := s.d.Manager.Policy().CheckWorkdir(l.ProjectRoot); err != nil {
						// MCP-167: a lease on a removed worktree blocks nothing.
						if !errors.Is(err, os.ErrNotExist) {
							miss("lease_workdir")
						}
						continue
					}
					o := mission.Observation{Kind: "lease", ID: l.LeaseID, Path: l.ProjectRoot, Phase: "writer_held", EvidenceAt: latestMissionTime(l.AcquiredAt, l.HeartbeatAt), Running: true, UnitState: ""}
					// A fresh interactive heartbeat is activity evidence, not unit proof.
					ownerEvidence[l.ProjectRoot+":"+l.Owner] = o.EvidenceAt
					leased[l.ProjectRoot] = true
					add(o)
				} else {
					var q struct {
						Version int                            `json:"version"`
						Items   []integrationqueue.RuntimeItem `json:"items"`
					}
					if len(raw) > 4<<20 || json.Unmarshal(raw, &q) != nil || q.Version != 1 {
						miss("integration_queue")
						continue
					}
					for _, item := range q.Items {
						if item.State == integrationqueue.Cancelled || item.State == integrationqueue.Integrated {
							continue
						}
						if !integrationqueue.ValidRequest(item.Request) {
							miss("integration_request")
							continue
						}
						if _, err := s.d.Manager.Policy().CheckWorkdir(item.Request.Path); err != nil {
							miss("integration_workdir")
							continue
						}
						// Queue v1 has no timestamp or test/approval proof. Do not invent age
						// or claim that CI/Face ID/worker succeeded from mere admission.
						add(mission.Observation{Kind: "integration", ID: item.Request.ID, Path: item.Request.Path, Phase: string(item.State)})
					}
				}
			}
		}
	}
	filtered := observations[:0]
	for _, o := range observations {
		if !o.Running && (o.Kind == "agent_task" || o.Kind == "roadmap_run") && o.EvidenceAt.Before(latest[o.Kind+":"+o.Path]) {
			continue
		}
		if o.Kind == "agent_task" && !o.Running && supersededAgentTask(o, agentStarted[taskAgent[o.ID]], taskFinished[o.ID], now) {
			continue
		}
		if o.Kind == "roadmap_run" {
			o.EvidenceAt = latestMissionTime(o.EvidenceAt, ownerEvidence[o.Path+":roadmap-run:"+o.ID])
		}
		if o.Kind == "integration" && leased[o.Path] {
			o.BlockedBy = append(o.BlockedBy, "lease")
		}
		filtered = append(filtered, o)
	}
	observations = filtered
	// Recompute occupancy after superseded task failures are removed.
	busy = map[string]bool{}
	for _, o := range observations {
		busy[o.Path] = true
	}
	// Only advertise candidates after all blocking registries were read. These
	// are advisory scopes; acquiring a lease is still mandatory before writing.
	// A running job only occupies its own project; an unknown or uncounted
	// one leaves occupancy unproven.
	activity := s.d.Manager.ProjectActivityReadOnly(string(os.PathSeparator), 64)
	if activity.Truncated || activity.UnknownCount > 0 {
		miss("job_activity")
	}
	for _, j := range activity.Jobs {
		path := j.Workdir
		if root, ok := mission.ProjectRoot(s.d.Projects, j.Workdir); ok {
			busy[root] = true
			path = root
		}
		// Queue jobs (tests, builds, exec) join the list with their stop. The
		// live process is the evidence, so a long test is not "stalled".
		o := mission.Observation{Kind: "job", ID: j.ID, Path: path, Phase: string(j.State), EvidenceAt: now}
		if j.State == jobs.Unknown {
			o.BlockedBy = []string{"unknown_job"}
		}
		if len(observations) < missionObserveCap {
			observations = append(observations, o)
		} else {
			miss("observation_limit")
		}
	}
	var free []string
	entries, err := mission.Files(s.d.Projects)
	if err != nil {
		miss("projects_dir")
	}
	if ctx.Err() != nil {
		miss("timeout")
	}
	if complete {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			path := filepath.Join(s.d.Projects, entry.Name())
			if busy[path] {
				continue
			}
			if _, err := s.d.Manager.Policy().CheckWorkdir(path); err != nil {
				continue
			}
			if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
				free = append(free, path)
				if len(free) == 8 {
					break
				}
			}
		}
	}
	report := mission.Evaluate(now, observations, free, complete)
	report.Incomplete = reasons
	report.Trim(missionLimit)
	return report, nil
}

func Register(add func(tooldef.Def), s *Reader, controllers ...*mission.Controller) {
	add(tooldef.New(&mcp.Tool{Name: "mission_status", Description: "Read a durable SQLite mission by optional id; without id, return the recorded mission index plus bounded read-only progress from agent_task, roadmap_run, integration queues, project leases and queue jobs (tests, builds, exec) — one list of what runs; stop is the exact call that stops an item. evidence_age is seconds (null when missing/future); stalled heartbeat after 30min or a stopped worker is degraded, never authorization to restart or take a lease. Unknown/truncated sources stay explicit. Pending integration without timestamp/approval/CI proof remains unknown; blocker classes use recorded errors, never guessed account-wide state. Independent work lists advisory unleased immediate /root/repos projects/worktrees; acquire a lease before writing. No prompts, secrets, reconciliation or background work.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in mission.StatusInput) (*mcp.CallToolResult, mission.Report, error) {
		out, err := s.Read(ctx)
		if err == nil && len(controllers) > 0 {
			if in.ID != "" {
				snapshot, e := controllers[0].Status(ctx, in.ID)
				out.Mission, err = &snapshot, e
			} else {
				var indexErr error
				out.Missions, out.MissionsTruncated, indexErr = controllers[0].Index()
				if indexErr != nil {
					out.MissionsTruncated = true
				}
			}
		}
		return nil, mission.WithProgress(out), err
	}))
}

func HealthProbe(s *Reader) healthlayer.Probe {
	return healthlayer.Probe{Name: "mission", NextSafeAction: &healthlayer.Action{Tool: "mission_status", Args: json.RawMessage("{}"), Approval: "none"}, Read: func(ctx context.Context) (healthlayer.Sample, error) {
		r, err := s.Read(ctx)
		if err != nil {
			return healthlayer.Sample{}, err
		}
		counts := map[string]int{}
		for _, item := range r.Items {
			counts[item.State]++
		}
		data := map[string]any{"items": len(r.Items), "states": counts, "independent_work_available": r.IndependentWorkAvailable, "next_check": r.NextCheck, "truncated": r.Truncated}
		if len(r.Items) > 0 {
			selected := r.Items[0]
			for _, item := range r.Items {
				if item.State == "degraded" {
					selected = item
					break
				}
			}
			data["current_phase"] = selected.CurrentPhase
			data["evidence_age"] = selected.EvidenceAge
			data["blocked_by"] = selected.BlockedBy
			data["next_safe_action"] = selected.NextSafeAction
		}
		raw, err := json.Marshal(data)
		return healthlayer.Sample{State: r.State, Data: raw}, err
	}}
}
