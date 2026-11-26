package phasegate

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mcpshka/internal/hosttriage"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"mcpshka/internal/gitops"
	"mcpshka/internal/jobs"
	"mcpshka/internal/memory"
	"mcpshka/internal/projectci"
	"mcpshka/internal/roadmapstyle"
)

// The phase gate turns facts into one verdict so a chat never re-derives
// gates by eye (BUG-20261021-24: absolute load compared with a per-CPU limit,
// PASS/WAIVED checkpoint gates re-examined, a passing test cancelled).
//
// A passing project test proves code, not a phase: P26…P39 of DIAG passed on
// one shared make test with "no checkpoint" (DIAG ACTIVE-02, MCP-119). A
// named phase therefore needs its own checkpoint whose PASS criteria cite
// MCPshka jobs that really ran on this history.

// ServerVersion is the running MCPshka version (set by mcpserver).
var ServerVersion string

type PhaseGateInput struct {
	Path  string `json:"path" jsonschema:"Project path (any onboarded repository or worktree)."`
	Phase string `json:"phase,omitempty" jsonschema:"Optional roadmap phase or item id, e.g. P02; selects the project checkpoint."`
}

type PhaseGateCheck = hosttriage.PhaseGateCheck

type PhaseGateResult struct {
	Verdict string `json:"verdict"` // PASS or BLOCKED
	// Acceptance separates what PASS means: CODE_TEST_PASS (no phase asked),
	// PHASE_ACCEPTED (phase checkpoint proven) or INCOMPLETE.
	Acceptance       string           `json:"acceptance"`
	Root             string           `json:"root"`
	Head             string           `json:"head,omitempty"`
	Phase            string           `json:"phase,omitempty"`
	Checks           []PhaseGateCheck `json:"checks"`
	Blockers         []string         `json:"blockers,omitempty"`
	NextAction       string           `json:"next_action"`
	CheckpointSHA256 string           `json:"checkpoint_sha256,omitempty"`
}

// CheckpointStateRoot is the MCPshka state directory holding checkpoints.
var CheckpointStateRoot = "/var/lib/mcpshka"

var phaseID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// CheckpointPaths lists where a project's phase checkpoint may live: the
// generic per-project store, then DIAG's historical location.
func CheckpointPaths(root, phase string) []string {
	name := filepath.Base(root)
	paths := []string{filepath.Join(CheckpointStateRoot, "project-checkpoints", name, phase+".json")}
	if name == "diag" {
		paths = append(paths, filepath.Join(CheckpointStateRoot, "diag-checkpoints", phase+".json"))
	}
	return paths
}

func PassingGate(value string) bool {
	v := strings.ToUpper(strings.TrimSpace(value))
	return strings.HasPrefix(v, "PASS") || strings.HasPrefix(v, "WAIVED")
}

// CheckpointGate is one acceptance criterion. Legacy checkpoints hold a bare
// status string; recorded ones carry evidence ("job:<id>", "knowledge:<id>")
// and, for soak-like criteria, the minimum wall time that evidence must span.
type CheckpointGate struct {
	Status             string        `json:"status"`
	Evidence           string        `json:"evidence,omitempty"`
	MinDurationSeconds int64         `json:"min_duration_seconds,omitempty"`
	Note               string        `json:"note,omitempty"`
	RecordedAt         time.Time     `json:"recorded_at,omitempty"`
	Supersession       *Supersession `json:"supersession,omitempty"`
	legacy             bool
}

type CheckpointDoc struct {
	Version    int                       `json:"version,omitempty"`
	Phase      string                    `json:"phase,omitempty"`
	Head       string                    `json:"head,omitempty"`
	ExpiresAt  time.Time                 `json:"expires_at,omitempty"`
	RecordedAt time.Time                 `json:"recorded_at,omitempty"`
	Gates      map[string]CheckpointGate `json:"gates"`
}

func (g CheckpointGate) MarshalJSON() ([]byte, error) {
	if g.legacy {
		return json.Marshal(g.Status)
	}
	type plain CheckpointGate
	return json.Marshal(plain(g))
}

func (g *CheckpointGate) UnmarshalJSON(raw []byte) error {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		*g = CheckpointGate{Status: s, legacy: true}
		return nil
	}
	type plain CheckpointGate
	var p plain
	if err := json.Unmarshal(raw, &p); err != nil {
		var v any
		if json.Unmarshal(raw, &v) != nil {
			return err
		}
		*g = CheckpointGate{Status: fmt.Sprint(v), legacy: true}
		return nil
	}
	*g = CheckpointGate(p)
	return nil
}

func readCheckpoint(root, phase string) (string, []byte, CheckpointDoc, error) {
	for _, path := range CheckpointPaths(root, phase) {
		f, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return path, nil, CheckpointDoc{}, err
		}
		raw, err := io.ReadAll(io.LimitReader(f, (256<<10)+1))
		f.Close()
		if err != nil {
			return path, nil, CheckpointDoc{}, err
		}
		if len(raw) > 256<<10 {
			return path, nil, CheckpointDoc{}, errors.New("checkpoint exceeds 256 KiB")
		}
		var doc CheckpointDoc
		if err := json.Unmarshal(raw, &doc); err != nil {
			return path, nil, CheckpointDoc{}, err
		}
		if doc.Gates == nil {
			doc.Gates = map[string]CheckpointGate{}
		}
		return path, raw, doc, nil
	}
	return "", nil, CheckpointDoc{}, os.ErrNotExist
}

// gateEvidenceProblem checks that a PASS criterion's evidence really exists:
// a finished, successful MCPshka job of this project that ran long enough.
func gateEvidenceProblem(manager *jobs.Manager, root string, g CheckpointGate) string {
	kind, id, _ := strings.Cut(g.Evidence, ":")
	switch {
	case g.legacy || strings.HasPrefix(strings.ToUpper(g.Status), "WAIVED"):
		return ""
	case kind == "job":
		if manager == nil {
			return "job evidence cannot be read"
		}
		job, err := manager.Get(id)
		if err != nil {
			return "evidence job " + id + " not found"
		}
		if job.State != jobs.Done || job.ExitCode == nil || *job.ExitCode != 0 {
			return "evidence job " + id + " is " + string(job.State) + ", not a success"
		}
		if !sameProject(job.Workdir, root) {
			return "evidence job " + id + " ran in another project"
		}
		if ran := int64(job.FinishedAt.Sub(job.StartedAt).Seconds()); g.MinDurationSeconds > 0 && ran < g.MinDurationSeconds {
			return fmt.Sprintf("evidence job %s ran %ds, criterion needs ≥%ds", id, ran, g.MinDurationSeconds)
		}
		return ""
	case kind == "knowledge" && id != "":
		if g.MinDurationSeconds > 0 {
			return "duration criterion needs job evidence, not a note"
		}
		return ""
	default:
		return "PASS without evidence (job:<id> or knowledge:<id>)"
	}
}

func sameProject(workdir, root string) bool {
	w, r := filepath.Clean(workdir), filepath.Clean(root)
	return w == r || strings.HasPrefix(w, r+string(filepath.Separator)) || filepath.Base(w) == filepath.Base(r)
}

func Evaluate(git *gitops.Service, manager *jobs.Manager, in PhaseGateInput) (PhaseGateResult, error) {
	if in.Phase != "" && !phaseID.MatchString(in.Phase) {
		return PhaseGateResult{}, errors.New("phase must be a short roadmap id like P02")
	}
	st, err := git.Status(in.Path, 64)
	if err != nil {
		return PhaseGateResult{}, err
	}
	out := PhaseGateResult{Root: st.Root, Phase: in.Phase}
	add := func(c PhaseGateCheck) {
		out.Checks = append(out.Checks, c)
		if c.Status == "FAIL" || c.Status == "MISSING" || c.Status == "RUNNING" {
			out.Blockers = append(out.Blockers, c.Name+": "+c.Detail)
		}
	}
	head, err := git.Revision(st.Root)
	if err != nil {
		return PhaseGateResult{}, err
	}
	out.Head = head
	if st.Dirty {
		add(PhaseGateCheck{Name: "git_clean", Status: "FAIL", Detail: fmt.Sprintf("%d uncommitted entries on %s; commit or use a sibling worktree", len(st.Entries), st.Branch)})
	} else {
		add(PhaseGateCheck{Name: "git_clean", Status: "PASS", Detail: "clean " + st.Branch + " at " + head[:12]})
	}
	activity := manager.ProjectActivity(st.Root, 16)
	switch {
	case activity.UnknownCount > 0:
		add(PhaseGateCheck{Name: "project_jobs", Status: "FAIL", Detail: fmt.Sprintf("%d job(s) in UNKNOWN; reconcile with job_status", activity.UnknownCount)})
	case activity.Count > 0:
		add(PhaseGateCheck{Name: "project_jobs", Status: "RUNNING", Detail: fmt.Sprintf("%d job(s) active; wait with job_wait", activity.Count)})
	default:
		add(PhaseGateCheck{Name: "project_jobs", Status: "PASS", Detail: "no active jobs"})
	}
	add(hosttriage.Pressure())

	testsPassed := false
	var local *PhaseGateCheck
	if job, ok := manager.SealedTestEvidence(st.Root, head); ok {
		switch {
		case job.State == jobs.Done && job.ExitCode != nil && *job.ExitCode == 0:
			testsPassed = true
			local = &PhaseGateCheck{Name: "sealed_test", Status: "PASS", Detail: "job " + job.ID + " passed on " + head[:12]}
		case job.State == jobs.Queued || job.State == jobs.Running || job.State == jobs.Unknown || job.State == jobs.Cancelling:
			local = &PhaseGateCheck{Name: "sealed_test", Status: "RUNNING", Detail: "job " + job.ID + " " + string(job.State) + "; job_wait, never cancel a progressing test"}
		case job.State == jobs.Cancelled || job.State == jobs.TimedOut || (job.External != nil && job.External.Cancel):
			local = &PhaseGateCheck{Name: "sealed_test", Status: "FAIL", Detail: "job " + job.ID + " was cancelled or timed out — not a code failure; rerun project_test, runner_task go-test or GitVerse CI (forge_ci path=)"}
		default:
			local = &PhaseGateCheck{Name: "sealed_test", Status: "FAIL", Detail: "job " + job.ID + " " + string(job.State) + "; read job_output, fix, commit, rerun"}
		}
	}
	if !testsPassed && (local == nil || local.Status != "RUNNING") {
		if state, detail := projectci.OffBoxTest(st.Root, head, ServerVersion); state == "PASS" || local == nil || state == "RUNNING" {
			testsPassed, local = state == "PASS", &PhaseGateCheck{Name: "sealed_test", Status: state, Detail: detail}
		}
	}
	if local.Status == "MISSING" {
		// 26.11 go2rtc: a PASS of project_test with eight packages was taken
		// for the full test; only the profile run seals the exact HEAD.
		local.Detail += "; a targeted project_test (packages/run) never seals: run project_test path=" + st.Root + " without packages on the committed HEAD " + head[:12]
	}
	add(*local)
	if pass, detail, ok := memory.Gate(manager.StateDir(), st.Root); ok { // 0.11.3 item 1
		add(PhaseGateCheck{Name: "memory_exam", Status: map[bool]string{true: "PASS", false: "FAIL"}[pass], Detail: detail})
	}
	if style := roadmapstyle.CheckFile(st.Root, 10); len(style) > 0 {
		// WARN never blocks: the shared template is adopted project by project.
		add(PhaseGateCheck{Name: "roadmap_style", Status: "WARN", Detail: strings.Join(style, "; ")})
	}

	if in.Phase != "" {
		add(phaseCheckpointCheck(git, manager, st, head, in.Phase, testsPassed, &out.CheckpointSHA256))
	}
	out.Verdict, out.NextAction = "PASS", "proceed: the phase gates hold for "+head[:12]
	out.Acceptance = "CODE_TEST_PASS"
	if in.Phase != "" {
		out.Acceptance = "PHASE_ACCEPTED"
	}
	if len(out.Blockers) > 0 {
		out.Verdict, out.Acceptance = "BLOCKED", "INCOMPLETE"
		out.NextAction = out.Blockers[0]
	}
	return out, nil
}

func phaseCheckpointCheck(git *gitops.Service, manager *jobs.Manager, st gitops.StatusResult, head, phase string, testsPassed bool, sha *string) PhaseGateCheck {
	path, raw, doc, err := readCheckpoint(st.Root, phase)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return PhaseGateCheck{Name: "checkpoint", Status: "MISSING", Detail: "no checkpoint for " + phase + ": a passing project test proves code, not this phase; record each acceptance criterion with phase_checkpoint_record (PASS cites job:<id> evidence)"}
	case err != nil:
		return PhaseGateCheck{Name: "checkpoint", Status: "FAIL", Detail: path + ": " + err.Error()}
	}
	*sha = fmt.Sprintf("%x", sha256.Sum256(raw))
	if !doc.ExpiresAt.IsZero() && time.Now().After(doc.ExpiresAt) {
		return PhaseGateCheck{Name: "checkpoint", Status: "FAIL", Detail: "checkpoint for " + phase + " expired " + doc.ExpiresAt.UTC().Format(time.RFC3339) + "; re-verify and record again"}
	}
	if doc.Head != "" && doc.Head != head {
		if ok, err := git.IsAncestor(st.Root, doc.Head); err != nil || !ok {
			return PhaseGateCheck{Name: "checkpoint", Status: "FAIL", Detail: "checkpoint recorded at " + shortRev(doc.Head) + " is not in the history of HEAD " + head[:12] + "; re-verify on this branch"}
		}
	}
	// Facts proven here supersede stale checkpoint text; marking the
	// roadmap done is the closing action itself, never its precondition.
	delete(doc.Gates, "roadmap_done")
	if testsPassed {
		for _, k := range []string{"go_test", "tests", "full_test"} {
			delete(doc.Gates, k)
		}
	}
	if !st.Dirty && (st.Branch == "main" || st.Branch == "master") {
		delete(doc.Gates, "git_integrated_main")
	}
	if len(doc.Gates) == 0 {
		return PhaseGateCheck{Name: "checkpoint", Status: "MISSING", Detail: "checkpoint for " + phase + " lists no phase criteria beyond the code test"}
	}
	names := slices.Sorted(maps.Keys(doc.Gates))
	var open []string
	legacy, superseded := 0, 0
	for _, k := range names {
		g := doc.Gates[k]
		if g.Status == "SUPERSEDED" && manager != nil && verifiedSupersession(manager.Database(), st.Root, phase, k, g.Supersession) {
			superseded++
			continue
		}
		if g.legacy {
			legacy++
		}
		if !PassingGate(g.Status) {
			open = append(open, k+"="+g.Status)
		} else if problem := gateEvidenceProblem(manager, st.Root, g); problem != "" {
			open = append(open, k+": "+problem)
		}
	}
	if len(open) > 0 {
		return PhaseGateCheck{Name: "checkpoint", Status: "FAIL", Detail: "open gates: " + strings.Join(open, ", ")}
	}
	detail := fmt.Sprintf("%d gates PASS/WAIVED, %d SUPERSEDED in %s", len(names)-superseded, superseded, path)
	if legacy > 0 {
		detail += fmt.Sprintf(" (%d legacy without evidence)", legacy)
	}
	return PhaseGateCheck{Name: "checkpoint", Status: "PASS", Detail: detail}
}

func shortRev(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}
