package hosttriage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"mcpshka/internal/diagprovider"
	"mcpshka/internal/dockerops"
	"mcpshka/internal/heavyqueue"
	"mcpshka/internal/homeguard"
	"mcpshka/internal/issues"
	"mcpshka/internal/jobs"
	"mcpshka/internal/selfheal"
	"mcpshka/internal/systemdsvc"
)

// host_triage answers «разберись» from a chat: what is wrong on the host, the
// evidence, and the exact MCPshka call that fixes it. It never acts itself;
// the chat runs the actions, then calls host_triage again to re-check.

type TriageAction struct {
	Tool     string         `json:"tool"`
	Args     map[string]any `json:"args,omitempty"`
	Approval string         `json:"approval"` // none, face_id, owner
}

type TriageProblem struct {
	Severity string        `json:"severity"` // critical, warning, info
	Kind     string        `json:"kind"`
	Subject  string        `json:"subject"`
	Evidence string        `json:"evidence"`
	Action   *TriageAction `json:"action,omitempty"`
	Note     string        `json:"note,omitempty"`
}

type HostTriageResult struct {
	LastSelfHeal *selfheal.Incident `json:"last_self_heal,omitempty"`
	CheckedAt    time.Time          `json:"checked_at"`
	Healthy      bool               `json:"healthy"`
	Problems     []TriageProblem    `json:"problems"`
	Pressure     PhaseGateCheck     `json:"pressure"`
	NextAction   string             `json:"next_action"`
	// Healthy covers the host only. Execution is the separate verdict of the
	// executor, jobs, broker and workflow sources (MCP-046): a healthy host
	// with a degraded executor no longer reads as "nothing to fix".
	Execution *ExecutionReadiness `json:"execution,omitempty"`
	// DIAG is attached only when the host is not healthy.
	DiagState string                    `json:"diag_state,omitempty" jsonschema:"done, running, none (start it: problems kind=diagnosis) or unavailable: <reason>."`
	Diagnosis *diagprovider.ReportBrief `json:"diagnosis,omitempty" jsonschema:"Non-ok findings of the newest DIAG host run (UNTRUSTED)."`
}

// procSysRoot is swapped by tests.
var procSysRoot = "/proc/sys"

// kernelLinkProtection flags fs.protected_* switched off: any local user can
// then plant links in sticky world-writable directories (/tmp) that root
// follows, and a hard link to a foreign file survives a root chown walk. The
// stock kernel ships them on; this box's vendor kernel had all four at 0.
func kernelLinkProtection(root string) []TriageProblem {
	want := map[string]string{"protected_hardlinks": "1", "protected_symlinks": "1", "protected_fifos": "1", "protected_regular": "1"}
	var off []string
	for _, name := range []string{"protected_hardlinks", "protected_symlinks", "protected_fifos", "protected_regular"} {
		raw, err := os.ReadFile(filepath.Join(root, "fs", name))
		if err == nil && strings.TrimSpace(string(raw)) < want[name] {
			off = append(off, "fs."+name+"="+strings.TrimSpace(string(raw)))
		}
	}
	if len(off) == 0 {
		return nil
	}
	return []TriageProblem{{Severity: "warning", Kind: "kernel-hardening", Subject: "fs.protected_*", Evidence: strings.Join(off, ", "),
		Action: &TriageAction{Tool: "owner", Approval: "owner", Args: map[string]any{"run": "bash /root/mcpshka-run/harden-links.sh"}},
		Note:   "root-only sysctl; the script writes /etc/sysctl.d/60-mcpshka-links.conf and applies it"}}
}

var severityRank = map[string]int{"critical": 0, "warning": 1, "info": 2}

func failedUnits(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/systemctl", "list-units", "--failed", "--no-legend", "--plain", "--no-pager").Output()
	if err != nil {
		return nil, err
	}
	return parseFailedUnits(string(out)), nil
}

// parseFailedUnits reads `systemctl list-units --failed --plain` rows
// (UNIT LOAD ACTIVE SUB …). A masked unit was retired on purpose (MCP-106:
// openvpn@mandatory after the move to Lane) and is not a live problem.
func parseFailedUnits(out string) []string {
	var units []string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || len(units) >= 64 || (len(f) > 1 && f[1] == "masked") {
			continue
		}
		units = append(units, f[0])
	}
	return units
}

// serviceDown: an allowlisted unit that should run and does not. A unit
// disabled or masked on purpose (laned until LANE ships; openvpn@mandatory
// retired by MCP-106) is not down: a masked one even reported critical with a
// journal_read hint the tool cannot serve (ChatGPT audit 03.11.2026).
func serviceDown(active, load, fileState string) bool {
	switch {
	case active == "active", load == "not-found", load == "masked", fileState == "masked", fileState == "masked-runtime":
		return false
	case active == "inactive" && fileState == "disabled":
		return false
	}
	return true
}

// StoppedOnPurpose: a clean exit (code 0, or 143 from docker stop's SIGTERM)
// of a container whose restart policy is "no".
func StoppedOnPurpose(ctx context.Context, docker *dockerops.Service, state, status, name string) bool {
	if state != "exited" || !(strings.HasPrefix(status, "Exited (0)") || strings.HasPrefix(status, "Exited (143)")) {
		return false
	}
	info, err := docker.Inspect(ctx, name)
	return err == nil && (info.RestartPolicy == "no" || info.RestartPolicy == "")
}

func Run(ctx context.Context, docker *dockerops.Service, services *systemdsvc.Service, manager *jobs.Manager, heal ...*selfheal.Service) HostTriageResult {
	out := HostTriageResult{CheckedAt: time.Now().UTC(), Problems: []TriageProblem{}}
	add := func(p TriageProblem) { out.Problems = append(out.Problems, p) }

	if list, err := docker.List(ctx, dockerops.MaxListEntries); err != nil {
		add(TriageProblem{Severity: "warning", Kind: "docker", Subject: "docker", Evidence: "container list unavailable: " + err.Error(), Action: &TriageAction{Tool: "docker_status", Approval: "none"}})
	} else {
		for _, c := range list.Containers {
			status := strings.ToLower(c.Status)
			bad := ""
			switch {
			case strings.Contains(status, "unhealthy"):
				bad = "unhealthy"
			case c.State == "restarting":
				bad = "restart loop"
			case c.State == "exited" || c.State == "dead":
				bad = c.State
			}
			if bad == "" {
				continue
			}
			p := TriageProblem{Severity: "critical", Kind: "container", Subject: c.Name, Evidence: bad + ": " + c.Status}
			if StoppedOnPurpose(ctx, docker, c.State, c.Status, c.Name) {
				// docker stop + restart policy "no" is the owner's decision, not a fault.
				p.Severity = "info"
				p.Evidence += "; restart policy no"
				p.Note = "stopped on purpose; start it with docker_action only if the owner wants it back"
				add(p)
				continue
			}
			if docker.RestartAllowed(c.Name) {
				p.Action = &TriageAction{Tool: "docker_action", Args: map[string]any{"container": c.Name, "action": "restart"}, Approval: "none"}
				p.Note = "read docker_logs first if it fails again within minutes"
			} else {
				p.Action = &TriageAction{Tool: "docker_logs", Args: map[string]any{"container": c.Name}, Approval: "none"}
				p.Note = "not in the restart allowlist; the owner decides"
			}
			add(p)
		}
	}

	if ok, why := heavyqueue.HomeReady(); !ok && !strings.Contains(why, "memory_") {
		// homeguard (broker) saw Zigbee/MQTT degrade; heavy work waits for it.
		add(TriageProblem{Severity: "critical", Kind: "home", Subject: "zigbee/mqtt", Evidence: why, Action: &TriageAction{Tool: "docker_logs", Args: map[string]any{"container": "zigbee2mqtt"}, Approval: "none"}, Note: "heavy work is held until the home recovers"})
	} else if !ok {
		add(memoryProblem(why))
	} else if cur, err := homeguard.ReadMem(time.Now()); err == nil {
		if why := homeguard.Thrash(homeguard.MemSample{}, cur); why != "" {
			add(memoryProblem(why))
		}
	}
	if cur, err := homeguard.ReadMem(time.Now()); err == nil {
		if p, ok := memoryBudget(cur.TotalKB); ok {
			add(p)
		}
	}
	if p, ok := volatileJournal(); ok {
		add(p)
	}
	// MCP-180: kernel root mount flags are observed but never changed here.
	// Until a tested OOB rollback exists, no remount/reboot action is offered.
	for _, p := range rootMountRisk() {
		add(p)
	}
	if a := homeguard.Anomalies(homeguard.ReadBaseline()); len(a) > 0 {
		// ROADMAP P20: the host is unlike its own last day and the same hour yesterday.
		add(TriageProblem{Severity: "warning", Kind: "anomaly", Subject: "host", Evidence: strings.Join(a, "; "),
			Action: &TriageAction{Tool: "host_diagnose", Args: map[string]any{"symptom": "slow"}, Approval: "none"}, Note: "robust z-score over 24 h and the same hour yesterday; nothing is changed"})
	}

	if list, err := services.List(ctx); err == nil {
		for _, s := range list.Services {
			// A unit disabled on purpose (laned until LANE ships) is not down;
			// it showed critical once 0.1.174 made it status-readable.
			if !serviceDown(s.ActiveState, s.LoadState, s.UnitFileState) {
				continue
			}
			p := TriageProblem{Severity: "critical", Kind: "service", Subject: s.Service, Evidence: fmt.Sprintf("%s/%s result=%s", s.ActiveState, s.SubState, s.Result)}
			if cause := lastJournalError(ctx, services, s.Service); cause != "" {
				// A restart loop repeats this failure: show the cause, not a restart.
				p.Evidence += "; last error: " + cause
				p.Action = &TriageAction{Tool: "journal_read", Args: map[string]any{"service": s.Service}, Approval: "none"}
				p.Note = "the unit keeps failing on start; fix the cause above before restarting"
			} else if services.RestartAllowed(s.Service) {
				p.Action = &TriageAction{Tool: "service_action", Args: map[string]any{"service": s.Service, "action": "restart"}, Approval: "none"}
			} else {
				p.Action = &TriageAction{Tool: "journal_read", Args: map[string]any{"service": s.Service}, Approval: "none"}
				p.Note = "not in the restart allowlist; the owner decides"
			}
			add(p)
		}
	}

	if units, err := failedUnits(ctx); err == nil {
		for _, u := range units {
			p := TriageProblem{Severity: "warning", Kind: "failed_unit", Subject: u, Evidence: "systemd unit in failed state"}
			if strings.HasPrefix(u, "mcpshka-executor-") || strings.HasPrefix(u, "mcpshka-recipe-prepare-") || strings.HasPrefix(u, "mcpshka-lease-recovery-") || strings.HasPrefix(u, "mcpshka-integration-admin-") || strings.HasPrefix(u, "mcpshka-agent-task-") {
				p.Severity, p.Note = "info", "finished MCPshka worker unit; evidence only, cleared on the next fence"
			} else if services.StatusAllowed(u) {
				p.Action = &TriageAction{Tool: "journal_read", Args: map[string]any{"service": u}, Approval: "none"}
				if cause := lastJournalError(ctx, services, u); cause != "" {
					p.Evidence += "; last error: " + cause
				}
			} else {
				p.Note = "outside the MCPshka journal allowlist; the owner reads it or adds the unit to MCPSHKA_SYSTEMD_SERVICES"
			}
			add(p)
		}
	}

	activity := manager.RegistrySummary(32)
	for _, problem := range activityProblems(activity) {
		add(problem)
	}

	for _, p := range kernelLinkProtection(procSysRoot) {
		add(p)
	}
	for _, p := range configProblems(configDir) {
		add(p)
	}
	for _, p := range gitOwnerProblems(reposDir) {
		add(p)
	}

	if len(heal) > 0 && heal[0] != nil {
		attachSelfHealTriage(ctx, &out, heal[0])
	}

	out.Pressure = Pressure()
	if out.Pressure.Status != "PASS" {
		add(TriageProblem{Severity: "warning", Kind: "pressure", Subject: "host", Evidence: out.Pressure.Detail, Action: &TriageAction{Tool: "docker_resources", Approval: "none"}, Note: "find the heaviest container; defer heavy jobs meanwhile"})
	}

	sort.SliceStable(out.Problems, func(i, j int) bool {
		return severityRank[out.Problems[i].Severity] < severityRank[out.Problems[j].Severity]
	})
	out.Healthy = true
	for _, p := range out.Problems {
		if p.Severity != "info" {
			out.Healthy = false
		}
	}
	switch {
	case out.Healthy:
		out.NextAction = "nothing to fix"
	case out.Problems[0].Action != nil:
		a := out.Problems[0].Action
		out.NextAction = fmt.Sprintf("%s %v for %s, then call host_triage again", a.Tool, a.Args, out.Problems[0].Subject)
	default:
		out.NextAction = "report " + out.Problems[0].Subject + " to the owner"
	}
	return out
}

// activityProblems makes incomplete job evidence visible. RegistrySummary is
// bounded, but UnknownCount is computed across the entire live registry; an
// UNKNOWN beyond the first page must not become a false healthy=true result.
func activityProblems(activity jobs.ProjectActivity) []TriageProblem {
	var out []TriageProblem
	visibleUnknown := 0
	for _, j := range activity.Jobs {
		if j.State != jobs.Unknown {
			continue
		}
		visibleUnknown++
		out = append(out, TriageProblem{Severity: "warning", Kind: "job", Subject: j.ID, Evidence: "MCPshka job in UNKNOWN", Action: &TriageAction{Tool: "job_status", Args: map[string]any{"job_id": j.ID}, Approval: "none"}, Note: "job_status reconciles; never replay the original call"})
	}
	if activity.UnknownCount > visibleUnknown {
		out = append(out, TriageProblem{Severity: "warning", Kind: "jobs_evidence", Subject: "MCPshka job registry", Evidence: fmt.Sprintf("%d UNKNOWN jobs are outside the bounded page", activity.UnknownCount-visibleUnknown), Action: &TriageAction{Tool: "health_snapshot", Args: map[string]any{}, Approval: "none"}, Note: "do not declare the executor healthy from a partial page"})
	}
	if activity.Truncated {
		out = append(out, TriageProblem{Severity: "warning", Kind: "jobs_evidence", Subject: "MCPshka job registry", Evidence: fmt.Sprintf("bounded registry page contains %d of %d active jobs", len(activity.Jobs), activity.Count), Action: &TriageAction{Tool: "health_snapshot", Args: map[string]any{}, Approval: "none"}, Note: "incomplete job evidence; inspect individual job_status for affected jobs"})
	}
	return out
}

// lastJournalError returns the newest failure line of an allowlisted unit in
// the last ten minutes, trimmed for evidence; empty when none or unreadable.
func lastJournalError(ctx context.Context, services *systemdsvc.Service, unit string) string {
	if !services.StatusAllowed(unit) {
		return ""
	}
	page, err := services.JournalRead(ctx, unit, 10*time.Minute, 40, 16384, "")
	if err != nil {
		return ""
	}
	for i := len(page.Lines) - 1; i >= 0; i-- {
		line := page.Lines[i]
		low := strings.ToLower(line)
		if strings.Contains(low, "systemd[1]") {
			continue
		}
		if strings.Contains(low, "fail") || strings.Contains(low, "error") || strings.Contains(low, "in use") || strings.Contains(low, "denied") {
			if _, msg, ok := strings.Cut(line, "]: "); ok {
				line = msg
			}
			if len(line) > 200 {
				line = line[:200]
			}
			return line
		}
	}
	return ""
}

// diagTriagePath is the workdir triage investigations run in.
var diagTriagePath = "/root/repos/diag"

const diagTriageWindow = 15 * time.Minute

// attachDiagnosis joins DIAG to a non-healthy triage without launching it:
// host_triage stays read-only, the chat runs the prepared start.
func AttachDiagnosis(ctx context.Context, out *HostTriageResult, native interface {
	Latest(context.Context, string, time.Duration) (diagprovider.NativeRun, bool, error)
}) {
	if out.Healthy {
		return
	}
	run, ok, err := native.Latest(ctx, "host", diagTriageWindow)
	switch {
	case err != nil:
		out.DiagState = "unavailable: " + err.Error()
		return
	case ok:
		brief := diagprovider.Brief(run)
		out.Diagnosis, out.DiagState = &brief, run.State
		if run.State != "done" {
			out.NextAction += "; DIAG " + run.ID + " is " + run.State + ": diag_investigation_status"
		}
		return
	}
	out.DiagState = "none"
	kinds := []string{}
	for _, p := range out.Problems {
		if p.Severity != "info" && !slices.Contains(kinds, p.Kind) {
			kinds = append(kinds, p.Kind)
		}
	}
	reason := "host_triage: " + strings.Join(kinds, ", ")
	sum := sha256.Sum256([]byte(reason))
	key := "triage-host-" + out.CheckedAt.Truncate(10*time.Minute).Format("20060102T1504") + "-" + hex.EncodeToString(sum[:4])
	out.Problems = append(out.Problems, TriageProblem{Severity: "info", Kind: "diagnosis", Subject: "host", Evidence: "no DIAG host investigation in the last 15 min",
		Action: &TriageAction{Tool: "diag_investigation_start", Approval: "none", Args: map[string]any{"idempotency_key": key, "request": diagprovider.Investigation{
			Path: diagTriagePath, Target: "host", Reason: reason, Risk: "read_only", TimeoutSeconds: 30, MaxBytes: 16384, MemoryLimitBytes: 128 << 20, CPUTimeSeconds: 5}}},
		Note: "read-only DIAG evidence before the fix; after it run DIAG again and diag_compare"})
	out.NextAction += "; DIAG: diag_investigation_start (problems kind=diagnosis)"
}

// ExecutionReadiness is health_snapshot reduced to what host_triage lacks.
type ExecutionReadiness struct {
	State string   `json:"state" jsonschema:"ok, degraded, unknown or stale (worst health_snapshot source)."`
	NotOK []string `json:"not_ok,omitempty" jsonschema:"health_snapshot sources that are not ok."`
}

// AttachExecution sets Execution and, when the host itself is healthy but
// execution is not, points next_action at health_snapshot instead of
// "nothing to fix".
func AttachExecution(out *HostTriageResult, state string, notOK []string) {
	if state == "" {
		state = "unknown"
	}
	out.Execution = &ExecutionReadiness{State: state, NotOK: notOK}
	if out.Healthy && state != "ok" {
		what := state
		if len(notOK) > 0 {
			what += ": " + strings.Join(notOK, ", ")
		}
		out.NextAction = "host is healthy, execution is " + what + "; call health_snapshot and follow each item's next_safe_action"
	}
}

// memoryProblem: swap thrash froze H96 for 3.5 h on 09.11 (MCP-159); the
// heaviest processes are the evidence, earlyoom is the safety net.
func memoryProblem(why string) TriageProblem {
	return TriageProblem{Severity: "critical", Kind: "memory_thrash", Subject: "memory", Evidence: why,
		Action: &TriageAction{Tool: "host_diagnose", Args: map[string]any{"symptom": "oom"}, Approval: "none"},
		Note:   "heavy work is held; stop the biggest non-home process (host_diagnose lists them) before starting anything heavy"}
}

// journalRoot is swapped by tests.
var journalRoot = "/"

// volatileJournal: /var/log/journal exists (persistent wanted) but journald
// writes to /run — the day's log dies with the next freeze or power loss.
// After relinquish-var the old system.journal stays on disk, untouched, so
// only the runtime file decides: journald removes it on every flush (15.11:
// armbian-ramlog left 00:57–14:57 in RAM while the disk file looked fine).
func volatileJournal() (TriageProblem, bool) {
	volatile, _ := filepath.Glob(filepath.Join(journalRoot, "run/log/journal/*/system.journal"))
	if _, err := os.Stat(filepath.Join(journalRoot, "var/log/journal")); err != nil || len(volatile) == 0 {
		return TriageProblem{}, false
	}
	return TriageProblem{Severity: "warning", Kind: "journal", Subject: "systemd-journald", Evidence: "/var/log/journal exists but journald writes to /run (volatile): logs are lost on reboot or power loss", Note: "something ran journalctl --relinquish-var (armbian-truncate-logs or armbian-ramlog; packaging/native/h96/host-hardening.sh disables both); journalctl --flush moves it back"}, true
}

// Findings converts problems for the card sentinel (issues.FileFindings).
func Findings(problems []TriageProblem) []issues.Finding {
	out := make([]issues.Finding, 0, len(problems))
	for _, p := range problems {
		f := issues.Finding{Severity: p.Severity, Kind: p.Kind, Subject: p.Subject, Evidence: p.Evidence, Note: p.Note}
		if p.Action != nil {
			f.Action = fmt.Sprintf("%s %v", p.Action.Tool, p.Action.Args)
		}
		out = append(out, f)
	}
	return out
}
