package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/evidence"
	"mcpshka/internal/gitops"
	"mcpshka/internal/mission"
)

func controlledMissionFixture(t *testing.T, e *Engine, root, id string) *mission.Controller {
	t.Helper()
	git := gitops.New(e.manager.Policy())
	object := func(v any) map[string]any {
		raw, _ := json.Marshal(v)
		out := map[string]any{}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	c := mission.NewController(e.manager.Database(), git, func(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
		switch name {
		case "capabilities_bootstrap":
			return map[string]any{"revision": strings.Repeat("a", 64)}, nil
		case "tool_resolve":
			return map[string]any{"code": "OK", "revision": strings.Repeat("a", 64), "candidates": []any{map[string]any{"name": args["goal"], "permission_granted": true, "input_schema": map[string]any{"type": "object"}}}}, nil
		case "capability_preflight":
			return map[string]any{"verdict": "ready"}, nil
		case "workflow_plan":
			pin, err := e.Preview(args["path"].(string), args["pipeline"].(string), args["id"].(string), args["roadmap_sha256"].(string), args["scope"].(string))
			return object(pin), err
		case "workflow_status":
			r, err := e.Get(id)
			return object(r), err
		case "acceptance_status":
			head, _ := git.Revision(root)
			tree, _ := git.Tree(root)
			return object(mission.Acceptance{Inputs: evidence.Inputs{Head: head, Tree: tree}, Gates: []evidence.GateState{{Gate: "engineering", State: "MISSING"}}}), nil
		default:
			t.Fatal("unexpected mission effect", name)
			return nil, nil
		}
	})
	c.WorkflowControl = func(workflow, task, mode string, deadline *time.Time) error {
		if err := e.BindMission(workflow, task, deadline); err != nil {
			return err
		}
		if mode == "bind" {
			return nil
		}
		return e.ControlMission(workflow, task, mode)
	}
	return c
}

func TestMissionControlLostPauseReplyRecoversFromChildAuthority(t *testing.T) {
	e, m, root, sha := canary(t, "")
	const id = "bound-pause-reply"
	if _, err := e.Start(root, "release", id, sha, "P7"); err != nil {
		t.Fatal(err)
	}
	c := controlledMissionFixture(t, e, root, id)
	s, err := c.Start(context.Background(), mission.StartInput{Plan: mission.Plan{Path: root, Goal: "finish original pipeline", Mode: "CLOSE_STEP", Step: "P7", WorkflowID: id}, IdempotencyKey: "bound-pause"})
	if err != nil {
		t.Fatal(err)
	}
	original := c.WorkflowControl
	c.WorkflowControl = func(workflow, task, mode string, deadline *time.Time) error {
		if err := original(workflow, task, mode, deadline); err != nil {
			return err
		}
		return errors.New("simulated response loss after durable pause")
	}
	if _, err := c.Pause(mission.ControlInput{ID: s.ID}); err == nil {
		t.Fatal("fixture did not lose reply")
	}
	cold := New(m)
	recovered := controlledMissionFixture(t, cold, root, id)
	if err := recovered.Maintain(); err != nil {
		t.Fatal(err)
	}
	status, err := recovered.Status(context.Background(), s.ID)
	if err != nil || status.State != "PAUSED" {
		t.Fatal("lost pause was forgotten", status.State, err)
	}
	if tick, err := recovered.Tick(context.Background(), mission.ControlInput{ID: s.ID}); err != nil || tick.State != "PAUSED" {
		t.Fatal("mission_tick reopened the owner pause", tick.State, err)
	}
	if run, err := cold.Tick(id); err != nil || run.LaunchClaimed {
		t.Fatal("cold controller dispatched paused work", run, err)
	}
	if _, err := recovered.Resume(context.Background(), mission.ControlInput{ID: s.ID}); err != nil {
		t.Fatal(err)
	}
	run, _ := cold.Get(id)
	if run.Control != "" {
		t.Fatal("authorized mission resume did not clear pause", run.Control)
	}
	if _, err := recovered.Cancel(mission.ControlInput{ID: s.ID}); err != nil {
		t.Fatal(err)
	}
	cold.OnTerminal = recovered.WorkflowCompleted
	run, err = cold.Tick(id)
	if err != nil || run.State != "cancelled" {
		t.Fatal("unstarted mission cancellation failed", run, err)
	}
	if claim, err := cold.ProjectClaim(root); err != nil || claim != nil {
		t.Fatal("cancelled mission stranded its claim", claim, err)
	}
	if _, err := os.Stat(filepath.Join(root, "events")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled pipeline caused an effect", err)
	}
	status, err = recovered.Status(context.Background(), s.ID)
	if err != nil || status.State != "CANCELLED" || status.LastProven != nil {
		t.Fatal("cancellation fabricated acceptance", status.State, err)
	}
}
