package workflow

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/evidence"
	"mcpshka/internal/gitops"
	"mcpshka/internal/mission"
	"mcpshka/internal/projectprofile"
)

func TestBoundMissionWaitsForAcceptanceAndResumesFromColdOutbox(t *testing.T) {
	missionAcceptanceFixture(t, "")
}

func TestMissionIntakeCrossProjectAcceptanceFromColdOutbox(t *testing.T) {
	for _, kind := range []string{"service-config", "game-artifact", "sqlite-ledger"} {
		t.Run(kind, func(t *testing.T) { missionAcceptanceFixture(t, kind) })
	}
}

func missionAcceptanceFixture(t *testing.T, kind string) {
	t.Helper()
	e, m, root, sha := canary(t, "")
	if kind != "" {
		profileFile := filepath.Join(root, ".mcpshka/profile.json")
		raw, err := os.ReadFile(profileFile)
		if err != nil {
			t.Fatal(err)
		}
		var profile projectprofile.Profile
		if err := json.Unmarshal(raw, &profile); err != nil {
			t.Fatal(err)
		}
		profile.Name = kind
		var modify, verify string
		switch kind {
		case "service-config":
			modify = "; import json; Path('service.json').write_text(json.dumps({'enabled': True, 'port': 8765}))"
			verify = "; import json; assert json.loads(Path('service.json').read_text()) == {'enabled': True, 'port': 8765}"
		case "game-artifact":
			modify = "; Path('game.pak').write_bytes(b'PACK-test-content')"
			verify = "; assert Path('game.pak').read_bytes() == b'PACK-test-content'"
		case "sqlite-ledger":
			modify = "; import sqlite3; db=sqlite3.connect('ledger.db'); db.execute('CREATE TABLE ledger(amount INTEGER)'); db.execute('INSERT INTO ledger VALUES (100)'); db.commit(); db.close()"
			verify = "; import sqlite3; db=sqlite3.connect('ledger.db'); assert db.execute('SELECT sum(amount) FROM ledger').fetchone()[0] == 100; db.close()"
		}
		for name, extra := range map[string]string{"modify": modify, "verify": verify} {
			w := profile.Workflows[name]
			w.Args[1] += extra
			profile.Workflows[name] = w
		}
		raw, err = json.Marshal(profile)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(profileFile, raw, 0600); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"add", ".mcpshka/profile.json"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@localhost", "commit", "-qm", "private project profile"}} {
			cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("profile: %s %v", out, err)
			}
		}
	}
	git := gitops.New(m.Policy())
	accepted := false
	object := func(v any) map[string]any {
		raw, _ := json.Marshal(v)
		out := map[string]any{}
		json.Unmarshal(raw, &out)
		return out
	}
	call := func(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
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
			r, err := e.Get("linked-run")
			return object(r), err
		case "acceptance_status":
			head, _ := git.Revision(root)
			tree, _ := git.Tree(root)
			inputs := evidence.Inputs{Head: head, Tree: tree}
			state := "MISSING"
			if accepted {
				state = "PASS"
			}
			gates := []string{"engineering"}
			if kind != "" {
				gates = []string{"engineering", "integration", "release", "deploy", "acceptance", "cleanup"}
			}
			var records []evidence.GateState
			for _, gate := range gates {
				records = append(records, evidence.GateState{Gate: gate, State: state, Record: &evidence.Record{Result: state, Inputs: inputs}})
			}
			return object(mission.Acceptance{Done: accepted, Inputs: inputs, Gates: records}), nil
		default:
			t.Fatal("unexpected mission effect", name)
			return nil, nil
		}
	}
	controller := mission.NewController(m.Database(), git, call)
	e.OnTerminal = controller.WorkflowCompleted
	plan := mission.Plan{Path: root, Goal: "complete P7", Mode: "CLOSE_STEP", Step: "P7", WorkflowID: "linked-run"}
	if kind == "" {
		if _, err := e.Start(root, "release", "linked-run", sha, "P7"); err != nil {
			t.Fatal(err)
		}
	} else {
		pin, err := e.Preview(root, "release", "linked-run", sha, "P7")
		if err != nil {
			t.Fatal(err)
		}
		plan.WorkflowPlan = &pin
		plan.Mode = "REPAIR_AND_RELEASE"
		controller.WorkflowDispatch = func(pin projectprofile.PipelinePin, task string, deadline *time.Time, snapshot []byte) error {
			_, err := e.StartMission(pin, task, deadline, snapshot)
			return err
		}
	}
	s, err := controller.Start(context.Background(), mission.StartInput{Plan: plan, IdempotencyKey: "original-mission"})
	if err != nil || s.State == "DONE" {
		t.Fatal(s, err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		r, err := e.Tick("linked-run")
		if r.State == "done" {
			if err == nil {
				t.Fatal("unproven acceptance discarded durable completion")
			}
			break
		}
		if err != nil || time.Now().After(deadline) {
			t.Fatal("workflow did not finish", r, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	before, _ := os.ReadFile(filepath.Join(root, "events"))
	// This separate fixture gate models reviewed committed evidence arriving later.
	for _, args := range [][]string{{"add", "."}, {"-c", "user.name=Gate", "-c", "user.email=gate@localhost", "commit", "-qm", "accepted fixture"}} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("gate commit: %v %s", err, out)
		}
	}
	accepted = true
	cold := New(m)
	e = cold
	controller = mission.NewController(m.Database(), git, call)
	cold.OnTerminal = controller.WorkflowCompleted
	if err := cold.Maintain(); err != nil {
		t.Fatal(err)
	}
	final, err := controller.Status(context.Background(), s.ID)
	if err != nil || final.State != "DONE" || final.LastProven == nil {
		t.Fatal("accepted mission not resumed automatically", final, err)
	}
	after, _ := os.ReadFile(filepath.Join(root, "events"))
	if string(before) != string(after) {
		t.Fatal("acceptance retry launched another stage")
	}
}
