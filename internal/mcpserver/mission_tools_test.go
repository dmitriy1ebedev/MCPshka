package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/agents"
	"mcpshka/internal/authn"
	"mcpshka/internal/healthlayer"
	"mcpshka/internal/mission"
	"mcpshka/internal/roadmaprun"
	missiontools "mcpshka/internal/tools/mission"
)

func missionWrite(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestMissionSourcesReadOnlyAndNoSecondWriter(t *testing.T) {
	m, leases, in := yieldFixture(t)
	tasks, runs := t.TempDir(), t.TempDir()
	now := time.Now().UTC()
	id := "0123456789abcdef"
	missionWrite(t, filepath.Join(tasks, id+".json"), agents.Task{ID: id, Path: in.Path, State: "running", StartedAt: now.Add(-time.Hour), Tail: "DO NOT EXPOSE PROMPT", Error: ""})
	rid := "project-20261023-150000-abcd"
	missionWrite(t, filepath.Join(runs, rid+".json"), roadmaprun.Journal{ID: rid, Path: in.Path, State: "running", StartedAt: now.Add(-time.Minute), UpdatedAt: now, Phases: []roadmaprun.Phase{{ID: "P8"}}})
	s := missiontools.New(missiontools.Deps{Manager: m, Tasks: tasks, Runs: runs, Projects: filepath.Dir(in.Path), Unit: func(context.Context, string) string { return "active" }})
	before, err := leases.Status(in.Path)
	if err != nil {
		t.Fatal(err)
	}
	report, err := s.Read(context.Background())
	if err != nil || report.State != "degraded" {
		t.Fatalf("%+v %v", report, err)
	}
	seen := map[string]bool{}
	for _, i := range report.Items {
		seen[i.Kind] = true
		if i.Kind == "agent_task" && (i.EvidenceAge == nil || *i.EvidenceAge < 3599 || i.NextSafeAction.Tool != "agent_task_status") {
			t.Fatal(i)
		}
		if i.Kind == "roadmap_run" && i.CurrentPhase != "P8:running" {
			t.Fatal(i)
		}
	}
	if !seen["lease"] || !seen["agent_task"] || !seen["roadmap_run"] {
		t.Fatal(seen)
	}
	raw, _ := json.Marshal(report)
	if strings.Contains(string(raw), in.Token) || strings.Contains(string(raw), "DO NOT EXPOSE") {
		t.Fatal("secret or prompt leaked")
	}
	after, err := leases.Status(in.Path)
	if err != nil || before.LeaseID != after.LeaseID || before.Generation != after.Generation || !after.Held {
		t.Fatal("status changed lease")
	}
	c := healthlayer.New([]healthlayer.Probe{missionHealthProbe(s)})
	health, err := c.Read(context.Background())
	if err != nil || health.State != "degraded" || health.Items[0].NextSafeAction.Tool != "mission_status" {
		t.Fatalf("%+v %v", health, err)
	}
}

func TestMissionNativeContractAndDiscovery(t *testing.T) {
	m, _, in := yieldFixture(t)
	s := missiontools.New(missiontools.Deps{Manager: m, Tasks: t.TempDir(), Runs: t.TempDir(), Projects: filepath.Dir(in.Path), Unit: func(context.Context, string) string { return "active" }})
	srv := mcp.NewServer(&mcp.Implementation{Name: "mission-test", Version: "1"}, nil)
	r := &toolRegistry{server: srv, tools: map[string]bool{}, readers: map[string]probeHandler{}}
	registerMissionTools(r, s)
	ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: "reader", Role: "reader", Scopes: []string{"mission_status"}})
	v, err := r.readers["mission_status"](ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if v.(mission.Report).Trust != "UNTRUSTED" || !r.catalog["mission_status"].Annotations.ReadOnlyHint {
		t.Fatal(v)
	}
	if _, ok := mutationContracts["mission_status"]; ok {
		t.Fatal("read tool has mutation contract")
	}
	if !strings.Contains(toolAliasesRU["mission_status"], "миссии") {
		t.Fatal("missing RU alias")
	}
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	out, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "mission_status", Arguments: map[string]any{}})
	if err != nil || out.IsError {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestMissionIntegrationLeaseAndMissingEvidence(t *testing.T) {
	m, _, in := yieldFixture(t)
	s := missiontools.New(missiontools.Deps{Manager: m, Tasks: t.TempDir(), Runs: t.TempDir(), Projects: t.TempDir(), Unit: func(context.Context, string) string { return "active" }})
	key := "integration/v1/queues/" + strings.Repeat("a", 64)
	item := map[string]any{"request": map[string]any{"owner": "test", "path": in.Path, "id": "candidate", "branch": "feature", "commit": strings.Repeat("a", 40), "target_branch": "main", "target_head": strings.Repeat("b", 40), "roadmap_sha256": strings.Repeat("c", 64), "scope": "P8"}, "execution_id": strings.Repeat("d", 32), "state": "pending"}
	raw, _ := json.Marshal(map[string]any{"version": 1, "items": []any{item}})
	if _, err := m.Database().PutIfAbsent(key, raw); err != nil {
		t.Fatal(err)
	}
	report, err := s.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range report.Items {
		if i.Kind == "integration" {
			if i.EvidenceAge != nil || len(i.BlockedBy) != 1 || i.BlockedBy[0] != "lease" || i.NextSafeAction.Tool != "integration_status" {
				t.Fatal(i)
			}
			return
		}
	}
	t.Fatal("integration disappeared")
}

func TestMissionCompletedRunSupersedesOldFailure(t *testing.T) {
	m, _, in := yieldFixture(t)
	tasks := t.TempDir()
	now := time.Now().UTC()
	oldID, newID := "0123456789abcdef", "1123456789abcdef"
	missionWrite(t, filepath.Join(tasks, oldID+".json"), agents.Task{ID: oldID, Path: in.Path, State: "quota", StartedAt: now.Add(-time.Hour), FinishedAt: now.Add(-time.Minute)})
	missionWrite(t, filepath.Join(tasks, newID+".json"), agents.Task{ID: newID, Path: in.Path, State: "done", StartedAt: now, FinishedAt: now})
	s := missiontools.New(missiontools.Deps{Manager: m, Tasks: tasks, Runs: t.TempDir(), Projects: t.TempDir(), Unit: func(context.Context, string) string { return "active" }})
	report, err := s.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range report.Items {
		if i.Kind == "agent_task" {
			t.Fatal("historical failure still blocks", i)
		}
	}
}

func TestMissionFreshRoadmapLeaseAndIndependentWork(t *testing.T) {
	m, leases, in := yieldFixture(t)
	if _, err := leases.Release(in.Path, in.Token); err != nil {
		t.Fatal(err)
	}
	rid := "project-20261023-150000-abcd"
	held, err := leases.AcquireTracked(in.Path, "roadmap-run:"+rid, "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	_ = held
	runs, projects := t.TempDir(), t.TempDir()
	// A marker is enough for advisory discovery; no Git command or clone occurs.
	free := filepath.Join(projects, "free-worktree")
	if err := os.MkdirAll(filepath.Join(free, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	m.Policy().AllowedWorkdirs = append(m.Policy().AllowedWorkdirs, projects)
	now := time.Now().UTC()
	missionWrite(t, filepath.Join(runs, rid+".json"), roadmaprun.Journal{ID: rid, Path: in.Path, State: "running", StartedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour)})
	s := missiontools.New(missiontools.Deps{Manager: m, Tasks: t.TempDir(), Runs: runs, Projects: projects, Unit: func(context.Context, string) string { return "active" }})
	report, err := s.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.State != "ok" || report.IndependentWorkAvailable == nil || !*report.IndependentWorkAvailable || len(report.FreeProjects) != 1 || report.FreeProjects[0] != free {
		t.Fatal(report)
	}
	for _, i := range report.Items {
		if i.Kind == "roadmap_run" && (i.EvidenceAge == nil || *i.EvidenceAge > 2) {
			t.Fatal("matching lease not used", i)
		}
	}
}

// MCP-167: a lease left on a removed worktree must not keep mission_status
// unknown; it blocks nothing and is dropped by stale release.
func TestMissionSkipsLeaseOfRemovedWorktree(t *testing.T) {
	m, leases, in := yieldFixture(t)
	if _, err := leases.Release(in.Path, in.Token); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(filepath.Dir(in.Path), "removed-worktree")
	raw, _ := json.Marshal(map[string]any{"project_root": gone, "held": true, "owner": "dead-chat"})
	if _, err := m.Database().PutIfAbsent("leases/"+strings.Repeat("e", 64)+".json", raw); err != nil {
		t.Fatal(err)
	}
	s := missiontools.New(missiontools.Deps{Manager: m, Tasks: t.TempDir(), Runs: t.TempDir(), Projects: t.TempDir(), Unit: func(context.Context, string) string { return "active" }})
	report, err := s.Read(context.Background())
	if err != nil || report.State == "unknown" {
		t.Fatalf("dead lease keeps mission unknown: %+v %v", report, err)
	}
}

func TestMissionManyDeadLeasesBeyondPageStayKnown(t *testing.T) {
	m, leases, in := yieldFixture(t)
	if _, err := leases.Release(in.Path, in.Token); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		gone := filepath.Join(filepath.Dir(in.Path), fmt.Sprintf("removed-%d", i))
		raw, _ := json.Marshal(map[string]any{"project_root": gone, "held": true, "owner": "old-chat"})
		key := fmt.Sprintf("leases/%064x.json", i+1)
		if _, err := m.Database().PutIfAbsent(key, raw); err != nil {
			t.Fatal(err)
		}
	}
	s := missiontools.New(missiontools.Deps{Manager: m, Tasks: t.TempDir(), Runs: t.TempDir(), Projects: t.TempDir(), Unit: func(context.Context, string) string { return "active" }})
	report, err := s.Read(context.Background())
	if err != nil || report.State == "unknown" || report.Truncated || len(report.Items) != 0 {
		t.Fatalf("dead leases past one page make mission unknown: %+v %v", report, err)
	}
}

func TestMissionProjectRoot(t *testing.T) {
	for in, want := range map[string]string{"/root/repos/lane": "/root/repos/lane", "/root/repos/diag/cmd/x": "/root/repos/diag", "/root/repos": "", "/var/lib/x": "", "/root/repos-other/a": ""} {
		got, ok := mission.ProjectRoot("/root/repos", in)
		if got != want || ok != (want != "") {
			t.Fatalf("%s: %q %v", in, got, ok)
		}
	}
}
