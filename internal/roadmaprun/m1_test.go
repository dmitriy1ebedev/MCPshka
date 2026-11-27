package roadmaprun

import (
	"context"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/roadmapnext"
)

func TestOrdinaryProjectPromptUsesFFMerge(t *testing.T) {
	p := Prompt("/root/repos/example", roadmapnext.Item{ID: "P1", Title: "task"}, "", "", "token")
	if !strings.Contains(p, "ff-merge") || !strings.Contains(p, "sealed project_test") {
		t.Fatal("ordinary project lacks ff-merge and sealed test route")
	}
}

func TestPromptProductionRequiresExplicitGoalAndGuards(t *testing.T) {
	for _, tc := range []struct {
		name string
		item roadmapnext.Item
	}{
		{name: "goal", item: roadmapnext.Item{Title: "выпустить production версию MCPshka"}},
		{name: "phase", item: roadmapnext.Item{ID: "P1", Title: "task"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := Prompt("/root/repos/mcpshka", tc.item, "", "", "token")
			if tc.item.ID == "" {
				for _, required := range []string{"только если это явно разрешено задачей владельца", "approval", "fencing", "UNKNOWN", "sealed project_test"} {
					if !strings.Contains(p, required) {
						t.Fatalf("goal prompt missing %q", required)
					}
				}
				if strings.Contains(p, "Не трогай прод") {
					t.Fatal("goal prompt contradicts owner-authorized production work")
				}
			} else if !strings.Contains(p, "Не трогай прод") {
				t.Fatal("phase prompt lost production restriction")
			}
			if !strings.Contains(p, "Не обходи защиту MCPshka") || !strings.Contains(p, "не запускай вложенный roadmap_run") {
				t.Fatal("prompt lost mandatory guards")
			}
		})
	}
}

func TestGoalRunsSealedTestAndRetriesWithDiagnosis(t *testing.T) {
	withStateDir(t)
	calls, tests, diagnoses := 0, 0, 0
	c := Config{Agents: []string{"codex", "claude"}, ID: "goal", Path: "/p", Goal: "fix it", Budget: time.Hour, MaxPhases: 1,
		RunAgent: func(_ context.Context, _, _, prompt string) (string, error) {
			calls++
			if !strings.Contains(prompt, "fix it") {
				t.Fatal(prompt)
			}
			if calls == 2 && !strings.Contains(prompt, "admission unverified") {
				t.Fatal("missing diagnostic", prompt)
			}
			return "TASK_DONE", nil
		},
		Test: func(context.Context, string, string, string, func(string) error) (string, string, string, error) {
			tests++
			if tests == 1 {
				return "job1", "", "test failed", nil
			}
			return "job2", "head", "", nil
		},
		Diagnose: func(context.Context) (string, error) { diagnoses++; return "admission unverified", nil },
	}
	j, err := Run(context.Background(), c, "")
	if err != nil || j.State != "done" || calls != 2 || tests != 2 || diagnoses != 1 || j.LastJob != "job2" || j.Goal != "fix it" {
		t.Fatalf("%+v %v calls=%d tests=%d diagnostics=%d", j, err, calls, tests, diagnoses)
	}
}

func TestGoalAgentBlockerIsRetriedWithoutOwnerReadingJournal(t *testing.T) {
	withStateDir(t)
	calls, tests := 0, 0
	c := Config{Agents: []string{"codex", "claude"}, ID: "blocked-agent", Path: "/p", Goal: "repair", Budget: time.Hour, MaxPhases: 1,
		RunAgent: func(_ context.Context, _, _, prompt string) (string, error) {
			calls++
			if calls == 1 {
				return "BLOCKED: owner must read journalctl", nil
			}
			if !strings.Contains(prompt, "own unit: admission unverified") {
				t.Fatal(prompt)
			}
			return "TASK_DONE", nil
		},
		Test: func(context.Context, string, string, string, func(string) error) (string, string, string, error) {
			tests++
			return "job", "head", "", nil
		},
		Diagnose: func(context.Context) (string, error) { return "admission unverified", nil },
	}
	j, err := Run(context.Background(), c, "")
	if err != nil || j.State != "done" || calls != 2 || tests != 1 {
		t.Fatalf("%+v %v calls=%d tests=%d", j, err, calls, tests)
	}
}

func TestGoalCannotPassWithoutSealedTestOrOnUnknownJob(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		withStateDir(t)
		calls := 0
		c := Config{Agents: []string{"codex", "claude"}, ID: "unknown", Goal: "repair", Path: "/p", MaxPhases: 1, Budget: time.Hour, RunAgent: func(context.Context, string, string, string) (string, error) { calls++; return "TASK_DONE", nil }}
		if unknown {
			c.Test = func(context.Context, string, string, string, func(string) error) (string, string, string, error) {
				return "job-unknown", "", "", context.DeadlineExceeded
			}
		}
		j, err := Run(context.Background(), c, "")
		if err != nil || j.State != "stopped" || (unknown && (calls != 1 || !strings.Contains(j.NextAction, "job-unknown"))) {
			t.Fatalf("%+v %v", j, err)
		}
	}
}

func TestRepeatedGoalFailureKeepsActionableNextStep(t *testing.T) {
	withStateDir(t)
	j, err := Run(context.Background(), Config{Agents: []string{"codex", "claude"}, ID: "needs-profile", Goal: "repair", MaxPhases: 1, Budget: time.Hour,
		RunAgent: func(context.Context, string, string, string) (string, error) { return "TASK_DONE", nil },
		Test: func(context.Context, string, string, string, func(string) error) (string, string, string, error) {
			return "", "", "project_onboard_preview → review → project_onboard", nil
		},
	}, "")
	if err != nil || j.State != "stopped" || !strings.Contains(j.NextAction, "project_onboard_preview") {
		t.Fatalf("%+v %v", j, err)
	}
}
