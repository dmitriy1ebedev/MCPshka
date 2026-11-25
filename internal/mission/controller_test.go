package mission

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mcpshka/internal/evidence"
	"mcpshka/internal/gitops"
	"mcpshka/internal/policy"
	"mcpshka/internal/projectprofile"
	"mcpshka/internal/statestore"
)

type missionFixture struct {
	service    *Controller
	root       string
	accepted   map[string]bool
	calls      []string
	head, tree string
}

func controllerFixture(t *testing.T) *missionFixture {
	t.Helper()
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		raw, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git: %s %v", raw, err)
		}
		return string(raw)
	}
	git("init", "-q")
	git("config", "user.name", "test")
	git("config", "user.email", "test@example.invalid")
	os.WriteFile(filepath.Join(root, "README"), []byte("fixture"), 0600)
	git("add", "README")
	git("commit", "-qm", "fixture")
	p := policy.Default()
	p.AllowedWorkdirs = []string{root}
	g := gitops.New(p)
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	f := &missionFixture{root: root, accepted: map[string]bool{}}
	f.head, _ = g.Revision(root)
	f.tree, _ = g.Tree(root)
	f.service = NewController(db, g, func(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
		f.calls = append(f.calls, name)
		var out any
		switch name {
		case "capabilities_bootstrap":
			out = map[string]any{"revision": strings.Repeat("a", 64)}
		case "tool_resolve":
			out = map[string]any{"code": "OK", "revision": strings.Repeat("a", 64), "candidates": []any{map[string]any{"name": args["goal"], "permission_granted": true, "input_schema": map[string]any{"type": "object"}}}}
		case "project_context":
			out = map[string]any{}
		case "project_lease_status":
			out = map[string]any{"held": true, "owner": "chatgpt-diag"}
		case "capability_preflight":
			out = map[string]any{"verdict": "ready"}
		case "acceptance_status":
			pass := f.accepted[args["step"].(string)]
			result := "MISSING"
			if pass {
				result = "PASS"
			}
			out = Acceptance{Done: pass, Inputs: evidence.Inputs{Head: f.head, Tree: f.tree}, Gates: []evidence.GateState{{Gate: "build", State: result, Record: &evidence.Record{Result: result, Inputs: evidence.Inputs{Head: f.head, Tree: f.tree}}}}}
		default:
			t.Fatalf("unexpected effect: %s", name)
		}
		raw, _ := json.Marshal(out)
		var m map[string]any
		json.Unmarshal(raw, &m)
		return m, nil
	})
	return f
}
func (f *missionFixture) start(t *testing.T) Snapshot {
	t.Helper()
	s, err := f.service.Start(context.Background(), StartInput{Plan: Plan{Path: f.root, Goal: "close tier", Mode: "TIER", Tier: "1", Steps: []Step{{ID: "P1"}, {ID: "P2"}}}, IdempotencyKey: "start"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestControllerDurableResumeUsesProof(t *testing.T) {
	f := controllerFixture(t)
	s := f.start(t)
	if s.State != "READY" || s.Current != 0 || s.LastProven != nil {
		t.Fatal(s)
	}
	f.accepted["P1"] = true
	s, err := f.service.Resume(context.Background(), ControlInput{ID: s.ID})
	if err != nil {
		t.Fatal(err)
	}
	if s.Current != 1 || s.LastProven == nil || s.LastProven.Step != "P1" {
		t.Fatal(s)
	}
	// A new service instance reads SQLite, never the last chat/tool call.
	f.service = NewController(f.service.db, f.service.git, f.service.call)
	f.accepted["P1"] = false
	s, err = f.service.Resume(context.Background(), ControlInput{ID: s.ID})
	if err != nil {
		t.Fatal(err)
	}
	if s.Current != 0 || s.LastProven != nil || s.State == "DONE" {
		t.Fatal("unproven checkpoint reused", s)
	}
	f.accepted["P1"] = true
	f.accepted["P2"] = true
	for i := 0; i < 2; i++ {
		s, err = f.service.Resume(context.Background(), ControlInput{ID: s.ID})
		if err != nil {
			t.Fatal(err)
		}
	}
	if s.State != "DONE" || s.Current != 2 {
		t.Fatal(s)
	}
}
func TestControllerPauseCancelAndDirtyTree(t *testing.T) {
	f := controllerFixture(t)
	s := f.start(t)
	s, err := f.service.Pause(ControlInput{ID: s.ID, Reason: "owner busy"})
	if err != nil || s.State != "PAUSED" {
		t.Fatal(s, err)
	}
	stored, err := f.service.Status(context.Background(), s.ID)
	if err != nil || stored.State != "PAUSED" {
		t.Fatal(stored, err)
	}
	os.WriteFile(filepath.Join(f.root, "dirty"), []byte("x"), 0600)
	f.accepted["P1"] = true
	s, err = f.service.Resume(context.Background(), ControlInput{ID: s.ID})
	if err != nil || s.State != "BLOCKED" || s.LastProven != nil {
		t.Fatal(s, err)
	}
	s, err = f.service.Cancel(ControlInput{ID: s.ID})
	if err != nil || s.State != "CANCELLED" {
		t.Fatal(s, err)
	}
	if _, err = f.service.Resume(context.Background(), ControlInput{ID: s.ID}); err == nil {
		t.Fatal("cancelled mission resumed")
	}
	if _, err = f.service.Pause(ControlInput{ID: s.ID}); err == nil {
		t.Fatal("cancelled mission paused")
	}
}
func TestControllerPreflightModesAndConsent(t *testing.T) {
	f := controllerFixture(t)
	for _, mode := range Modes {
		p := Plan{Path: f.root, Goal: "test", Mode: mode, Step: "P1", Tier: "1"}
		out, err := f.service.Preflight(context.Background(), p)
		if err != nil || len(out.Steps) != 1 {
			t.Fatal(mode, out, err)
		}
	}
	p := Plan{Path: f.root, Goal: "test", Mode: "CLOSE_STEP", Step: "P1", AgentsAllowed: false}
	s, err := f.service.Start(context.Background(), StartInput{Plan: p, IdempotencyKey: "no-agents"})
	if err != nil {
		t.Fatal(err)
	}
	if s.AgentsAllowed || s.NextSafeAction.Tool == "agent_task" || s.NextSafeAction.Tool == "roadmap_run" {
		t.Fatal(s)
	}
	for _, name := range f.calls {
		if name == "agent_task" || name == "roadmap_run" {
			t.Fatal("agent launched without consent")
		}
	}
	p.Mode = "invented"
	if _, err = f.service.Preflight(context.Background(), p); err == nil {
		t.Fatal("invalid mode accepted")
	}
}

func TestControllerRejectsChangedIdentityAndInvalidPlan(t *testing.T) {
	f := controllerFixture(t)
	s := f.start(t)
	f.accepted["P1"] = true
	s, err := f.service.Resume(context.Background(), ControlInput{ID: s.ID})
	if err != nil {
		t.Fatal(err)
	}
	f.head = "different"
	s, err = f.service.Resume(context.Background(), ControlInput{ID: s.ID})
	if err != nil {
		t.Fatal(err)
	}
	if s.Current != 0 || s.LastProven != nil {
		t.Fatal("changed HEAD reused", s)
	}
	for _, p := range []Plan{
		{Path: f.root, Mode: "CLOSE_STEP", Step: "P1"},
		{Path: f.root, Goal: "x", Mode: "TIER", Steps: []Step{{ID: "P1"}}},
		{Path: f.root, Goal: "x", Mode: "ROADMAP", Steps: []Step{{ID: "P1"}, {ID: "P1"}}},
	} {
		if _, err := f.service.Preflight(context.Background(), p); err == nil {
			t.Fatal("invalid plan accepted", p)
		}
	}
}

func TestControllerConsentAndAuditActions(t *testing.T) {
	f := controllerFixture(t)
	for _, audit := range []bool{false, true} {
		mode := "CLOSE_STEP"
		if audit {
			mode = "AUDIT_ONLY"
		}
		s, err := f.service.Start(context.Background(), StartInput{Plan: Plan{Path: f.root, Goal: "x", Mode: mode, Step: "P1", AgentsAllowed: true}, IdempotencyKey: mode})
		if err != nil {
			t.Fatal(err)
		}
		if audit && (s.NextSafeAction.Tool == "agent_task" || s.NextSafeAction.Tool == "roadmap_run") {
			t.Fatal("audit delegated mutation", s)
		}
		if !audit && s.NextSafeAction.Tool != "agent_task" {
			t.Fatal("approved delegation absent", s)
		}
	}
}

func TestControllerPreflightBlockerAndMissingProof(t *testing.T) {
	f := controllerFixture(t)
	original := f.service.call
	f.service.call = func(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
		if name == "capability_preflight" {
			return map[string]any{"verdict": "blocked", "blocker": "Face ID required", "next_safe_action": map[string]any{"tool": "admin_session_status", "args": map[string]any{}, "approval": "face_id"}}, nil
		}
		return original(ctx, name, args)
	}
	p := Plan{Path: f.root, Goal: "x", Mode: "CLOSE_STEP", Step: "P1"}
	out, err := f.service.Preflight(context.Background(), p)
	if err != nil || out.State != "BLOCKED" || len(out.Blockers) == 0 || out.Blockers[0].NextSafeAction.Tool == "" {
		t.Fatal(out, err)
	}
	f.service.call = func(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
		if name == "acceptance_status" {
			return map[string]any{"done": true, "inputs": map[string]any{"head": f.head, "tree": f.tree}, "gates": []any{}}, nil
		}
		return original(ctx, name, args)
	}
	s, err := f.service.Start(context.Background(), StartInput{Plan: p, IdempotencyKey: "missing-proof"})
	if err != nil {
		t.Fatal(err)
	}
	s, err = f.service.Resume(context.Background(), ControlInput{ID: s.ID})
	if err != nil || s.LastProven != nil || s.State == "DONE" {
		t.Fatal("empty gate set proved checkpoint", s, err)
	}
}

func TestControllerReportAndReadOnlyStatus(t *testing.T) {
	f := controllerFixture(t)
	s := f.start(t)
	before, err := f.service.db.Get("missions/" + s.ID + ".json")
	if err != nil {
		t.Fatal(err)
	}
	f.accepted["P1"] = true
	report, err := f.service.Report(context.Background(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Mission != s.ID || report.CurrentStep != "P1" || report.GitHEAD != f.head || report.Production != "UNKNOWN" {
		t.Fatal(report)
	}
	// Both chats must see who drives the project and what landed (08.11).
	if report.Holder != "chatgpt-diag" || len(report.RecentCommits) != 1 || !strings.Contains(report.RecentCommits[0], "fixture") || !strings.Contains(report.Text, "Project held by · chatgpt-diag") {
		t.Fatalf("holder/commits: %q %v", report.Holder, report.RecentCommits)
	}
	after, _ := f.service.db.Get("missions/" + s.ID + ".json")
	if string(after) != string(before) {
		t.Fatal("reading report wrote checkpoint")
	}
	s, err = f.service.Status(context.Background(), s.ID)
	if err != nil || s.LastProven != nil {
		t.Fatal(s, err)
	}
}

func TestControllerIndexSurvivesLostChatID(t *testing.T) {
	f := controllerFixture(t)
	s := f.start(t)
	refs, truncated, err := f.service.Index()
	if err != nil || truncated || len(refs) != 1 || refs[0].ID != s.ID || refs[0].RecordedState != s.State {
		t.Fatal(refs, truncated, err)
	}
}

func TestMissionIntakeRejectsConsentAndScopeWidening(t *testing.T) {
	f := controllerFixture(t)
	for _, change := range []func(*Plan){
		func(p *Plan) { p.Mode = "AUDIT_ONLY" },
		func(p *Plan) { p.AgentsAllowed = true },
		func(p *Plan) { p.WorkflowPlan.Scope = "P2" },
		func(p *Plan) { p.WorkflowPlan.Path = "/" },
		func(p *Plan) { p.WorkflowID = "different" },
	} {
		p := Plan{Path: f.root, Goal: "exact native scope", Mode: "CLOSE_STEP", Step: "P1", WorkflowPlan: &projectprofile.PipelinePin{ID: "intake", Path: f.root, Scope: "P1"}}
		change(&p)
		if _, err := f.service.normalize(p); err == nil {
			t.Fatal("widened mission intake accepted", p)
		}
	}
}
