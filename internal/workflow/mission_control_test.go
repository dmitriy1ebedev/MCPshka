package workflow

import (
	"errors"
	"mcpshka/internal/jobs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type missionControlledWorkflow interface {
	BindMission(string, string, *time.Time) error
	ControlMission(string, string, string) error
}

func workflowMissionControls(t *testing.T, e *Engine) missionControlledWorkflow {
	t.Helper()
	controls, ok := any(e).(missionControlledWorkflow)
	if !ok {
		t.Fatal("workflow cannot persist owner mission controls or enforce the task deadline")
	}
	return controls
}

func TestMissionControlPauseSurvivesRestartWithoutDispatch(t *testing.T) {
	e, m, root, sha := canary(t, "")
	if _, err := e.Start(root, "release", "controlled-pause", sha, "P7"); err != nil {
		t.Fatal(err)
	}
	c := workflowMissionControls(t, e)
	task := strings.Repeat("a", 32)
	if err := c.BindMission("controlled-pause", task, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.ControlMission("controlled-pause", task, "paused"); err != nil {
		t.Fatal(err)
	}
	cold := New(m)
	for range 3 {
		r, err := cold.Tick("controlled-pause")
		if err != nil || r.LaunchClaimed || r.Index != 0 {
			t.Fatal("paused task dispatched", r, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "events")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("paused task performed an effect", err)
	}
	if _, err := cold.Resume("controlled-pause"); err != nil {
		t.Fatal(err)
	}
	r, _ := cold.Get("controlled-pause")
	if r.LaunchClaimed {
		t.Fatal("workflow_resume bypassed the parent mission pause")
	}
	if err := c.ControlMission("controlled-pause", strings.Repeat("b", 32), "running"); err == nil {
		t.Fatal("another task resumed the workflow")
	}
	if err := c.ControlMission("controlled-pause", task, "running"); err != nil {
		t.Fatal(err)
	}
	r, err := cold.Tick("controlled-pause")
	if err != nil || !r.LaunchClaimed {
		t.Fatal("authorized resume did not dispatch", r, err)
	}
}

func TestMissionControlStoppedTaskKeepsOriginalJobThenCompensates(t *testing.T) {
	for _, mode := range []string{"cancelled", "expired"} {
		t.Run(mode, func(t *testing.T) {
			e, m, root, sha := canary(t, "")
			const id = "controlled-cancel"
			if _, err := e.Start(root, "release", id, sha, "P7"); err != nil {
				t.Fatal(err)
			}
			c := workflowMissionControls(t, e)
			task := strings.Repeat("a", 32)
			if err := c.BindMission(id, task, nil); err != nil {
				t.Fatal(err)
			}
			started, err := e.Tick(id)
			if err != nil || !started.LaunchClaimed {
				t.Fatal(started, err)
			}
			if mode == "cancelled" {
				if err := c.ControlMission(id, task, "cancelled"); err != nil {
					t.Fatal(err)
				}
			} else {
				expired := time.Now().Add(-time.Second)
				if err := c.BindMission(id, task, &expired); err != nil {
					t.Fatal(err)
				}
			}
			observed, _ := e.Get(id)
			if observed.JobID != started.JobID || !observed.LaunchClaimed {
				t.Fatal("cancel forgot the original admitted job", observed)
			}
			if err := c.ControlMission(id, task, "running"); err == nil {
				t.Fatal("cancelled task resumed")
			}
			cold := New(m)
			deadline := time.Now().Add(20 * time.Second)
			for {
				r, err := cold.Tick(id)
				if err != nil {
					t.Fatal(r, err)
				}
				if r.State == "rolled_back" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("cancel did not settle", r)
				}
				time.Sleep(10 * time.Millisecond)
			}
			raw, err := os.ReadFile(filepath.Join(root, "events"))
			if err != nil {
				t.Fatal(err)
			}
			if string(raw) != "inspect\nrollback\nfinalizer\n" {
				t.Fatal("cancel dispatched normal work or repeated its original effect", string(raw))
			}
			if claim, err := cold.ProjectClaim(root); err != nil || claim != nil {
				t.Fatal("verified compensation left a writer claim", claim, err)
			}

		})
	}
}

func TestMissionControlDeadlineCannotExtendOrRunNormalStages(t *testing.T) {
	e, _, root, sha := canary(t, "")
	const id = "controlled-deadline"
	if _, err := e.Start(root, "release", id, sha, "P7"); err != nil {
		t.Fatal(err)
	}
	c := workflowMissionControls(t, e)
	task := strings.Repeat("a", 32)
	deadline := time.Now().Add(time.Minute)
	if err := c.BindMission(id, task, &deadline); err != nil {
		t.Fatal(err)
	}
	later := deadline.Add(time.Hour)
	if err := c.BindMission(id, task, &later); err == nil {
		t.Fatal("task deadline extended")
	}
	expired := time.Now().Add(-time.Second)
	if err := c.BindMission(id, task, &expired); err != nil {
		t.Fatal(err)
	}
	r, err := e.Tick(id)
	if err != nil || r.LaunchClaimed {
		t.Fatal("expired task dispatched", r, err)
	}
	r, err = e.Tick(id)
	if err != nil || r.State != "cancelled" {
		t.Fatal("unstarted expiry did not release safely", r, err)
	}
	if _, err := os.Stat(filepath.Join(root, "events")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("deadline caused an effect", err)
	}
	if claim, err := e.ProjectClaim(root); err != nil || claim != nil {
		t.Fatal("unstarted expiry retained a claim", claim, err)
	}
	if _, err := e.manager.Get(stageJobID(id, 0)); !errors.Is(err, jobs.ErrNotFound) {
		t.Fatal("expiry invented a stage receipt", err)
	}
}

func TestMissionControlStopsStaleLaunchCAS(t *testing.T) {
	e, _, root, sha := canary(t, "")
	const id = "controlled-race"
	if _, err := e.Start(root, "release", id, sha, "P7"); err != nil {
		t.Fatal(err)
	}
	c := workflowMissionControls(t, e)
	task := strings.Repeat("a", 32)
	if err := c.BindMission(id, task, nil); err != nil {
		t.Fatal(err)
	}
	old, err := e.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ControlMission(id, task, "paused"); err != nil {
		t.Fatal(err)
	}
	launch := old
	launch.LaunchClaimed = true
	if _, err := e.save(old, launch); err == nil {
		t.Fatal("a stale admission crossed the committed pause")
	}
	if err := c.ControlMission(id, task, "cancelled"); err != nil {
		t.Fatal(err)
	}
	r, err := e.Tick(id)
	if err != nil || r.State != "cancelled" {
		t.Fatal("unstarted cancellation failed", r, err)
	}
	if _, err := e.Start(root, "release", id, sha, "P7"); err != nil {
		t.Fatal("lost reply to cancellation cannot be reconciled", err)
	}
}

func TestMissionControlPauseDuringFinalStageCanResumeAcceptance(t *testing.T) {
	e, _, root, sha := canary(t, "")
	const id = "controlled-final"
	if _, err := e.Start(root, "release", id, sha, "P7"); err != nil {
		t.Fatal(err)
	}
	c := workflowMissionControls(t, e)
	task := strings.Repeat("a", 32)
	if err := c.BindMission(id, task, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		r, err := e.Tick(id)
		if err != nil {
			t.Fatal(err)
		}
		if r.Index == 9 && r.LaunchClaimed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pipeline did not reach final stage", r.State, r.Index)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := c.ControlMission(id, task, "paused"); err != nil {
		t.Fatal(err)
	}
	done := finish(t, e, id)
	if done.State != "done" || done.Control != "paused" {
		t.Fatal("admitted final stage was forgotten", done.State, done.Control)
	}
	if err := c.ControlMission(id, task, "running"); err != nil {
		t.Fatal(err)
	}
	resumed, err := e.Get(id)
	if err != nil || resumed.Control != "" {
		t.Fatal("finalized workflow trapped mission acceptance in PAUSED", resumed.Control, err)
	}
}
