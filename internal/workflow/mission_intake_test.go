package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mcpshka/internal/failure"
	"mcpshka/internal/mission"
	"mcpshka/internal/projectprofile"
)

func TestMissionIntakePinsBeforeEffectAndCancelsBeforeStageZero(t *testing.T) {
	e, _, root, sha := canary(t, "")
	pin, err := e.Preview(root, "release", "intake-cancel", sha, "P7")
	if err != nil {
		t.Fatal(err)
	}
	if count, err := e.db.Count("pipelines/"); err != nil || count != 0 {
		t.Fatalf("preview changed authority: %d %v", count, err)
	}
	c := controlledMissionFixture(t, e, root, pin.ID)
	c.WorkflowDispatch = func(plan projectprofile.PipelinePin, task string, deadline *time.Time, snapshot []byte) error {
		_, err := e.StartMission(plan, task, deadline, snapshot)
		return err
	}
	deadline := time.Now().Add(time.Minute)
	input := mission.StartInput{Plan: mission.Plan{Path: root, Goal: "execute exact preview", Mode: "CLOSE_STEP", Step: "P7", WorkflowPlan: &pin, Deadline: &deadline}, IdempotencyKey: "intake-cancel"}
	s, err := c.Start(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	r, err := e.Get(pin.ID)
	if err != nil || r.MissionID != s.ID || r.Deadline == nil || !r.Deadline.Equal(deadline) || r.LaunchClaimed {
		t.Fatal("parent and control not present before dispatch", r, err)
	}
	if err := e.requireWorkflowClaim(r); err != nil {
		t.Fatal(err)
	}
	again, err := c.Start(context.Background(), input)
	if err != nil || again.ID != s.ID {
		t.Fatal("lost reply changed task", again, err)
	}
	if _, err = c.Cancel(mission.ControlInput{ID: s.ID}); err != nil {
		t.Fatal(err)
	}
	e.OnTerminal = c.WorkflowCompleted
	r, err = e.Tick(pin.ID)
	if err != nil || r.State != "cancelled" {
		t.Fatal("cancel before launch", r, err)
	}
	if _, err := os.Stat(filepath.Join(root, "events")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled intake ran", err)
	}
}

func TestMissionIntakeRejectsChangedPreviewWithoutRecords(t *testing.T) {
	e, _, root, sha := canary(t, "")
	pin, err := e.Preview(root, "release", "intake-pins", sha, "P7")
	if err != nil {
		t.Fatal(err)
	}
	c := controlledMissionFixture(t, e, root, pin.ID)
	c.WorkflowDispatch = func(plan projectprofile.PipelinePin, task string, deadline *time.Time, snapshot []byte) error {
		_, err := e.StartMission(plan, task, deadline, snapshot)
		return err
	}
	pin.Revision = "0000000000000000000000000000000000000000"
	_, err = c.Start(context.Background(), mission.StartInput{Plan: mission.Plan{Path: root, Goal: "reject stale preview", Mode: "CLOSE_STEP", Step: "P7", WorkflowPlan: &pin}, IdempotencyKey: "stale-preview"})
	if err == nil {
		t.Fatal("stale source admitted")
	}
	for _, prefix := range []string{"missions/", "pipelines/"} {
		if count, err := e.db.Count(prefix); err != nil || count != 0 {
			t.Fatalf("rejected preview left %s: %d %v", prefix, count, err)
		}
	}
}

func TestMissionIntakeCapacityNeverStrandsChildClaim(t *testing.T) {
	e, _, root, sha := canary(t, "")
	pin, err := e.Preview(root, "release", "intake-capacity", sha, "P7")
	if err != nil {
		t.Fatal(err)
	}
	c := controlledMissionFixture(t, e, root, pin.ID)
	c.WorkflowDispatch = func(plan projectprofile.PipelinePin, task string, deadline *time.Time, snapshot []byte) error {
		_, err := e.StartMission(plan, task, deadline, snapshot)
		return err
	}
	// A full parent namespace must prevent both the child and writer claim.
	for i := range 256 {
		if ok, err := e.db.PutIfAbsent("missions/"+time.Unix(int64(i), 0).Format("150405"), []byte("{}")); err != nil || !ok {
			t.Fatal(ok, err)
		}
	}
	_, err = c.Start(context.Background(), mission.StartInput{Plan: mission.Plan{Path: root, Goal: "capacity", Mode: "CLOSE_STEP", Step: "P7", WorkflowPlan: &pin}, IdempotencyKey: "full-intake"})
	if err == nil {
		t.Fatal("full namespace admitted")
	}
	if !failure.IsNoEffect(err) {
		t.Fatal("proven empty intake became UNKNOWN", err)
	}
	if count, err := e.db.Count("pipelines/"); err != nil || count != 0 {
		t.Fatal("partial child", count, err)
	}
}

func TestMissionIntakeLostReplyColdExecutorNeverDuplicates(t *testing.T) {
	e, m, root, sha := canary(t, "")
	pin, err := e.Preview(root, "release", "intake-cold", sha, "P7")
	if err != nil {
		t.Fatal(err)
	}
	c := controlledMissionFixture(t, e, root, pin.ID)
	c.WorkflowDispatch = func(plan projectprofile.PipelinePin, task string, deadline *time.Time, snapshot []byte) error {
		if _, err := e.StartMission(plan, task, deadline, snapshot); err != nil {
			return err
		}
		return errors.New("response lost after atomic intake")
	}
	in := mission.StartInput{Plan: mission.Plan{Path: root, Goal: "cold intake", Mode: "CLOSE_STEP", Step: "P7", WorkflowPlan: &pin}, IdempotencyKey: "cold-intake"}
	if _, err := c.Start(context.Background(), in); err == nil {
		t.Fatal("fixture did not lose reply")
	}
	cold := New(m)
	recovered := controlledMissionFixture(t, cold, root, pin.ID)
	s, err := recovered.Start(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if s.WorkflowID != pin.ID {
		t.Fatal("lost original workflow", s)
	}
	// The normal maintenance path owns stage execution; no new Start is needed.
	done := finish(t, cold, pin.ID)
	if done.State != "done" {
		t.Fatal(done)
	}
	raw, err := os.ReadFile(filepath.Join(root, "events"))
	if err != nil || string(raw) != "inspect\nmodify\ntest\nreview\ncommit\nbuild\ndeploy\nreconnect\nverify\ncleanup\n" {
		t.Fatal("duplicate/skipped stages", string(raw), err)
	}
	if err = recovered.WorkflowCompleted(pin.ID); !errors.Is(err, mission.ErrAcceptancePending) {
		t.Fatal("job done fabricated production acceptance", err)
	}
	// Durable intake remains decodable after workflow effects make the tree dirty.
	snapshot, err := e.db.Get("missions/" + s.ID + ".json")
	if err != nil || !json.Valid(snapshot) {
		t.Fatal("lost durable parent", err)
	}
}

func TestMissionIntakePreviewPreflightIsReadyWithoutCreatingWorkflow(t *testing.T) {
	e, _, root, sha := canary(t, "")
	pin, err := e.Preview(root, "release", "preview-preflight", sha, "P7")
	if err != nil {
		t.Fatal(err)
	}
	c := controlledMissionFixture(t, e, root, pin.ID)
	out, err := c.Preflight(context.Background(), mission.Plan{Path: root, Goal: "preflight preview", Mode: "CLOSE_STEP", Step: "P7", WorkflowPlan: &pin})
	if err != nil || out.State != "READY" || len(out.Steps) != 1 || out.Steps[0].Proven {
		t.Fatal("valid unstarted preview held or fabricated acceptance", out, err)
	}
	for _, prefix := range []string{"missions/", "pipelines/"} {
		if count, err := e.db.Count(prefix); err != nil || count != 0 {
			t.Fatal("read-only preflight created authority", prefix, count, err)
		}
	}
}

func TestMissionIntakeReplayChecksEntireOriginalPreview(t *testing.T) {
	e, _, root, sha := canary(t, "")
	pin, err := e.Preview(root, "release", "intake-replay-pin", sha, "P7")
	if err != nil {
		t.Fatal(err)
	}
	c := controlledMissionFixture(t, e, root, pin.ID)
	c.WorkflowDispatch = func(plan projectprofile.PipelinePin, task string, deadline *time.Time, snapshot []byte) error {
		_, err := e.StartMission(plan, task, deadline, snapshot)
		return err
	}
	s, err := c.Start(context.Background(), mission.StartInput{Plan: mission.Plan{Path: root, Goal: "replay pins", Mode: "CLOSE_STEP", Step: "P7", WorkflowPlan: &pin}, IdempotencyKey: "replay-pin"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := e.db.Get("missions/" + s.ID + ".json")
	if err != nil {
		t.Fatal(err)
	}
	pin.Fingerprint = "0000000000000000000000000000000000000000000000000000000000000000"
	if _, err := e.StartMission(pin, s.ID, nil, raw); err == nil {
		t.Fatal("existing run accepted a different preview fingerprint")
	}
}

func TestMissionIntakeConcurrentPlanCannotClaimWinner(t *testing.T) {
	e, _, root, sha := canary(t, "")
	pin, err := e.Preview(root, "release", "intake-plan-race", sha, "P7")
	if err != nil {
		t.Fatal(err)
	}
	winner := controlledMissionFixture(t, e, root, pin.ID)
	loser := controlledMissionFixture(t, e, root, pin.ID)
	dispatch := func(plan projectprofile.PipelinePin, task string, deadline *time.Time, snapshot []byte) error {
		_, err := e.StartMission(plan, task, deadline, snapshot)
		return err
	}
	winner.WorkflowDispatch = dispatch
	first := mission.StartInput{Plan: mission.Plan{Path: root, Goal: "winning owner plan", Mode: "CLOSE_STEP", Step: "P7", WorkflowPlan: &pin}, IdempotencyKey: "winner"}
	second := first
	second.Goal = "different losing plan"
	second.IdempotencyKey = "loser"
	// Deterministically interleave after both have observed an absent parent.
	loser.WorkflowDispatch = func(plan projectprofile.PipelinePin, task string, deadline *time.Time, snapshot []byte) error {
		if _, err := winner.Start(context.Background(), first); err != nil {
			return err
		}
		return dispatch(plan, task, deadline, snapshot)
	}
	if _, err := loser.Start(context.Background(), second); err == nil {
		t.Fatal("losing request claimed another accepted mission plan")
	}
	s, err := winner.Start(context.Background(), first)
	if err != nil || s.Goal != first.Goal {
		t.Fatal("winner plan lost", s.Goal, err)
	}
	r, err := e.Get(pin.ID)
	if err != nil || r.MissionID != s.ID || r.LaunchClaimed {
		t.Fatal("race widened work", r, err)
	}
}
